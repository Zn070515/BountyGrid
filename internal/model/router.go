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

// CallRecord is one model invocation. It stores hashes, never prompt or
// response content.
type CallRecord struct {
	ID           string
	TaskID       domain.TaskID
	Purpose      Purpose
	DataClass    DataClass
	Provider     string
	Model        string
	PromptHash   string
	ResponseHash string
	Usage        Usage
	Cost         domain.Money
	StartedAt    time.Time
	EndedAt      time.Time
	Error        string
}

type CallLog interface {
	Add(CallRecord)
	List() []CallRecord
}

type MemoryCallLog struct {
	mu      sync.Mutex
	records []CallRecord
}

func NewMemoryCallLog() *MemoryCallLog { return &MemoryCallLog{} }

func (l *MemoryCallLog) Add(r CallRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
}

func (l *MemoryCallLog) List() []CallRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]CallRecord, len(l.records))
	copy(out, l.records)
	return out
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

// CallStore durably reserves a logical call before a provider is invoked and
// records its outcome. A retry with the same CallID sees the reservation and
// must not pay for a second model invocation.
type CallStore interface {
	Reserve(ctx context.Context, id string, rec CallRecord) (bool, error)
	Complete(ctx context.Context, id string, rec CallRecord) error
	Get(ctx context.Context, id string) (CallRecord, bool, error)
}

type MemoryCallStore struct {
	mu      sync.Mutex
	records map[string]CallRecord
}

func NewMemoryCallStore() *MemoryCallStore {
	return &MemoryCallStore{records: make(map[string]CallRecord)}
}

func (s *MemoryCallStore) Reserve(ctx context.Context, id string, rec CallRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; ok {
		return false, nil
	}
	s.records[id] = rec
	return true, nil
}

func (s *MemoryCallStore) Complete(ctx context.Context, id string, rec CallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = rec
	return nil
}

func (s *MemoryCallStore) Get(ctx context.Context, id string) (CallRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	return r, ok, nil
}

type Options struct {
	Providers []Provider
	Auditor   Auditor
	CallLog   CallLog
	CallStore CallStore
	Now       func() time.Time
}

type Router struct {
	providers []Provider
	auditor   Auditor
	log       CallLog
	calls     CallStore
	now       func() time.Time
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
	return &Router{providers: opts.Providers, auditor: opts.Auditor, log: opts.CallLog, calls: opts.CallStore, now: opts.Now}, nil
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
	if class == DataSensitive && !info.IsLocal() {
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

// routeScore is lower-is-better. Planning/analysis prefers strong remote
// models; code/embedding/report work prefers local models. Sensitive data has
// already excluded remote providers.
func routeScore(req ModelRequest, info ProviderInfo) int {
	switch req.Purpose {
	case PurposePlan, PurposeAnalyze:
		if info.IsLocal() {
			return 10
		}
		return 10 - info.Strength
	case PurposeCode, PurposeEmbed:
		if info.IsLocal() {
			return 0
		}
		return 10
	case PurposeReport:
		if info.IsLocal() {
			return 0
		}
		return 5
	default:
		return 5
	}
}

// Route selects a provider, enforces data class, output tokens and budget,
// records the call, and falls back to the next provider on failure. Model
// output is returned as text; it never becomes an ActionDecision or platform
// action.
func (r *Router) Route(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	if err := req.Validate(); err != nil {
		return ModelResponse{}, err
	}
	candidates := r.Candidates(req)
	if len(candidates) == 0 {
		return ModelResponse{}, fmt.Errorf("%w: data_class=%s purpose=%s max_tokens=%d",
			ErrNoProvider, req.DataClass, req.Purpose, req.MaxTokens)
	}

	// Reserve the logical call before any provider is invoked. A retry with the
	// same CallID is rejected instead of paying for a second call.
	callID := strings.TrimSpace(req.CallID)
	if callID == "" {
		callID = callIDFor(req, r.now())
	}
	if r.calls != nil {
		reserved, err := r.calls.Reserve(ctx, callID, CallRecord{
			ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), PromptHash: PromptHash(req),
		})
		if err != nil {
			return ModelResponse{}, fmt.Errorf("model: reserve call: %w", err)
		}
		if !reserved {
			return ModelResponse{}, fmt.Errorf("%w: %s", ErrDuplicateCall, callID)
		}
	}

	budgeted := 0
	var spent int64
	var lastErr error
	finish := func(rec CallRecord) {
		if r.calls == nil {
			return
		}
		_ = r.calls.Complete(context.WithoutCancel(ctx), callID, rec)
	}
	for _, info := range candidates {
		provider := r.provider(info.Name)
		if provider == nil {
			continue
		}
		estimate := EstimateCost(info, req)
		if req.Budget.Amount > 0 {
			if !strings.EqualFold(req.Budget.Currency, estimate.Currency) {
				return ModelResponse{}, fmt.Errorf("%w: budget currency %q does not match pricing %q",
					ErrInvalidRequest, req.Budget.Currency, estimate.Currency)
			}
			// The budget bounds the whole fallback chain, not one candidate.
			if spent+estimate.Amount > req.Budget.Amount {
				budgeted++
				continue
			}
		}

		started := r.now()
		resp, err := provider.Complete(ctx, req)
		ended := r.now()
		if err != nil {
			// A failed provider may still have billed for the attempt. Count the
			// estimate against the chain budget so fallback cannot overspend.
			spent += EstimateCost(info, req).Amount
			lastErr = err
			if rerr := r.record(ctx, req, info, started, ended, EstimateCost(info, req), resp, "PROVIDER_ERROR"); rerr != nil {
				finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
					DataClass: req.EffectiveDataClass(), Provider: info.Name, Model: info.Model, Error: "AUDIT_FAILED"})
				return ModelResponse{}, errors.Join(err, fmt.Errorf("model: audit failed call: %w", rerr))
			}
			continue
		}
		cost := ActualCost(info, resp.Usage)
		spent += cost.Amount
		if req.Budget.Amount > 0 && spent > req.Budget.Amount {
			berr := fmt.Errorf("%w: accumulated cost %d exceeds budget %d",
				ErrBudgetExceeded, spent, req.Budget.Amount)
			if rerr := r.record(ctx, req, info, started, ended, cost, resp, "BUDGET_EXCEEDED"); rerr != nil {
				return ModelResponse{}, errors.Join(berr, fmt.Errorf("model: audit call: %w", rerr))
			}
			finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
				DataClass: req.EffectiveDataClass(), Provider: info.Name, Model: info.Model,
				Cost: cost, Error: "BUDGET_EXCEEDED", StartedAt: started, EndedAt: ended,
				PromptHash: PromptHash(req), ResponseHash: ResponseHash(resp.Content), Usage: resp.Usage})
			return ModelResponse{}, berr
		}
		resp.Provider = info.Name
		if resp.Model == "" {
			resp.Model = info.Model
		}
		resp.Cost = cost
		resp.Latency = ended.Sub(started)
		if rerr := r.record(ctx, req, info, started, ended, cost, resp, ""); rerr != nil {
			// The call happened and its content is still useful, but the audit
			// trail is incomplete. Return both rather than pretending success.
			finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
				DataClass: req.EffectiveDataClass(), Provider: info.Name, Model: info.Model,
				Cost: cost, Error: "AUDIT_FAILED", StartedAt: started, EndedAt: ended,
				PromptHash: PromptHash(req), ResponseHash: ResponseHash(resp.Content), Usage: resp.Usage})
			return resp, fmt.Errorf("model: audit call: %w", rerr)
		}
		finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), Provider: info.Name, Model: info.Model,
			Cost: cost, StartedAt: started, EndedAt: ended,
			PromptHash: PromptHash(req), ResponseHash: ResponseHash(resp.Content), Usage: resp.Usage})
		return resp, nil
	}
	if budgeted > 0 {
		finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), Error: "BUDGET_EXCEEDED"})
		return ModelResponse{}, fmt.Errorf("%w: every eligible provider exceeds the remaining request budget", ErrBudgetExceeded)
	}
	if lastErr != nil {
		finish(CallRecord{ID: callID, TaskID: req.TaskID, Purpose: req.Purpose,
			DataClass: req.EffectiveDataClass(), Error: "PROVIDER_ERROR"})
		return ModelResponse{}, fmt.Errorf("model: all providers failed: %w", lastErr)
	}
	return ModelResponse{}, ErrNoProvider
}

func (r *Router) provider(name string) Provider {
	for _, p := range r.providers {
		if p.Info().Name == name {
			return p
		}
	}
	return nil
}

func (r *Router) record(ctx context.Context, req ModelRequest, info ProviderInfo, started, ended time.Time, cost domain.Money, resp ModelResponse, errKind string) error {
	rec := CallRecord{
		ID:           callIDFor(req, started),
		TaskID:       req.TaskID,
		Purpose:      req.Purpose,
		DataClass:    req.DataClass,
		Provider:     info.Name,
		Model:        info.Model,
		PromptHash:   PromptHash(req),
		ResponseHash: ResponseHash(resp.Content),
		Usage:        resp.Usage,
		Cost:         cost,
		StartedAt:    started,
		EndedAt:      ended,
		Error:        errKind,
	}
	r.log.Add(rec)
	if r.auditor == nil {
		return nil
	}
	payload, err := json.Marshal(struct {
		TaskID       string       `json:"task_id,omitempty"`
		Purpose      string       `json:"purpose"`
		DataClass    string       `json:"data_class"`
		Provider     string       `json:"provider"`
		Model        string       `json:"model"`
		PromptHash   string       `json:"prompt_hash"`
		ResponseHash string       `json:"response_hash"`
		Usage        Usage        `json:"usage"`
		Cost         domain.Money `json:"cost"`
		DurationMS   int64        `json:"duration_ms"`
		Error        string       `json:"error,omitempty"`
	}{
		TaskID: string(req.TaskID), Purpose: string(req.Purpose), DataClass: string(req.DataClass),
		Provider: info.Name, Model: info.Model, PromptHash: rec.PromptHash,
		ResponseHash: rec.ResponseHash, Usage: rec.Usage, Cost: rec.Cost,
		DurationMS: ended.Sub(started).Milliseconds(), Error: errKind,
	})
	if err != nil {
		return err
	}
	if _, err := r.auditor.Append(ctx, audit.Event{
		Type: "model.call", EntityType: "task", EntityID: string(req.TaskID),
		ActorKind: string(domain.ActorModel), ActorID: info.Name,
		Payload: payload, RecordedAt: ended,
	}); err != nil {
		return fmt.Errorf("model: append audit: %w", err)
	}
	return nil
}

var callSeq atomic.Uint64

func callIDFor(req ModelRequest, at time.Time) string {
	task := string(req.TaskID)
	if task == "" {
		task = "adhoc"
	}
	// The sequence keeps two auto-generated calls in the same clock tick from
	// colliding. Explicit ModelRequest.CallID is still the idempotency key.
	return fmt.Sprintf("call/%s/%s/%d", task, at.UTC().Format("20060102T150405.000000000"), callSeq.Add(1))
}
