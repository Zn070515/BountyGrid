// Package gateway is the Action Gateway: it loads policy inputs from
// persistence, evaluates them, records the decision, and re-checks a decision
// immediately before execution. It never executes a platform action.
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
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type Store interface {
	GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error)
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error)
	PutDecision(ctx context.Context, d domain.ActionDecision) error
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

type Gateway struct {
	store   Store
	engine  *policy.Engine
	auditor Auditor
	now     func() time.Time
}

func New(store Store, engine *policy.Engine, auditor Auditor, ledger policy.ApprovalLedger) *Gateway {
	if engine == nil {
		engine = policy.NewEngine()
	}
	if ledger != nil {
		engine.SetApprovalLedger(ledger)
	}
	return &Gateway{store: store, engine: engine, auditor: auditor, now: time.Now}
}

func (g *Gateway) SetClock(now func() time.Time) {
	if now != nil {
		g.now = now
	}
}

type EvaluateInput struct {
	Request       domain.ActionRequest
	Asset         *domain.Asset
	ResourceID    string
	HumanApproval *domain.HumanApproval
	UsageToday    map[string]int64
	BudgetSpent   *domain.Money
	Now           time.Time
}

// Evaluate loads the account/program, evaluates policy, persists the
// decision, and appends an audit event. A missing account/program produces a
// persisted DENY; it never returns an executable decision.
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
		if errors.Is(err, postgres.ErrNotFound) {
			return g.record(ctx, g.denyDecision(in.Request, "CONTEXT_NOT_FOUND", now), in.Request, now)
		}
		return domain.ActionDecision{}, fmt.Errorf("gateway: load account: %w", err)
	}
	prog, err := g.store.GetProgram(ctx, in.Request.ProgramID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return g.record(ctx, g.denyDecision(in.Request, "CONTEXT_NOT_FOUND", now), in.Request, now)
		}
		return domain.ActionDecision{}, fmt.Errorf("gateway: load program: %w", err)
	}
	decision, err := g.engine.Decide(ctx, policy.DecisionContext{
		Request:       in.Request,
		Account:       acct,
		Program:       prog,
		Asset:         in.Asset,
		UsageToday:    in.UsageToday,
		BudgetSpent:   in.BudgetSpent,
		ResourceID:    in.ResourceID,
		Now:           now,
		HumanApproval: in.HumanApproval,
	})
	if err != nil {
		return domain.ActionDecision{}, err
	}
	return g.record(ctx, decision, in.Request, now)
}

// Authorize is the pre-execution check. Only an unexpired ALLOW with a fresh
// running program state may proceed. It performs no platform action.
func (g *Gateway) Authorize(ctx context.Context, id domain.DecisionID) error {
	now := g.now().UTC()
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
	if d.Action.NeedsScope() || d.Action == domain.ActionReportSubmit {
		p, err := g.store.GetProgram(ctx, d.ProgramID)
		if err != nil {
			return fmt.Errorf("gateway: load program for decision %s: %w", id, err)
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
	}
}

func (g *Gateway) record(ctx context.Context, d domain.ActionDecision, req domain.ActionRequest, now time.Time) (domain.ActionDecision, error) {
	if err := g.store.PutDecision(ctx, d); err != nil {
		return domain.ActionDecision{}, fmt.Errorf("gateway: persist decision: %w", err)
	}
	if g.auditor != nil {
		// The event EntityID already carries the decision ID; do not duplicate
		// it in the payload (long numeric IDs can look like secrets to the
		// scanner).
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
			return domain.ActionDecision{}, fmt.Errorf("gateway: marshal audit: %w", err)
		}
		if _, err := g.auditor.Append(ctx, audit.Event{
			Type: "action.decision", EntityType: "decision", EntityID: string(d.ID),
			ActorKind: string(req.Actor.Kind), ActorID: req.Actor.ID,
			Payload: payload, RecordedAt: now,
		}); err != nil {
			return domain.ActionDecision{}, fmt.Errorf("gateway: audit decision: %w", err)
		}
	}
	return d, nil
}
