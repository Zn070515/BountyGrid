package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

const DefaultPolicyVersion = "policy-v1"

type DecisionContext struct {
	Request       domain.ActionRequest
	Account       domain.Account
	Program       domain.Program
	Now           time.Time
	PolicyVersion string
	HumanApproval *domain.HumanApproval
}

type Engine struct {
	matrix map[domain.Action]domain.Decision
}

func NewEngine() *Engine {
	return &Engine{matrix: DefaultMatrix()}
}

// DefaultMatrix is deliberately conservative. Conditional platform rules are
// represented as REQUIRE_HUMAN until a program-specific override is loaded.
func DefaultMatrix() map[domain.Action]domain.Decision {
	return map[domain.Action]domain.Decision{
		domain.ActionViewProgram:        domain.DecisionAllow,
		domain.ActionViewRules:          domain.DecisionAllow,
		domain.ActionPassiveAnalysis:    domain.DecisionAllow,
		domain.ActionReportDraft:        domain.DecisionAllow,
		domain.ActionReadScope:          domain.DecisionRequireHuman,
		domain.ActionActiveHTTP:         domain.DecisionRequireHuman,
		domain.ActionSubdomainDiscovery: domain.DecisionRequireHuman,
		domain.ActionBruteForce:         domain.DecisionRequireHuman,
		domain.ActionFileUpload:         domain.DecisionRequireHuman,
		domain.ActionRCEVerify:          domain.DecisionRequireHuman,
		domain.ActionDirectoryEnum:      domain.DecisionDeny,
		domain.ActionAutomatedScan:      domain.DecisionDeny,
		domain.ActionHighConcurrency:    domain.DecisionDeny,
		domain.ActionCredentialStuffing: domain.DecisionDeny,
		domain.ActionStateMutation:      domain.DecisionDeny,
		domain.ActionSensitiveDataRead:  domain.DecisionDeny,
		domain.ActionInternalNetwork:    domain.DecisionDeny,
		domain.ActionLateralMovement:    domain.DecisionDeny,
		domain.ActionDoS:                domain.DecisionDeny,
		domain.ActionReportSubmit:       domain.DecisionRequireHuman,
		domain.ActionProgramSignup:      domain.DecisionRequireHuman,
		domain.ActionNDAAccept:          domain.DecisionRequireHuman,
		domain.ActionComplianceExam:     domain.DecisionRequireHuman,
		domain.ActionPracticalCert:      domain.DecisionRequireHuman,
		domain.ActionPayout:             domain.DecisionRequireHuman,
		domain.ActionBankBinding:        domain.DecisionRequireHuman,
	}
}

func (e *Engine) Decide(ctx context.Context, dc DecisionContext) (domain.ActionDecision, error) {
	if err := dc.Request.Validate(); err != nil {
		return domain.ActionDecision{}, err
	}
	now := dc.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	version := dc.PolicyVersion
	if version == "" {
		version = DefaultPolicyVersion
	}
	decide := func(d domain.Decision, reasons ...string) domain.ActionDecision {
		return domain.ActionDecision{
			ID:             domain.DecisionID(fmt.Sprintf("dec-%s-%d", dc.Request.ID, now.UnixNano())),
			RequestID:      dc.Request.ID,
			ProgramID:      dc.Request.ProgramID,
			Action:         dc.Request.Action,
			Decision:       d,
			Reasons:        reasons,
			GatesEvaluated: dc.Program.Eligibility.Gates,
			PolicyVersion:  version,
			DecidedAt:      now,
			HumanApproval:  dc.HumanApproval,
		}
	}

	// 0. Evidence freshness.
	if dc.Program.Status.ValidUntil.IsZero() || dc.Program.Status.ValidUntil.Before(now) {
		return decide(domain.DecisionRequireMoreEvidence, "STALE_PROGRAM_STATE"), nil
	}

	// 1. Account-level capability.
	if dc.Request.Action == domain.ActionReportSubmit && !dc.Account.Capability.CanSubmit {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_SUBMIT"), nil
	}
	if dc.Request.Action == domain.ActionReadScope && !dc.Account.Capability.CanViewScope {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_VIEW_SCOPE"), nil
	}

	// 2. Program state.
	execution := dc.Request.Action.NeedsScope() || dc.Request.Action == domain.ActionReportSubmit
	if execution && dc.Program.Status.Status != domain.StatusRunning {
		return decide(domain.DecisionDeny, "PROGRAM_NOT_RUNNING"), nil
	}

	// 3. Eligibility gates.
	switch dc.Program.Eligibility.Decision {
	case domain.Ineligible:
		return decide(domain.DecisionDeny, "PROGRAM_INELIGIBLE"), nil
	case domain.HumanActionNeeded:
		return decide(domain.DecisionRequireHuman, "PROGRAM_GATE_HUMAN_ACTION"), nil
	case domain.EligibilityUnknown:
		return decide(domain.DecisionRequireMoreEvidence, "PROGRAM_GATE_UNKNOWN"), nil
	}

	// 4. Scope visibility.
	if dc.Request.Action.NeedsScope() && dc.Program.Scope.Visibility != domain.ScopePublic {
		return decide(domain.DecisionDeny, "SCOPE_GATED"), nil
	}

	// 5. Action matrix.
	base, ok := e.matrix[dc.Request.Action]
	if !ok {
		return decide(domain.DecisionDeny, "NO_ACTION_POLICY"), nil
	}
	if base == domain.DecisionDeny {
		return decide(domain.DecisionDeny, "ACTION_DENIED"), nil
	}
	if dc.Request.Action.HumanOnly() && dc.HumanApproval == nil {
		return decide(domain.DecisionRequireHuman, "HUMAN_ONLY_ACTION"), nil
	}
	if base == domain.DecisionRequireHuman && dc.HumanApproval == nil {
		return decide(domain.DecisionRequireHuman, "APPROVAL_REQUIRED"), nil
	}
	if dc.HumanApproval != nil {
		if err := validateApproval(dc.HumanApproval, dc.Request.Action, dc.Request.ProgramID, now); err != nil {
			return decide(domain.DecisionRequireHuman, "INVALID_APPROVAL"), nil
		}
	}

	expires := now.Add(5 * time.Minute)
	out := decide(domain.DecisionAllow)
	out.ExpiresAt = &expires
	out.Constraints = &dc.Program.Constraints
	return out, nil
}

func validateApproval(a *domain.HumanApproval, action domain.Action, program domain.ProgramID, now time.Time) error {
	if a == nil {
		return fmt.Errorf("approval is nil")
	}
	if a.Action != action {
		return fmt.Errorf("approval action mismatch")
	}
	if a.ProgramID != "" && program != "" && a.ProgramID != program {
		return fmt.Errorf("approval program mismatch")
	}
	if a.ApprovedBy == "" || a.ApprovedAt.IsZero() {
		return fmt.Errorf("approval missing approver/time")
	}
	if a.ExpiresAt.IsZero() || !a.ExpiresAt.After(now) {
		return fmt.Errorf("approval expired")
	}
	return nil
}
