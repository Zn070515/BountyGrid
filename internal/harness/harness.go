// Package harness is the execution boundary. A tool declares the Action it
// requires; the harness asks the Action Gateway for a decision and re-checks it
// immediately before running the tool. The harness has no policy authority.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
)

type ToolSpec struct {
	Name           string
	RequiredAction domain.Action
	NetworkEgress  bool
	ReadsFiles     bool
	WritesFiles    bool
	Destructive    bool
	Description    string
}

// RequiresReceipt reports whether the tool can produce an external or local
// side effect. Such tools must carry an idempotency key so a retry replays the
// recorded execution instead of running twice.
func (s ToolSpec) RequiresReceipt() bool {
	return s.Destructive || s.WritesFiles || s.NetworkEgress
}

type ToolResult struct {
	Output   string
	Evidence []domain.EvidenceID
}

// ToolContext is the only capability surface a tool receives. Egress is non-nil
// only for tools that declare NetworkEgress; every request through it is
// re-checked against the sandbox, including redirects.
type ToolContext struct {
	Request  domain.ActionRequest
	Decision domain.ActionDecision
	Egress   *Egress
	Budget   *Budget
}

type Tool interface {
	Spec() ToolSpec
	Cost() domain.Money
	Execute(ctx context.Context, tc ToolContext) (ToolResult, error)
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("harness: tool is nil")
	}
	spec := t.Spec()
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("harness: tool name is empty")
	}
	if !spec.RequiredAction.Valid() {
		return fmt.Errorf("harness: tool %s has invalid action %q", spec.Name, spec.RequiredAction)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[spec.Name]; exists {
		return fmt.Errorf("harness: tool %s already registered", spec.Name)
	}
	r.tools[spec.Name] = t
	return nil
}

func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("harness: tool %s not found", name)
	}
	return t, nil
}

func (r *Registry) List() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	return out
}

type Sandbox struct {
	AllowedHosts []string
	Workspace    string
}

// CheckURL denies egress unless the host is explicitly allowed. Wildcards match
// subdomains only, never the apex.
func (s *Sandbox) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("sandbox: invalid url %q", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("sandbox: scheme %q is not allowed", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range s.AllowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed {
			return nil
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := allowed[1:]
			if len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
				return nil
			}
		}
	}
	return fmt.Errorf("sandbox: egress to %s denied", host)
}

// Egress is the harness-controlled network client. A tool never receives raw
// network capability: every request (and every redirect) is checked against the
// sandbox allowlist before the connection is made.
type Egress struct {
	sandbox *Sandbox
	client  *http.Client
}

func NewEgress(s *Sandbox) *Egress {
	if s == nil {
		s = &Sandbox{}
	}
	e := &Egress{sandbox: s}
	e.client = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("egress: too many redirects")
			}
			return e.CheckURL(req.URL.String())
		},
	}
	return e
}

func (e *Egress) CheckURL(raw string) error {
	return e.sandbox.CheckURL(raw)
}

// Do performs one request after the sandbox check.
func (e *Egress) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("egress: request has no url")
	}
	if err := e.CheckURL(req.URL.String()); err != nil {
		return nil, err
	}
	return e.client.Do(req.WithContext(ctx))
}

// Get is a convenience wrapper for read-only tools.
func (e *Egress) Get(ctx context.Context, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("egress: build request: %w", err)
	}
	return e.Do(ctx, req)
}

type ReceiptStore interface {
	GetReceipt(ctx context.Context, idempotencyKey string) (domain.ExecutionReceipt, error)
	CreateReceipt(ctx context.Context, r domain.ExecutionReceipt) error
	UpdateReceipt(ctx context.Context, r domain.ExecutionReceipt) error
}

// MemoryReceiptStore is the durable-store contract's in-memory twin for tests.
type MemoryReceiptStore struct {
	mu    sync.Mutex
	byKey map[string]domain.ExecutionReceipt
}

func NewMemoryReceiptStore() *MemoryReceiptStore {
	return &MemoryReceiptStore{byKey: make(map[string]domain.ExecutionReceipt)}
}

func (s *MemoryReceiptStore) GetReceipt(ctx context.Context, key string) (domain.ExecutionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byKey[key]
	if !ok {
		return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
	}
	return r, nil
}

func (s *MemoryReceiptStore) CreateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[r.IdempotencyKey]; ok {
		return domain.ErrReceiptExists
	}
	s.byKey[r.IdempotencyKey] = r
	return nil
}

func (s *MemoryReceiptStore) UpdateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[r.IdempotencyKey]; !ok {
		return domain.ErrReceiptNotFound
	}
	s.byKey[r.IdempotencyKey] = r
	return nil
}

type Budget struct {
	MaxCost     domain.Money
	Spent       domain.Money
	MaxRequests int
	Requests    int

	mu sync.Mutex
}

func (b *Budget) Check(cost domain.Money) error {
	if cost.Amount < 0 {
		return errors.New("budget: negative cost")
	}
	if cost.Currency == "" && cost.Amount > 0 {
		return errors.New("budget: cost currency is empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.MaxRequests > 0 && b.Requests >= b.MaxRequests {
		return errors.New("budget: request limit reached")
	}
	if b.MaxCost.Amount > 0 {
		if cost.Amount > 0 && cost.Currency != b.MaxCost.Currency {
			return errors.New("budget: currency mismatch")
		}
		if b.Spent.Amount+cost.Amount > b.MaxCost.Amount {
			return errors.New("budget: cost limit reached")
		}
	}
	return nil
}

func (b *Budget) Charge(cost domain.Money) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Spent.Amount += cost.Amount
	b.Spent.Currency = cost.Currency
	b.Requests++
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

type Gate interface {
	Evaluate(ctx context.Context, in gateway.EvaluateInput) (domain.ActionDecision, error)
	AuthorizeWith(ctx context.Context, id domain.DecisionID, ai gateway.AuthorizeInput) error
}

type DeniedError struct {
	Decision domain.ActionDecision
}

func (e DeniedError) Error() string {
	return fmt.Sprintf("harness: action %s is %s", e.Decision.Action, e.Decision.Decision)
}

type Harness struct {
	registry         *Registry
	gate             Gate
	auditor          Auditor
	sandbox          *Sandbox
	egress           *Egress
	budget           *Budget
	receipts         ReceiptStore
	allowDestructive bool
	now              func() time.Time
}

func New(reg *Registry, gate Gate, auditor Auditor, sandbox *Sandbox, budget *Budget) *Harness {
	if sandbox == nil {
		sandbox = &Sandbox{}
	}
	if budget == nil {
		budget = &Budget{}
	}
	return &Harness{
		registry: reg, gate: gate, auditor: auditor, sandbox: sandbox,
		egress: NewEgress(sandbox), budget: budget, receipts: NewMemoryReceiptStore(), now: time.Now,
	}
}

func (h *Harness) SetAllowDestructive(v bool) { h.allowDestructive = v }
func (h *Harness) SetReceiptStore(s ReceiptStore) {
	if s != nil {
		h.receipts = s
	}
}
func (h *Harness) SetClock(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// Execute authorizes and runs one tool. The tool's RequiredAction overrides the
// request action; a mismatched non-empty action is rejected. The tool is never
// called unless the gateway returns ALLOW and Authorize passes.
//
// Tools that can produce side effects require an IdempotencyKey. The key is
// reserved before execution; a retry replays the receipt instead of running the
// tool again, and an execution whose audit write failed can be completed
// without repeating the side effect.
func (h *Harness) Execute(ctx context.Context, toolName string, in gateway.EvaluateInput) (domain.ActionDecision, ToolResult, error) {
	tool, err := h.registry.Get(toolName)
	if err != nil {
		return domain.ActionDecision{}, ToolResult{}, err
	}
	spec := tool.Spec()
	if spec.Destructive && !h.allowDestructive {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: destructive tool %s is disabled", toolName)
	}
	if in.Request.Action != "" && in.Request.Action != spec.RequiredAction {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: tool %s requires %s, request asked for %s",
			toolName, spec.RequiredAction, in.Request.Action)
	}
	in.Request.Action = spec.RequiredAction

	if spec.NetworkEgress && (h.sandbox == nil || len(h.sandbox.AllowedHosts) == 0) {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
			"harness: network tool %s requires a non-empty egress allowlist", toolName)
	}

	key := strings.TrimSpace(in.Request.IdempotencyKey)
	needsReceipt := spec.RequiresReceipt()
	if needsReceipt && key == "" {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
			"harness: tool %s requires an idempotency key", toolName)
	}
	var reserved *domain.ExecutionReceipt
	if key != "" {
		if h.receipts == nil {
			return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: no receipt store configured")
		}
		rec, err := h.receipts.GetReceipt(ctx, key)
		switch {
		case err == nil:
			if err := checkReceiptBinding(rec, in, toolName); err != nil {
				return domain.ActionDecision{}, ToolResult{}, err
			}
			switch rec.Status {
			case domain.ReceiptCompleted, domain.ReceiptExecuted:
				return h.replay(ctx, rec, toolName)
			case domain.ReceiptStarted:
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
					"harness: execution for key %s is unsettled; refusing to repeat it", key)
			case domain.ReceiptFailed:
				// The tool reported failure before a side-effect receipt; a retry
				// under the same key is allowed. Re-arm it only after the gate
				// authorizes, so a denied retry cannot leave a STARTED receipt
				// that blocks future attempts.
				reserved = &rec
			}
		case !errors.Is(err, domain.ErrReceiptNotFound):
			return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: read receipt: %w", err)
		}
	}

	decision, err := h.gate.Evaluate(ctx, in)
	if err != nil {
		return domain.ActionDecision{}, ToolResult{}, err
	}
	if decision.Decision != domain.DecisionAllow {
		return decision, ToolResult{}, DeniedError{Decision: decision}
	}
	// Budget is checked before Authorize: a budget rejection must not consume a
	// single-use approval.
	cost := tool.Cost()
	if err := h.budget.Check(cost); err != nil {
		return decision, ToolResult{}, err
	}
	if err := h.gate.AuthorizeWith(ctx, decision.ID, gateway.AuthorizeInput{
		UsageToday: in.UsageToday, BudgetSpent: in.BudgetSpent,
		Environment: in.Environment, Now: in.Now,
	}); err != nil {
		return decision, ToolResult{}, err
	}

	// Reserve the key after authorization and budget checks, immediately
	// before the tool can act.
	if key != "" {
		now := h.now().UTC()
		if reserved == nil {
			rec := newReceipt(key, decision, in, toolName, now)
			if err := h.receipts.CreateReceipt(ctx, rec); err != nil {
				if !errors.Is(err, domain.ErrReceiptExists) {
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: reserve receipt: %w", err)
				}
				existing, gerr := h.receipts.GetReceipt(ctx, key)
				if gerr != nil {
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: read existing receipt: %w", gerr)
				}
				if berr := checkReceiptBinding(existing, in, toolName); berr != nil {
					return domain.ActionDecision{}, ToolResult{}, berr
				}
				switch existing.Status {
				case domain.ReceiptCompleted, domain.ReceiptExecuted:
					return h.replay(ctx, existing, toolName)
				case domain.ReceiptStarted:
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
						"harness: execution for key %s is unsettled; refusing to repeat it", key)
				case domain.ReceiptFailed:
					reserved = &existing
				default:
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
						"harness: receipt %s has unknown status %q", key, existing.Status)
				}
			} else {
				reserved = &rec
			}
		}
		// A FAILED receipt from an earlier attempt may be retried. Fill in the
		// binding fields as well, so receipts written before the binding existed
		// become fully checked after one retry.
		if reserved != nil && reserved.Status == domain.ReceiptFailed {
			*reserved = newReceipt(key, decision, in, toolName, now)
			reserved.StartedAt = now
			if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: re-arm receipt: %w", err)
			}
		}
	}

	tc := ToolContext{Request: in.Request, Decision: decision, Budget: h.budget}
	if spec.NetworkEgress {
		tc.Egress = h.egress
	}
	res, err := tool.Execute(ctx, tc)
	if err != nil {
		if reserved != nil {
			reserved.Status = domain.ReceiptFailed
			reserved.Error = err.Error()
			reserved.UpdatedAt = h.now().UTC()
			_ = h.receipts.UpdateReceipt(ctx, *reserved)
		}
		return decision, ToolResult{}, err
	}
	h.budget.Charge(cost)

	if reserved != nil {
		reserved.DecisionID = decision.ID
		reserved.Output = res.Output
		reserved.Evidence = res.Evidence
		reserved.Status = domain.ReceiptExecuted
		reserved.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
			return decision, res, fmt.Errorf("harness: record executed receipt: %w", err)
		}
	}
	if err := h.appendAudit(ctx, toolName, in.Request, decision); err != nil {
		// The tool already executed; the receipt stays EXECUTED so a retry can
		// finish the audit without executing again.
		return decision, res, err
	}
	if reserved != nil {
		reserved.Status = domain.ReceiptCompleted
		reserved.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
			return decision, res, fmt.Errorf("harness: complete receipt: %w", err)
		}
	}
	return decision, res, nil
}

func newReceipt(key string, decision domain.ActionDecision, in gateway.EvaluateInput, toolName string, now time.Time) domain.ExecutionReceipt {
	var asset *domain.AssetID
	if in.Request.AssetID != nil {
		v := *in.Request.AssetID
		asset = &v
	}
	return domain.ExecutionReceipt{
		ID: "rcpt/" + key, IdempotencyKey: key, DecisionID: decision.ID,
		ToolName: toolName, Action: in.Request.Action,
		AccountID: in.Request.AccountID, ProgramID: in.Request.ProgramID,
		AssetID: asset, ResourceID: in.ResourceID,
		ParametersHash: domain.CanonicalParametersHash(in.Request.Parameters),
		Actor:          in.Request.Actor,
		Status:         domain.ReceiptStarted, StartedAt: now, UpdatedAt: now,
	}
}

// checkReceiptBinding refuses a replay whose request identity differs from the
// one that produced the receipt. The idempotency key identifies one logical
// action; it must never return another program's/asset's result without policy.
func checkReceiptBinding(rec domain.ExecutionReceipt, in gateway.EvaluateInput, toolName string) error {
	if rec.ToolName != "" && rec.ToolName != toolName {
		return fmt.Errorf("harness: idempotency key %s is bound to tool %s, not %s",
			rec.IdempotencyKey, rec.ToolName, toolName)
	}
	if rec.Action != "" && rec.Action != in.Request.Action {
		return fmt.Errorf("harness: idempotency key %s is bound to action %s, not %s",
			rec.IdempotencyKey, rec.Action, in.Request.Action)
	}
	if rec.AccountID != "" && rec.AccountID != in.Request.AccountID {
		return fmt.Errorf("harness: idempotency key %s is bound to another account", rec.IdempotencyKey)
	}
	if rec.ProgramID != "" && rec.ProgramID != in.Request.ProgramID {
		return fmt.Errorf("harness: idempotency key %s is bound to another program", rec.IdempotencyKey)
	}
	if !sameAssetID(rec.AssetID, in.Request.AssetID) {
		return fmt.Errorf("harness: idempotency key %s is bound to another asset", rec.IdempotencyKey)
	}
	// New receipts always carry a ParametersHash; compare the resource exactly
	// so an empty original resource cannot be replayed for a report/finding.
	if rec.ParametersHash != "" && rec.ResourceID != in.ResourceID {
		return fmt.Errorf("harness: idempotency key %s is bound to another resource", rec.IdempotencyKey)
	}
	if rec.ParametersHash != "" && rec.ParametersHash != domain.CanonicalParametersHash(in.Request.Parameters) {
		return fmt.Errorf("harness: idempotency key %s is bound to different parameters", rec.IdempotencyKey)
	}
	return nil
}

func sameAssetID(a, b *domain.AssetID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// replay returns the recorded result without calling the tool. An EXECUTED
// receipt gets its pending audit event written first.
func (h *Harness) replay(ctx context.Context, rec domain.ExecutionReceipt, toolName string) (domain.ActionDecision, ToolResult, error) {
	decision := domain.ActionDecision{ID: rec.DecisionID, Decision: domain.DecisionAllow, Action: rec.Action, ProgramID: rec.ProgramID}
	if rec.Status == domain.ReceiptExecuted {
		if err := h.appendAudit(ctx, toolName, domain.ActionRequest{Actor: rec.Actor}, decision); err != nil {
			return decision, ToolResult{}, err
		}
		rec.Status = domain.ReceiptCompleted
		rec.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, rec); err != nil {
			return decision, ToolResult{}, fmt.Errorf("harness: complete replayed receipt: %w", err)
		}
	}
	return decision, ToolResult{Output: rec.Output, Evidence: rec.Evidence}, nil
}

func (h *Harness) appendAudit(ctx context.Context, toolName string, req domain.ActionRequest, decision domain.ActionDecision) error {
	if h.auditor == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"tool": toolName, "action": string(decision.Action), "decision": string(decision.Decision),
	})
	if err != nil {
		return err
	}
	if _, err := h.auditor.Append(ctx, audit.Event{
		Type: "tool.executed", EntityType: "decision", EntityID: string(decision.ID),
		ActorKind: string(req.Actor.Kind), ActorID: req.Actor.ID,
		Payload: payload, RecordedAt: h.now().UTC(),
	}); err != nil {
		return fmt.Errorf("harness: audit tool: %w", err)
	}
	return nil
}

// ReadOnlyTool is a local, non-network tool used for the Phase 5 boundary demo.
type ReadOnlyTool struct {
	ToolName    string
	Action      domain.Action
	Description string
}

func (t ReadOnlyTool) Spec() ToolSpec {
	return ToolSpec{Name: t.ToolName, RequiredAction: t.Action, Description: t.Description}
}
func (t ReadOnlyTool) Cost() domain.Money { return domain.Money{} }
func (t ReadOnlyTool) Execute(ctx context.Context, tc ToolContext) (ToolResult, error) {
	return ToolResult{Output: "read-only " + t.ToolName + " completed"}, nil
}

func NewBuiltinRegistry() *Registry {
	r := NewRegistry()
	_ = r.Register(ReadOnlyTool{ToolName: "view_rules", Action: domain.ActionViewRules, Description: "read only public rules"})
	_ = r.Register(ReadOnlyTool{ToolName: "read_scope", Action: domain.ActionReadScope, Description: "read an unlocked scope"})
	_ = r.Register(ReadOnlyTool{ToolName: "report_draft", Action: domain.ActionReportDraft, Description: "local report draft"})
	return r
}
