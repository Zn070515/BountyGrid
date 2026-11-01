package model

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

// CallStatus is the durable state of a logical model call. The transitions are
// explicit: RESERVED -> STARTED -> COMPLETED, or STARTED -> AMBIGUOUS /
// FAILED_BEFORE_SEND. Only FAILED_BEFORE_SEND is safe to retry, because the
// provider proved no request left the process.
type CallStatus string

const (
	// CallReserved reserves the idempotency key before any provider is chosen.
	CallReserved CallStatus = "RESERVED"
	// CallStarted is persisted immediately before the provider is invoked.
	CallStarted CallStatus = "STARTED"
	// CallCompleted means the provider returned a response and it was recorded.
	CallCompleted CallStatus = "COMPLETED"
	// CallAmbiguous means the provider returned an error but may still have
	// processed (and charged for) the request. It must never be retried
	// automatically.
	CallAmbiguous CallStatus = "AMBIGUOUS"
	// CallFailedBeforeSend means the provider proved the request was not sent
	// (domain.ErrNotSent), so a retry under the same CallID is safe.
	CallFailedBeforeSend CallStatus = "FAILED_BEFORE_SEND"
)

func (s CallStatus) Valid() bool {
	switch s {
	case CallReserved, CallStarted, CallCompleted, CallAmbiguous, CallFailedBeforeSend:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the status is final. A terminal status may never
// be overwritten by a stale writer; FAILED_BEFORE_SEND may only be reclaimed
// (deleted) before a retry.
func (s CallStatus) IsTerminal() bool {
	switch s {
	case CallCompleted, CallAmbiguous, CallFailedBeforeSend:
		return true
	default:
		return false
	}
}

// CanTransition reports whether a logical call may move from s to to. A call
// advances from RESERVED/STARTED only. In particular, a STARTED call may be
// re-written as STARTED for the next provider attempt in the same fallback
// chain, but a terminal call may never move to another state.
func (s CallStatus) CanTransition(to CallStatus) bool {
	if !to.Valid() {
		return false
	}
	switch s {
	case CallReserved, CallStarted:
		return to == CallStarted || to.IsTerminal()
	default:
		return false
	}
}

// ModelCall is the logical call. One logical call may span several provider
// attempts (fallback, ambiguous retry after reconciliation, and so on). The
// logical record carries the final outcome and the total cost, including
// estimates for ambiguous attempts that may already have been charged.
type ModelCall struct {
	ID                  string
	TaskID              domain.TaskID
	StepID              string
	Purpose             Purpose
	DataClass           DataClass
	FinalStatus         CallStatus
	TotalCost           domain.Money
	HasAmbiguousAttempt bool
	StartedAt           time.Time
	EndedAt             time.Time
	Error               string
}

// ModelCallAttempt is one provider invocation. It stores hashes, never prompt
// or response content. Status is the attempt outcome: COMPLETED, AMBIGUOUS or
// FAILED_BEFORE_SEND.
type ModelCallAttempt struct {
	AttemptID     string
	CallID        string
	Provider      string
	Model         string
	Status        CallStatus
	EstimatedCost domain.Money
	ActualCost    domain.Money
	Usage         Usage
	PromptHash    string
	ResponseHash  string
	StartedAt     time.Time
	EndedAt       time.Time
	Error         string
}

type CallLog interface {
	Add(ModelCallAttempt)
	List() []ModelCallAttempt
}

type MemoryCallLog struct {
	mu      sync.Mutex
	records []ModelCallAttempt
}

func NewMemoryCallLog() *MemoryCallLog { return &MemoryCallLog{} }

func (l *MemoryCallLog) Add(r ModelCallAttempt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
}

func (l *MemoryCallLog) List() []ModelCallAttempt {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ModelCallAttempt, len(l.records))
	copy(out, l.records)
	return out
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

// CallStore durably reserves a logical call before a provider is invoked,
// records its outcome, and appends one attempt row per provider invocation. A
// retry with the same CallID sees the reservation and must not pay for a second
// model invocation.
type CallStore interface {
	Reserve(ctx context.Context, id string, rec ModelCall) (bool, error)
	// Complete applies a state transition. Implementations must reject a stale
	// or invalid transition with ErrInvalidCallTransition: a terminal call may
	// not be overwritten, and a call may only advance from RESERVED/STARTED.
	Complete(ctx context.Context, id string, rec ModelCall) error
	Get(ctx context.Context, id string) (ModelCall, bool, error)
	// Reclaim deletes a FAILED_BEFORE_SEND reservation so the same CallID can
	// be reserved and retried. It must refuse any other status: those calls may
	// already have reached (and been charged by) the provider.
	Reclaim(ctx context.Context, id string) error
	// RecordAttempt appends one provider attempt to the durable ledger. It is
	// separate from Complete so the attempts survive even when the logical
	// call's final status is written later.
	RecordAttempt(ctx context.Context, attempt ModelCallAttempt) error
}

type MemoryCallStore struct {
	mu       sync.Mutex
	records  map[string]ModelCall
	attempts map[string][]ModelCallAttempt
}

func NewMemoryCallStore() *MemoryCallStore {
	return &MemoryCallStore{
		records:  make(map[string]ModelCall),
		attempts: make(map[string][]ModelCallAttempt),
	}
}

func (s *MemoryCallStore) Reserve(ctx context.Context, id string, rec ModelCall) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; ok {
		return false, nil
	}
	s.records[id] = rec
	return true, nil
}

func (s *MemoryCallStore) Complete(ctx context.Context, id string, rec ModelCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.records[id]
	if !ok {
		return fmt.Errorf("model: complete call %s: not found", id)
	}
	from := existing.FinalStatus
	if from == "" {
		from = CallReserved
	}
	if rec.FinalStatus == "" {
		rec.FinalStatus = CallCompleted
	}
	if !from.CanTransition(rec.FinalStatus) {
		return fmt.Errorf("%w: call %s: %s -> %s",
			ErrInvalidCallTransition, id, from, rec.FinalStatus)
	}
	s.records[id] = rec
	return nil
}

func (s *MemoryCallStore) Get(ctx context.Context, id string) (ModelCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	return r, ok, nil
}

func (s *MemoryCallStore) Reclaim(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return fmt.Errorf("model: reclaim call %s: not found", id)
	}
	if rec.FinalStatus != CallFailedBeforeSend {
		return fmt.Errorf("model: reclaim call %s: status %q is not FAILED_BEFORE_SEND", id, rec.FinalStatus)
	}
	delete(s.records, id)
	delete(s.attempts, id)
	return nil
}

func (s *MemoryCallStore) RecordAttempt(ctx context.Context, attempt ModelCallAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[attempt.CallID] = append(s.attempts[attempt.CallID], attempt)
	return nil
}

// Attempts returns a copy of the attempts recorded for a logical call. It is a
// test/inspection helper; the durable interface is RecordAttempt.
func (s *MemoryCallStore) Attempts(callID string) []ModelCallAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModelCallAttempt, len(s.attempts[callID]))
	copy(out, s.attempts[callID])
	return out
}

type Options struct {
	Providers []Provider
	Auditor   Auditor
	CallLog   CallLog
	CallStore CallStore
	Now       func() time.Time
	// RequireStableCallID rejects a request without an explicit CallID. Worker
	// mode sets this so a crashed/retried worker resumes the same logical call
	// instead of paying for a new one. Interactive/adhoc callers leave it off
	// and keep auto-generated IDs.
	RequireStableCallID bool
	// PolicyResolver supplies the authoritative program egress policy, the
	// redaction attestation and the escalation decision. If nil, the Router
	// fails closed to LOCAL_ONLY with no escalation and no redaction. The
	// request's ModelEgress/Redacted/Escalation fields are never authority.
	PolicyResolver PolicyResolver
	// TrustedNetworks is the operator-configured allowlist for LOCAL_NETWORK
	// providers. A LOCAL_NETWORK provider must pin an IP literal inside one of
	// these CIDRs; without a matching prefix Router.New rejects it.
	TrustedNetworks []netip.Prefix
}

type Router struct {
	providers []Provider
	// infos is the validated snapshot of every provider's Info(). Routing reads
	// this instead of calling Info() again, so a provider cannot change its
	// declared trust boundary after New accepted it.
	infos               []ProviderInfo
	index               map[string]int
	auditor             Auditor
	log                 CallLog
	calls               CallStore
	now                 func() time.Time
	requireStableCallID bool
	policy              PolicyResolver
	trusted             []netip.Prefix
}

func New(opts Options) (*Router, error) {
	if len(opts.Providers) == 0 {
		return nil, fmt.Errorf("%w: provider list is empty", ErrNoProvider)
	}
	trusted, err := normalizeTrustedNetworks(opts.TrustedNetworks)
	if err != nil {
		return nil, err
	}
	infos := make([]ProviderInfo, 0, len(opts.Providers))
	index := make(map[string]int, len(opts.Providers))
	for i, p := range opts.Providers {
		if p == nil {
			return nil, fmt.Errorf("%w: nil provider", ErrInvalidRequest)
		}
		info := p.Info()
		if strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("%w: provider name is empty", ErrInvalidRequest)
		}
		if _, ok := index[info.Name]; ok {
			return nil, fmt.Errorf("%w: duplicate provider %q", ErrInvalidRequest, info.Name)
		}
		if err := validateProviderTrust(info, trusted); err != nil {
			return nil, err
		}
		index[info.Name] = i
		infos = append(infos, info)
	}
	if opts.CallLog == nil {
		opts.CallLog = NewMemoryCallLog()
	}
	if opts.CallStore == nil {
		opts.CallStore = NewMemoryCallStore()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Router{
		providers: opts.Providers, infos: infos, index: index,
		auditor: opts.Auditor, log: opts.CallLog, calls: opts.CallStore,
		now: opts.Now, requireStableCallID: opts.RequireStableCallID,
		policy: opts.PolicyResolver, trusted: trusted,
	}, nil
}

func normalizeTrustedNetworks(in []netip.Prefix) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		if !p.IsValid() {
			return nil, fmt.Errorf("%w: invalid trusted network %q", ErrInvalidRequest, p)
		}
		p = p.Masked()
		// Normalize IPv4-mapped prefixes so a v4-mapped endpoint and a plain v4
		// CIDR compare in the same family.
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return nil, fmt.Errorf("%w: invalid IPv4-mapped trusted network %q", ErrInvalidRequest, p)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		out = append(out, p)
	}
	return out, nil
}

// validateProviderTrust enforces the deployment boundary declared by a
// provider. It is checked once at New so routing cannot be widened later by a
// provider mutating its Info().
func validateProviderTrust(info ProviderInfo, trusted []netip.Prefix) error {
	if info.Trust != "" && !info.Trust.Valid() {
		return fmt.Errorf("%w: provider %q has invalid trust %q",
			ErrInvalidRequest, info.Name, info.Trust)
	}
	switch info.EffectiveTrust() {
	case TrustInProcess:
		if info.Endpoint != "" && !endpointIsLocal(info.Endpoint) {
			return fmt.Errorf("%w: provider %q declares IN_PROCESS but endpoint %q is not loopback/unix/static",
				ErrInvalidRequest, info.Name, info.Endpoint)
		}
	case TrustLocalNetwork:
		ip, ok := endpointIP(info.Endpoint)
		if !ok {
			return fmt.Errorf("%w: provider %q declares LOCAL_NETWORK but endpoint %q is not an IP literal",
				ErrInvalidRequest, info.Name, info.Endpoint)
		}
		if len(trusted) == 0 {
			return fmt.Errorf("%w: provider %q declares LOCAL_NETWORK but no trusted networks are configured",
				ErrInvalidRequest, info.Name)
		}
		if !trustedContains(trusted, ip) {
			return fmt.Errorf("%w: provider %q endpoint %s is outside TrustedNetworks",
				ErrInvalidRequest, info.Name, ip)
		}
		if info.Local && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			return fmt.Errorf("%w: provider %q declares local but endpoint %s is public",
				ErrInvalidRequest, info.Name, ip)
		}
	case TrustThirdParty:
		if info.Local {
			return fmt.Errorf("%w: provider %q declares local but is THIRD_PARTY",
				ErrInvalidRequest, info.Name)
		}
		// A non-empty remote endpoint with Local=false is the normal cloud case.
	}
	return nil
}

func (r *Router) CallLog() CallLog { return r.log }

// Candidates returns the eligible providers in routing order, without calling
// any of them. The authoritative policy comes from Options.PolicyResolver; a
// request's ModelEgress/Redacted/Escalation fields are ignored here. Resolver
// errors produce an empty plan (Route surfaces the error).
func (r *Router) Candidates(req ModelRequest) []ProviderInfo {
	out, _ := r.CandidatesContext(context.Background(), req)
	return out
}

// CandidatesContext is Candidates with an explicit context for the policy
// resolver. It is used by Route and by callers that need the resolver error.
func (r *Router) CandidatesContext(ctx context.Context, req ModelRequest) ([]ProviderInfo, error) {
	policy, err := r.resolvePolicy(ctx, req)
	if err != nil {
		return nil, err
	}
	return r.candidatesFor(req, policy), nil
}

func (r *Router) candidatesFor(req ModelRequest, policy ModelPolicy) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(r.infos))
	for _, info := range r.infos {
		if !eligible(req, policy, info, r.trusted) {
			continue
		}
		out = append(out, info)
	}
	slices.SortStableFunc(out, func(a, b ProviderInfo) int {
		if c := cmp.Compare(routeScore(policy.Escalation, a), routeScore(policy.Escalation, b)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// resolvePolicy obtains the authoritative routing policy. A nil resolver is not
// an error: it deliberately means LOCAL_ONLY, no escalation, no redaction.
func (r *Router) resolvePolicy(ctx context.Context, req ModelRequest) (ModelPolicy, error) {
	if r.policy == nil {
		return ModelPolicy{Egress: domain.ModelEgressLocalOnly}, nil
	}
	policy, err := r.policy.ResolveModelPolicy(ctx, req)
	if err != nil {
		return ModelPolicy{}, fmt.Errorf("model: resolve policy for program %q: %w", req.ProgramID, err)
	}
	if !policy.Egress.Valid() {
		policy.Egress = domain.ModelEgressLocalOnly
	}
	policy.Egress = policy.Egress.Effective()
	if !policy.Escalation.Valid() {
		policy.Escalation = EscalationNone
	}
	return policy, nil
}

func eligible(req ModelRequest, policy ModelPolicy, info ProviderInfo, trusted []netip.Prefix) bool {
	class := req.EffectiveDataClass()
	// SENSITIVE data never leaves the process, even to a trusted LAN model:
	// LOCAL_NETWORK is a different host and may log or forward the prompt.
	if class == DataSensitive && info.EffectiveTrust() != TrustInProcess {
		return false
	}
	if info.IsThirdParty() {
		// A third-party provider is never an implicit fallback: it needs an
		// authoritative escalation decision even when the program ceiling
		// allows remote.
		if policy.Escalation == EscalationNone {
			return false
		}
	} else if !info.IsLocalTrust(trusted) {
		// A LOCAL_NETWORK provider outside the trusted CIDRs (or an inconsistent
		// IN_PROCESS endpoint) is not part of the trusted boundary.
		return false
	}
	if !egressAllowed(policy, info, class, trusted) {
		return false
	}
	if !info.SupportsDataClass(class) || !info.SupportsPurpose(req.Purpose) {
		return false
	}
	if info.MaxOutputTokens > 0 && req.MaxTokens > info.MaxOutputTokens {
		return false
	}
	if info.MaxContextTokens > 0 {
		if PromptTokens(req)+req.MaxTokens > info.MaxContextTokens {
			return false
		}
	}
	for _, c := range req.RequiredCapabilities {
		if !info.SupportsCapability(c) {
			return false
		}
	}
	return true
}

// egressAllowed applies the resolved program-level model egress ceiling. The
// zero value and UNKNOWN are LOCAL_ONLY. IN_PROCESS and trusted LOCAL_NETWORK
// providers are always allowed; THIRD_PARTY providers require the resolved
// policy to permit remote egress and (for REMOTE_REDACTED_ONLY) an attested
// redaction that the resolver supplied.
func egressAllowed(policy ModelPolicy, info ProviderInfo, class DataClass, trusted []netip.Prefix) bool {
	localTrusted := info.IsLocalTrust(trusted)
	switch policy.Egress.Effective() {
	case domain.ModelEgressLocalOnly:
		return localTrusted
	case domain.ModelEgressRemoteRedactedOnly:
		if localTrusted {
			return true
		}
		return policy.Redacted && class != DataSensitive
	case domain.ModelEgressRemoteAllowed:
		return true
	default:
		// Invalid values fail closed to LOCAL_ONLY.
		return localTrusted
	}
}

// routeScore is lower-is-better and deterministic. The tiers are:
//
//	no escalation: local small -> stronger trusted-local
//	escalation:    remote strong -> stronger trusted-local
//
// Third-party providers are filtered out entirely when escalation is NONE, so
// the no-escalation path never includes a remote provider.
func routeScore(escalation EscalationReason, info ProviderInfo) int {
	const tier = 1_000_000
	local := !info.IsThirdParty()
	remoteRank := tier - info.Strength // strongest remote first
	if escalation != EscalationNone && escalation.Valid() {
		if !local {
			return remoteRank
		}
		return tier + tier - info.Strength // stronger local first, below remote
	}
	if local {
		return info.Strength // small local first
	}
	return remoteRank
}

// Route selects a provider, enforces the resolved policy, data class, output
// tokens and budget, records the call, and falls back to the next provider on
// failure. Model output is returned as text; it never becomes an ActionDecision
// or platform action.
//
// The logical call stays STARTED for the entire fallback chain. A provider
// returning domain.ErrNotSent produces a FAILED_BEFORE_SEND *attempt*, but the
// logical call is not finalized mid-chain: a concurrent retry must not be able
// to reclaim it while another attempt may still run. Only at the terminal end
// is the logical status written: COMPLETED when an attempt succeeded, AMBIGUOUS
// when any attempt may have reached the provider (even if the last one proved
// ErrNotSent), and FAILED_BEFORE_SEND only when every attempt proved nothing
// was sent. A retry with the same CallID may only proceed after a terminal
// FAILED_BEFORE_SEND; RESERVED/STARTED/AMBIGUOUS are refused as ambiguous.
func (r *Router) Route(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	if err := req.Validate(); err != nil {
		return ModelResponse{}, err
	}
	if r.requireStableCallID && strings.TrimSpace(req.CallID) == "" {
		return ModelResponse{}, fmt.Errorf("%w: set ModelRequest.CallID = StableCallID(task, step, purpose, promptHash)",
			ErrStableCallIDRequired)
	}
	policy, err := r.resolvePolicy(ctx, req)
	if err != nil {
		return ModelResponse{}, err
	}
	candidates := r.candidatesFor(req, policy)
	if len(candidates) == 0 {
		return ModelResponse{}, fmt.Errorf("%w: data_class=%s purpose=%s max_tokens=%d",
			ErrNoProvider, req.DataClass, req.Purpose, req.MaxTokens)
	}

	// Reserve the logical call before any provider is invoked. A retry with the
	// same CallID is resolved against the recorded state below.
	callID := strings.TrimSpace(req.CallID)
	if callID == "" {
		callID = callIDFor(req, r.now())
	}
	callStarted := r.now()
	if err := r.reserveCall(ctx, callID, req, callStarted); err != nil {
		return ModelResponse{}, err
	}

	base := func() ModelCall {
		return ModelCall{
			ID: callID, TaskID: req.TaskID, StepID: req.StepID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), StartedAt: callStarted,
		}
	}
	finish := func(rec ModelCall) error {
		if r.calls == nil {
			return nil
		}
		rec.ID = callID
		// The audit trail must not be lost because the caller's context was
		// cancelled after the provider responded.
		return r.calls.Complete(context.WithoutCancel(ctx), callID, rec)
	}
	joinFinish := func(err error, ferr error) error {
		if ferr != nil {
			return errors.Join(err, fmt.Errorf("model: persist call status: %w", ferr))
		}
		return err
	}

	budgeted := 0
	var spent int64
	var lastErr error
	sawAmbiguous := false
	// totalCost is the logical call cost: actual cost for completed attempts
	// plus the estimate for ambiguous attempts that may already have been
	// charged. FAILED_BEFORE_SEND attempts contributed nothing.
	totalCost := domain.Money{Currency: "CNY"}
	addCost := func(m domain.Money) {
		if totalCost.Currency == "" {
			totalCost.Currency = m.Currency
		}
		totalCost.Amount += m.Amount
	}
	// finalStatus is the terminal state for a chain that did not return a
	// response: any attempted-but-unproven send keeps the call AMBIGUOUS;
	// FAILED_BEFORE_SEND requires every attempt to prove ErrNotSent.
	finalStatus := func() CallStatus {
		if sawAmbiguous {
			return CallAmbiguous
		}
		return CallFailedBeforeSend
	}
	terminal := func(status CallStatus, errKind string) ModelCall {
		rec := base()
		rec.FinalStatus = status
		rec.TotalCost = totalCost
		rec.HasAmbiguousAttempt = sawAmbiguous
		rec.EndedAt = r.now()
		rec.Error = errKind
		return rec
	}
	for _, info := range candidates {
		provider := r.provider(info.Name)
		if provider == nil {
			continue
		}
		estimate := EstimateCost(info, req)
		if req.Budget.Amount > 0 {
			if !strings.EqualFold(req.Budget.Currency, estimate.Currency) {
				berr := fmt.Errorf("%w: budget currency %q does not match pricing %q",
					ErrInvalidRequest, req.Budget.Currency, estimate.Currency)
				// No request bytes left the process for this candidate, but an
				// earlier provider attempt may have been ambiguous. Never
				// downgrade that to a retryable FAILED_BEFORE_SEND.
				return ModelResponse{}, joinFinish(berr, finish(terminal(finalStatus(), "CURRENCY_MISMATCH")))
			}
			// The budget bounds the whole fallback chain, not one candidate.
			if spent+estimate.Amount > req.Budget.Amount {
				budgeted++
				continue
			}
		}

		started := r.now()
		// Persist STARTED before the provider sees the request. If this write
		// fails the provider is never invoked, so a crash cannot leave a call
		// that was paid for but recorded as merely RESERVED. STARTED -> STARTED
		// is valid: the next attempt of the same fallback chain updates the
		// logical record without finalizing it.
		startedRec := base()
		startedRec.FinalStatus = CallStarted
		startedRec.TotalCost = totalCost
		startedRec.HasAmbiguousAttempt = sawAmbiguous
		if ferr := finish(startedRec); ferr != nil {
			return ModelResponse{}, fmt.Errorf("model: persist started call: %w", ferr)
		}

		resp, err := provider.Complete(ctx, req)
		ended := r.now()
		if err != nil {
			// A failed provider may still have billed for the attempt. Count the
			// estimate against the chain budget so fallback cannot overspend.
			spent += estimate.Amount
			lastErr = err
			status := CallAmbiguous
			if errors.Is(err, domain.ErrNotSent) {
				status = CallFailedBeforeSend
			} else {
				sawAmbiguous = true
				addCost(estimate)
			}
			attempt := ModelCallAttempt{
				AttemptID: attemptIDFor(callID, started), CallID: callID,
				Provider: info.Name, Model: info.Model, Status: status,
				EstimatedCost: estimate, PromptHash: PromptHash(req),
				StartedAt: started, EndedAt: ended, Error: "PROVIDER_ERROR",
			}
			if rerr := r.recordAttempt(ctx, req, attempt); rerr != nil {
				return ModelResponse{}, joinFinish(
					errors.Join(err, fmt.Errorf("model: audit failed call: %w", rerr)),
					finish(terminal(finalStatus(), "AUDIT_FAILED")))
			}
			// Deliberately leave the logical call in STARTED. The attempt ledger
			// records this provider's outcome; a terminal write here would make a
			// mid-chain FAILED_BEFORE_SEND reclaimable by a concurrent retry.
			continue
		}
		cost := ActualCost(info, resp.Usage)
		spent += cost.Amount
		addCost(cost)
		attempt := ModelCallAttempt{
			AttemptID: attemptIDFor(callID, started), CallID: callID,
			Provider: info.Name, Model: info.Model, Status: CallCompleted,
			EstimatedCost: estimate, ActualCost: cost, Usage: resp.Usage,
			PromptHash: PromptHash(req), ResponseHash: ResponseHash(resp.Content),
			StartedAt: started, EndedAt: ended,
		}
		if req.Budget.Amount > 0 && spent > req.Budget.Amount {
			berr := fmt.Errorf("%w: accumulated cost %d exceeds budget %d",
				ErrBudgetExceeded, spent, req.Budget.Amount)
			attempt.Error = "BUDGET_EXCEEDED"
			if rerr := r.recordAttempt(ctx, req, attempt); rerr != nil {
				return ModelResponse{}, joinFinish(
					errors.Join(berr, fmt.Errorf("model: audit call: %w", rerr)),
					finish(terminal(CallCompleted, "AUDIT_FAILED")))
			}
			if ferr := finish(terminal(CallCompleted, "BUDGET_EXCEEDED")); ferr != nil {
				return ModelResponse{}, errors.Join(berr,
					fmt.Errorf("model: persist call status: %w", ferr))
			}
			return ModelResponse{}, berr
		}
		resp.Provider = info.Name
		if resp.Model == "" {
			resp.Model = info.Model
		}
		resp.Cost = cost
		resp.Latency = ended.Sub(started)
		if rerr := r.recordAttempt(ctx, req, attempt); rerr != nil {
			// The call happened and its content is still useful, but the audit
			// trail is incomplete. Return both rather than pretending success.
			return resp, joinFinish(fmt.Errorf("model: audit call: %w", rerr),
				finish(terminal(CallCompleted, "AUDIT_FAILED")))
		}
		// The provider call completed. If this write fails the content is still
		// returned, but the caller is told the call receipt is incomplete; the
		// error is never silently discarded.
		if ferr := finish(terminal(CallCompleted, "")); ferr != nil {
			return resp, fmt.Errorf("model: complete call: %w", ferr)
		}
		return resp, nil
	}
	if budgeted > 0 {
		berr := fmt.Errorf("%w: every eligible provider exceeds the remaining request budget", ErrBudgetExceeded)
		// No request bytes left the process for the blocked candidates. If an
		// earlier provider was ambiguous, the whole call stays ambiguous.
		return ModelResponse{}, joinFinish(berr, finish(terminal(finalStatus(), "BUDGET_EXCEEDED")))
	}
	if lastErr != nil {
		return ModelResponse{}, joinFinish(fmt.Errorf("model: all providers failed: %w", lastErr),
			finish(terminal(finalStatus(), "PROVIDER_ERROR")))
	}
	return ModelResponse{}, joinFinish(ErrNoProvider,
		finish(terminal(CallFailedBeforeSend, "NO_PROVIDER")))
}

// reserveCall reserves callID or resolves an existing record. A COMPLETED call
// is a duplicate. A FAILED_BEFORE_SEND call is reclaimed and reserved again
// because the provider proved it was never invoked. RESERVED, STARTED and
// AMBIGUOUS are refused: the provider may have been charged, and only a human
// can reconcile that.
func (r *Router) reserveCall(ctx context.Context, callID string, req ModelRequest, started time.Time) error {
	if r.calls == nil {
		return nil
	}
	reserve := func() (bool, error) {
		return r.calls.Reserve(ctx, callID, ModelCall{
			ID: callID, TaskID: req.TaskID, StepID: req.StepID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), FinalStatus: CallReserved,
			StartedAt: started,
		})
	}
	reserved, err := reserve()
	if err != nil {
		return fmt.Errorf("model: reserve call: %w", err)
	}
	if reserved {
		return nil
	}
	existing, found, err := r.calls.Get(ctx, callID)
	if err != nil {
		return fmt.Errorf("model: get existing call: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: %s (reservation exists but record is missing)", ErrCallAmbiguous, callID)
	}
	switch existing.FinalStatus {
	case CallCompleted:
		return fmt.Errorf("%w: %s", ErrDuplicateCall, callID)
	case CallFailedBeforeSend:
		if err := r.calls.Reclaim(ctx, callID); err != nil {
			return fmt.Errorf("model: reclaim call %s: %w", callID, err)
		}
		reserved, err := reserve()
		if err != nil {
			return fmt.Errorf("model: reserve call after reclaim: %w", err)
		}
		if !reserved {
			return fmt.Errorf("%w: %s: another caller reclaimed the reservation", ErrCallAmbiguous, callID)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s (status %s)", ErrCallAmbiguous, callID, existing.FinalStatus)
	}
}

func (r *Router) provider(name string) Provider {
	i, ok := r.index[name]
	if !ok || i < 0 || i >= len(r.providers) {
		return nil
	}
	return r.providers[i]
}

// recordAttempt writes the attempt to the durable ledger, the in-memory log and
// the audit trail. The ledger write happens first: an audit-only record would
// lose the cost of an ambiguous attempt across a restart.
func (r *Router) recordAttempt(ctx context.Context, req ModelRequest, attempt ModelCallAttempt) error {
	// The audit trail must not be lost because the caller's context was
	// cancelled after the provider responded.
	ctx = context.WithoutCancel(ctx)
	if r.calls != nil {
		if err := r.calls.RecordAttempt(ctx, attempt); err != nil {
			return fmt.Errorf("model: record attempt: %w", err)
		}
	}
	r.log.Add(attempt)
	if r.auditor == nil {
		return nil
	}
	payload, err := json.Marshal(struct {
		AttemptID     string       `json:"attempt_id"`
		CallID        string       `json:"call_id"`
		StepID        string       `json:"step_id,omitempty"`
		TaskID        string       `json:"task_id,omitempty"`
		ProgramID     string       `json:"program_id,omitempty"`
		Purpose       string       `json:"purpose"`
		DataClass     string       `json:"data_class"`
		Provider      string       `json:"provider"`
		Model         string       `json:"model"`
		Status        string       `json:"status"`
		EstimatedCost domain.Money `json:"estimated_cost"`
		ActualCost    domain.Money `json:"actual_cost"`
		PromptHash    string       `json:"prompt_hash"`
		ResponseHash  string       `json:"response_hash"`
		Usage         Usage        `json:"usage"`
		DurationMS    int64        `json:"duration_ms"`
		Error         string       `json:"error,omitempty"`
	}{
		AttemptID: attempt.AttemptID, CallID: attempt.CallID, StepID: req.StepID,
		TaskID: string(req.TaskID), ProgramID: string(req.ProgramID),
		Purpose: string(req.Purpose), DataClass: string(req.EffectiveDataClass()),
		Provider: attempt.Provider, Model: attempt.Model, Status: string(attempt.Status),
		EstimatedCost: attempt.EstimatedCost, ActualCost: attempt.ActualCost,
		PromptHash: attempt.PromptHash, ResponseHash: attempt.ResponseHash, Usage: attempt.Usage,
		DurationMS: attempt.EndedAt.Sub(attempt.StartedAt).Milliseconds(), Error: attempt.Error,
	})
	if err != nil {
		return err
	}
	if _, err := r.auditor.Append(ctx, audit.Event{
		Type: "model.call", EntityType: "task", EntityID: string(req.TaskID),
		ActorKind: string(domain.ActorModel), ActorID: attempt.Provider,
		Payload: payload, RecordedAt: attempt.EndedAt,
	}); err != nil {
		return fmt.Errorf("model: append audit: %w", err)
	}
	return nil
}

var callSeq atomic.Uint64
var attemptSeq atomic.Uint64

func callIDFor(req ModelRequest, at time.Time) string {
	task := string(req.TaskID)
	if task == "" {
		task = "adhoc"
	}
	// The sequence keeps two auto-generated calls in the same clock tick from
	// colliding. Explicit ModelRequest.CallID is still the idempotency key.
	return fmt.Sprintf("call/%s/%s/%d", task, at.UTC().Format("20060102T150405.000000000"), callSeq.Add(1))
}

func attemptIDFor(callID string, at time.Time) string {
	return fmt.Sprintf("attempt/%s/%s/%d", callID, at.UTC().Format("20060102T150405.000000000"), attemptSeq.Add(1))
}
