// Package harness is the execution boundary. A tool declares the Action it
// requires; the harness asks the Action Gateway for a decision and re-checks it
// immediately before running the tool. The harness has no policy authority.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type ToolResult struct {
	Output   string
	Evidence []domain.EvidenceID
}

type ToolContext struct {
	Request  domain.ActionRequest
	Decision domain.ActionDecision
	Sandbox  *Sandbox
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
	Authorize(ctx context.Context, id domain.DecisionID) error
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
	budget           *Budget
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
	return &Harness{registry: reg, gate: gate, auditor: auditor, sandbox: sandbox, budget: budget, now: time.Now}
}

func (h *Harness) SetAllowDestructive(v bool) { h.allowDestructive = v }
func (h *Harness) SetClock(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// Execute authorizes and runs one tool. The tool's RequiredAction overrides the
// request action; a mismatched non-empty action is rejected. The tool is never
// called unless the gateway returns ALLOW and Authorize passes.
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

	decision, err := h.gate.Evaluate(ctx, in)
	if err != nil {
		return domain.ActionDecision{}, ToolResult{}, err
	}
	if decision.Decision != domain.DecisionAllow {
		return decision, ToolResult{}, DeniedError{Decision: decision}
	}
	if err := h.gate.Authorize(ctx, decision.ID); err != nil {
		return decision, ToolResult{}, err
	}
	cost := tool.Cost()
	if err := h.budget.Check(cost); err != nil {
		return decision, ToolResult{}, err
	}
	res, err := tool.Execute(ctx, ToolContext{
		Request: in.Request, Decision: decision, Sandbox: h.sandbox, Budget: h.budget,
	})
	if err != nil {
		return decision, ToolResult{}, err
	}
	h.budget.Charge(cost)
	if h.auditor != nil {
		payload, err := json.Marshal(map[string]any{
			"tool": toolName, "action": string(in.Request.Action), "decision": string(decision.Decision),
		})
		if err != nil {
			return decision, ToolResult{}, err
		}
		if _, err := h.auditor.Append(ctx, audit.Event{
			Type: "tool.executed", EntityType: "decision", EntityID: string(decision.ID),
			ActorKind: string(in.Request.Actor.Kind), ActorID: in.Request.Actor.ID,
			Payload: payload, RecordedAt: h.now().UTC(),
		}); err != nil {
			return decision, ToolResult{}, fmt.Errorf("harness: audit tool: %w", err)
		}
	}
	return decision, res, nil
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
