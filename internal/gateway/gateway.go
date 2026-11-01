// Package gateway is the Action Gateway: it loads policy inputs from
// persistence, resolves the canonical asset, evaluates them, records the
// decision, and re-checks a decision against fresh state immediately before
// execution. It never executes a platform action.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy"
)

type Store interface {
	GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error)
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error)
	PutDecision(ctx context.Context, d domain.ActionDecision) error
}

// DecisionRecorder is implemented by stores that can persist the decision and
// its audit event atomically. The gateway prefers it so a decision can never
// exist without its audit record.
type DecisionRecorder interface {
	RecordDecisionWithAudit(ctx context.Context, d domain.ActionDecision, e audit.Event) error
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

// ApprovalLedger atomically consumes a single-use human approval.
type ApprovalLedger interface {
	Consume(ctx context.Context, approvalID string) error
}

type Gateway struct {
	store   Store
	engine  *policy.Engine
	auditor Auditor
	ledger  ApprovalLedger
	now     func() time.Time
}

func New(store Store, engine *policy.Engine, auditor Auditor, ledger ApprovalLedger) *Gateway {
	if engine == nil {
		engine = policy.NewEngine()
	}
	return &Gateway{store: store, engine: engine, auditor: auditor, ledger: ledger, now: time.Now}
}

func (g *Gateway) SetClock(now func() time.Time) {
	if now != nil {
		g.now = now
	}
}

type EvaluateInput struct {
	Request       domain.ActionRequest
	ResourceID    string
	HumanApproval *domain.HumanApproval
	UsageToday    map[string]int64
	BudgetSpent   *domain.Money
	Environment   *domain.ExecutionEnvironment
	Now           time.Time
}

// AuthorizeInput carries freshly observed runtime state for the pre-execution
// revalidation. Usage and budget are deliberately not defaulted from the
// decision: a stored counter can be stale, and a constraint with no fresh
// observation must fail closed. Environment may be omitted; the stored one is
// used only while it is still fresh.
type AuthorizeInput struct {
	UsageToday  map[string]int64
	BudgetSpent *domain.Money
	Environment *domain.ExecutionEnvironment
	Now         time.Time
}

// Evaluate loads the account/program, resolves the requested asset from the
// program's stored scope, evaluates policy, persists the decision, and appends
// an audit event. A missing account/program/asset produces a persisted DENY; it
// never returns an executable decision.
//
// The caller supplies only an AssetID. It cannot supply an Asset value, so a
// valid ID cannot be paired with a different target.
func (g *Gateway) Evaluate(ctx context.Context, in EvaluateInput) (domain.ActionDecision, error) {
	if err := in.Request.Validate(); err != nil {
		return domain.ActionDecision{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = g.now().UTC()
	}
	acct, err := g.store.GetAccount(ctx, in.Request.AccountID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return g.record(ctx, g.denyDecision(in.Request, "CONTEXT_NOT_FOUND", now), in.Request, now)
		}
		return domain.ActionDecision{}, fmt.Errorf("gateway: load account: %w", err)
	}
	prog, err := g.store.GetProgram(ctx, in.Request.ProgramID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return g.record(ctx, g.denyDecision(in.Request, "CONTEXT_NOT_FOUND", now), in.Request, now)
		}
		return domain.ActionDecision{}, fmt.Errorf("gateway: load program: %w", err)
	}
	asset := resolveAsset(prog, in.Request.AssetID)
	decision, err := g.engine.Decide(ctx, policy.DecisionContext{
		Request:       in.Request,
		Account:       acct,
		Program:       prog,
		Asset:         asset,
		UsageToday:    in.UsageToday,
		BudgetSpent:   in.BudgetSpent,
		ResourceID:    in.ResourceID,
		Now:           now,
		HumanApproval: in.HumanApproval,
		Environment:   in.Environment,
	})
	if err != nil {
		return domain.ActionDecision{}, err
	}
	return g.record(ctx, decision, in.Request, now)
}

// Authorize is the pre-execution check without fresh runtime observations. It
// is safe for decisions whose program has no runtime/usage constraints; a
// constrained program fails closed until the caller supplies AuthorizeWith.
func (g *Gateway) Authorize(ctx context.Context, id domain.DecisionID) error {
	return g.AuthorizeWith(ctx, id, AuthorizeInput{})
}

// AuthorizeWith is the pre-execution check. The decision must still be an
// unexpired ALLOW, and the policy is re-run against fresh Account/Program/Scope
// so a change after the decision cannot ride an old ALLOW. A single-use
// approval is consumed only here, atomically, after revalidation succeeds.
//
// Usage and budget are taken only from ai. A stored counter is evidence of what
// was true at decision time, not what is true now. Environment falls back to the
// stored observation, which Policy treats as unknown once it is too old.
func (g *Gateway) AuthorizeWith(ctx context.Context, id domain.DecisionID, ai AuthorizeInput) error {
	now := ai.Now
	if now.IsZero() {
		now = g.now().UTC()
	}
	d, err := g.store.GetDecision(ctx, id)
	if err != nil {
		return fmt.Errorf("gateway: load decision %s: %w", id, err)
	}
	if d.Decision != domain.DecisionAllow {
		return fmt.Errorf("gateway: decision %s is %s", id, d.Decision)
	}
	if d.ExpiresAt == nil || !d.ExpiresAt.After(now) {
		return fmt.Errorf("gateway: decision %s expired", id)
	}
	if d.Action.HumanOnly() && d.HumanApproval == nil {
		return fmt.Errorf("gateway: human-only decision %s has no approval", id)
	}

	if d.Request.ID == "" {
		// Decisions written before requests were persisted carry no inputs to
		// re-evaluate. Keep the narrow legacy check rather than trusting them
		// beyond expiry; new decisions always take the full path below.
		return g.authorizeLegacy(ctx, d, now)
	}

	acct, err := g.store.GetAccount(ctx, d.Request.AccountID)
	if err != nil {
		return fmt.Errorf("gateway: reload account for decision %s: %w", id, err)
	}
	prog, err := g.store.GetProgram(ctx, d.Request.ProgramID)
	if err != nil {
		return fmt.Errorf("gateway: reload program for decision %s: %w", id, err)
	}
	req := d.Request
	// Restore the caller's exact parameter bytes: JSONB normalization must not
	// change the approval's parameters_hash.
	if d.RequestParameters != "" {
		req.Parameters = json.RawMessage(d.RequestParameters)
	}
	env := ai.Environment
	if env == nil {
		env = d.Environment
	}
	asset := resolveAsset(prog, req.AssetID)
	fresh, err := g.engine.Decide(ctx, policy.DecisionContext{
		Request:       req,
		Account:       acct,
		Program:       prog,
		Asset:         asset,
		UsageToday:    ai.UsageToday,
		BudgetSpent:   ai.BudgetSpent,
		ResourceID:    d.ResourceID,
		Now:           now,
		HumanApproval: d.HumanApproval,
		Environment:   env,
	})
	if err != nil {
		return fmt.Errorf("gateway: re-evaluate decision %s: %w", id, err)
	}
	if fresh.Decision != domain.DecisionAllow {
		return fmt.Errorf("gateway: decision %s is no longer executable: %s %v", id, fresh.Decision, fresh.Reasons)
	}
	// The approval was bound to a specific rule set. If the active rules changed,
	// re-evaluate under the new rules rather than riding the old approval.
	if fresh.RuleSetFingerprint != d.RuleSetFingerprint {
		return fmt.Errorf("gateway: decision %s is bound to rule set %q, active rule set is %q",
			id, d.RuleSetFingerprint, fresh.RuleSetFingerprint)
	}
	if err := g.consumeApproval(ctx, d, now); err != nil {
		return err
	}
	return nil
}

func (g *Gateway) authorizeLegacy(ctx context.Context, d domain.ActionDecision, now time.Time) error {
	if d.Action.NeedsScope() || d.Action == domain.ActionReportSubmit {
		p, err := g.store.GetProgram(ctx, d.ProgramID)
		if err != nil {
			return fmt.Errorf("gateway: load program for decision %s: %w", d.ID, err)
		}
		if p.Status.Status != domain.StatusRunning {
			return fmt.Errorf("gateway: program %s is %s", p.ID, p.Status.Status)
		}
		if p.Status.ValidUntil.IsZero() || !p.Status.ValidUntil.After(now) {
			return fmt.Errorf("gateway: program %s state is stale", p.ID)
		}
	}
	return nil
}

func (g *Gateway) consumeApproval(ctx context.Context, d domain.ActionDecision, now time.Time) error {
	a := d.HumanApproval
	if a == nil || !a.SingleUse {
		return nil
	}
	if a.ExpiresAt.IsZero() || !a.ExpiresAt.After(now) {
		return fmt.Errorf("gateway: approval %s expired", a.ID)
	}
	if g.ledger == nil {
		return fmt.Errorf("gateway: decision %s needs an approval ledger", d.ID)
	}
	if err := g.ledger.Consume(ctx, a.ID); err != nil {
		if errors.Is(err, domain.ErrApprovalConsumed) {
			return fmt.Errorf("gateway: approval %s already consumed: %w", a.ID, err)
		}
		return fmt.Errorf("gateway: consume approval %s: %w", a.ID, err)
	}
	return nil
}

// resolveAsset returns the canonical asset from the program's stored scope. A
// nil result means the requested ID is not in the scope; the policy layer turns
// that into ASSET_UNKNOWN rather than trusting any caller-supplied value.
func resolveAsset(p domain.Program, id *domain.AssetID) *domain.Asset {
	if id == nil || *id == "" {
		return nil
	}
	a, _, ok := p.Scope.ResolveAsset(*id)
	if !ok {
		return nil
	}
	if a.ProgramID == "" {
		a.ProgramID = p.ID
	}
	if a.Platform == "" {
		a.Platform = p.Platform
	}
	return &a
}

func (g *Gateway) denyDecision(req domain.ActionRequest, reason string, now time.Time) domain.ActionDecision {
	return domain.ActionDecision{
		ID:            domain.DecisionID(fmt.Sprintf("dec-%s-%s", req.ID, now.UTC().Format("20060102T150405.000000000"))),
		RequestID:     req.ID,
		ProgramID:     req.ProgramID,
		Action:        req.Action,
		Decision:      domain.DecisionDeny,
		Reasons:       []string{reason},
		PolicyVersion: policy.DefaultPolicyVersion,
		DecidedAt:     now,
		Request:       req,
	}
}

func (g *Gateway) record(ctx context.Context, d domain.ActionDecision, req domain.ActionRequest, now time.Time) (domain.ActionDecision, error) {
	event, hasEvent, err := g.decisionEvent(d, req, now)
	if err != nil {
		return domain.ActionDecision{}, err
	}
	if rec, ok := g.store.(DecisionRecorder); ok && hasEvent {
		if err := rec.RecordDecisionWithAudit(ctx, d, event); err != nil {
			return domain.ActionDecision{}, fmt.Errorf("gateway: record decision with audit: %w", err)
		}
		return d, nil
	}
	if err := g.store.PutDecision(ctx, d); err != nil {
		return domain.ActionDecision{}, fmt.Errorf("gateway: persist decision: %w", err)
	}
	if hasEvent {
		if _, err := g.auditor.Append(ctx, event); err != nil {
			return domain.ActionDecision{}, fmt.Errorf("gateway: audit decision: %w", err)
		}
	}
	return d, nil
}

func (g *Gateway) decisionEvent(d domain.ActionDecision, req domain.ActionRequest, now time.Time) (audit.Event, bool, error) {
	if g.auditor == nil {
		return audit.Event{}, false, nil
	}
	// The event EntityID already carries the decision ID; do not duplicate it
	// in the payload (long numeric IDs can look like secrets to the scanner).
	payload, err := json.Marshal(struct {
		RequestID string     `json:"request_id"`
		ProgramID string     `json:"program_id"`
		Action    string     `json:"action"`
		Decision  string     `json:"decision"`
		Reasons   []string   `json:"reasons"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}{
		RequestID: d.RequestID,
		ProgramID: string(d.ProgramID), Action: string(d.Action),
		Decision: string(d.Decision), Reasons: d.Reasons, ExpiresAt: d.ExpiresAt,
	})
	if err != nil {
		return audit.Event{}, false, fmt.Errorf("gateway: marshal audit: %w", err)
	}
	return audit.Event{
		Type: "action.decision", EntityType: "decision", EntityID: string(d.ID),
		ActorKind: string(req.Actor.Kind), ActorID: req.Actor.ID,
		Payload: payload, RecordedAt: now,
	}, true, nil
}
