package model

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

type Router struct {
	providers           []Provider
	auditor             Auditor
	log                 CallLog
	calls               CallStore
	now                 func() time.Time
	requireStableCallID bool
}

func New(opts Options) (*Router, error) {
	if len(opts.Providers) == 0 {
		return nil, fmt.Errorf("%w: provider list is empty", ErrNoProvider)
	}
	seen := make(map[string]struct{}, len(opts.Providers))
	for _, p := range opts.Providers {
		if p == nil {
			return nil, fmt.Errorf("%w: nil provider", ErrInvalidRequest)
		}
		info := p.Info()
		if strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("%w: provider name is empty", ErrInvalidRequest)
		}
		if _, ok := seen[info.Name]; ok {
			return nil, fmt.Errorf("%w: duplicate provider %q", ErrInvalidRequest, info.Name)
		}
		// A provider may not claim locality while pointing at a remote endpoint.
		// SENSITIVE data is only routed to IsLocal() providers, so a false local
		// claim would be a data-egress hole.
		if info.Local && info.Endpoint != "" && !endpointIsLocal(info.Endpoint) {
			return nil, fmt.Errorf("%w: provider %q declares local but endpoint %q is remote",
				ErrInvalidRequest, info.Name, info.Endpoint)
		}
		seen[info.Name] = struct{}{}
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
		providers: opts.Providers, auditor: opts.Auditor, log: opts.CallLog,
		calls: opts.CallStore, now: opts.Now, requireStableCallID: opts.RequireStableCallID,
	}, nil
}

func (r *Router) CallLog() CallLog { return r.log }

// Candidates returns the eligible providers in routing order, without calling
// any of them. It is used by the dry-run CLI and by tests.
func (r *Router) Candidates(req ModelRequest) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(r.providers))
	for _, p := range r.providers {
		info := p.Info()
		if !eligible(req, info) {
			continue
		}
		out = append(out, info)
	}
	slices.SortStableFunc(out, func(a, b ProviderInfo) int {
		if c := cmp.Compare(routeScore(req, a), routeScore(req, b)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func eligible(req ModelRequest, info ProviderInfo) bool {
	class := req.EffectiveDataClass()
	// SENSITIVE data never leaves the process, regardless of program policy.
	if class == DataSensitive && !info.IsLocal() {
		return false
	}
	if !egressAllowed(req, info, class) {
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

// egressAllowed applies the program-level model egress ceiling. The zero value
// and UNKNOWN are LOCAL_ONLY. REMOTE_REDACTED_ONLY permits remote providers
// only when the caller attests redaction and the effective data class is not
// SENSITIVE; local providers are always allowed.
func egressAllowed(req ModelRequest, info ProviderInfo, class DataClass) bool {
	switch req.ModelEgress.Effective() {
	case domain.ModelEgressLocalOnly:
		return info.IsLocal()
	case domain.ModelEgressRemoteRedactedOnly:
		if info.IsLocal() {
			return true
		}
		return req.Redacted && class != DataSensitive
	case domain.ModelEgressRemoteAllowed:
		return true
	default:
		// Invalid values fail closed to LOCAL_ONLY.
		return info.IsLocal()
	}
}

// routeScore is lower-is-better and deterministic. The tiers are:
//
//	no escalation: local small -> stronger local -> remote strong
//	escalation:    remote strong -> stronger local -> smaller local
//
// Only an explicit EscalationReason may move a remote provider ahead of local
// ones, and the egress policy has already removed forbidden candidates.
func routeScore(req ModelRequest, info ProviderInfo) int {
	const tier = 1_000_000
	local := info.IsLocal()
	remoteRank := tier - info.Strength // strongest remote first
	if req.Escalation != EscalationNone && req.Escalation.Valid() {
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

// Route selects a provider, enforces data class, output tokens and budget,
// records the call, and falls back to the next provider on failure. Model
// output is returned as text; it never becomes an ActionDecision or platform
// action.
//
// Every transition is durable: RESERVED is written before the fallback chain,
// STARTED immediately before each provider invocation, and COMPLETED /
// AMBIGUOUS / FAILED_BEFORE_SEND after it. A retry with the same CallID may only
// proceed when the previous attempt is FAILED_BEFORE_SEND (the provider proved
// nothing was sent). RESERVED, STARTED and AMBIGUOUS are refused as ambiguous,
// because the provider may already have been paid.
func (r *Router) Route(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	if err := req.Validate(); err != nil {
		return ModelResponse{}, err
	}
	if r.requireStableCallID && strings.TrimSpace(req.CallID) == "" {
		return ModelResponse{}, fmt.Errorf("%w: set ModelRequest.CallID = StableCallID(task, step, purpose, promptHash)",
			ErrStableCallIDRequired)
	}
	candidates := r.Candidates(req)
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
				status := CallFailedBeforeSend
				if sawAmbiguous {
					status = CallAmbiguous
				}
				return ModelResponse{}, joinFinish(berr, finish(terminal(status, "CURRENCY_MISMATCH")))
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
		// that was paid for but recorded as merely RESERVED.
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
				rec := terminal(status, "AUDIT_FAILED")
				return ModelResponse{}, joinFinish(
					errors.Join(err, fmt.Errorf("model: audit failed call: %w", rerr)), finish(rec))
			}
			if ferr := finish(terminal(status, "PROVIDER_ERROR")); ferr != nil {
				return ModelResponse{}, errors.Join(err,
					fmt.Errorf("model: persist call status: %w", ferr))
			}
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
				rec := terminal(CallCompleted, "AUDIT_FAILED")
				return ModelResponse{}, joinFinish(
					errors.Join(berr, fmt.Errorf("model: audit call: %w", rerr)), finish(rec))
			}
			rec := terminal(CallCompleted, "BUDGET_EXCEEDED")
			if ferr := finish(rec); ferr != nil {
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
			rec := terminal(CallCompleted, "AUDIT_FAILED")
			return resp, joinFinish(fmt.Errorf("model: audit call: %w", rerr), finish(rec))
		}
		rec := terminal(CallCompleted, "")
		// The provider call completed. If this write fails the content is still
		// returned, but the caller is told the call receipt is incomplete; the
		// error is never silently discarded.
		if ferr := finish(rec); ferr != nil {
			return resp, fmt.Errorf("model: complete call: %w", ferr)
		}
		return resp, nil
	}
	if budgeted > 0 {
		berr := fmt.Errorf("%w: every eligible provider exceeds the remaining request budget", ErrBudgetExceeded)
		// No request bytes left the process for the blocked candidates. If an
		// earlier provider was ambiguous, the whole call stays ambiguous.
		status := CallFailedBeforeSend
		if sawAmbiguous {
			status = CallAmbiguous
		}
		return ModelResponse{}, joinFinish(berr, finish(terminal(status, "BUDGET_EXCEEDED")))
	}
	if lastErr != nil {
		status := CallFailedBeforeSend
		if sawAmbiguous {
			status = CallAmbiguous
		}
		return ModelResponse{}, joinFinish(fmt.Errorf("model: all providers failed: %w", lastErr),
			finish(terminal(status, "PROVIDER_ERROR")))
	}
	rec := terminal(CallFailedBeforeSend, "NO_PROVIDER")
	return ModelResponse{}, joinFinish(ErrNoProvider, finish(rec))
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
	for _, p := range r.providers {
		if p.Info().Name == name {
			return p
		}
	}
	return nil
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
		TaskID: string(req.TaskID), Purpose: string(req.Purpose), DataClass: string(req.EffectiveDataClass()),
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
