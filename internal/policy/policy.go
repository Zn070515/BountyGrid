package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

const DefaultPolicyVersion = "policy-v1"

// MinExecutionConfidence is the floor from architecture/evidence-facts.md:
// a fact below 0.8 cannot by itself drive an execution decision.
const MinExecutionConfidence domain.Confidence = 0.8

type ApprovalLedger interface {
	Consume(ctx context.Context, approvalID string) error
}

type MemoryApprovalLedger struct {
	mu       sync.Mutex
	consumed map[string]struct{}
}

func NewMemoryApprovalLedger() *MemoryApprovalLedger {
	return &MemoryApprovalLedger{consumed: make(map[string]struct{})}
}

func (l *MemoryApprovalLedger) Consume(ctx context.Context, approvalID string) error {
	if approvalID == "" {
		return fmt.Errorf("approval id is empty")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.consumed[approvalID]; ok {
		return domain.ErrApprovalConsumed
	}
	l.consumed[approvalID] = struct{}{}
	return nil
}

type DecisionContext struct {
	Request       domain.ActionRequest
	Account       domain.Account
	Program       domain.Program
	Asset         *domain.Asset
	Competition   *domain.CompetitionSnapshot
	UsageToday    map[string]int64 // per-interface daily usage
	BudgetSpent   *domain.Money
	ResourceID    string
	Now           time.Time
	PolicyVersion string
	HumanApproval *domain.HumanApproval
}

type Engine struct {
	matrix map[domain.Action]domain.Decision
	ledger ApprovalLedger
}

func NewEngine() *Engine {
	return &Engine{matrix: DefaultMatrix(), ledger: NewMemoryApprovalLedger()}
}

func (e *Engine) SetApprovalLedger(l ApprovalLedger) { e.ledger = l }

// DefaultMatrix is deliberately conservative. Project automation policy is
// applied before this matrix and may explicitly allow a conditional action;
// FORBIDDEN automation can never be overridden by an approval.
func DefaultMatrix() map[domain.Action]domain.Decision {
	return map[domain.Action]domain.Decision{
		domain.ActionViewProgram:        domain.DecisionAllow,
		domain.ActionViewRules:          domain.DecisionAllow,
		domain.ActionPassiveAnalysis:    domain.DecisionAllow,
		domain.ActionReportDraft:        domain.DecisionAllow,
		domain.ActionReadScope:          domain.DecisionRequireHuman, // handled separately
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

	// 0a. The decision must be about exactly the account/program in the
	// request. Without this binding a caller could evaluate one Program and
	// attribute the ALLOW to another request.
	if dc.Account.ID != dc.Request.AccountID || dc.Program.ID != dc.Request.ProgramID {
		return decide(domain.DecisionDeny, "CONTEXT_MISMATCH"), nil
	}
	if dc.Account.Platform != dc.Program.Platform {
		return decide(domain.DecisionDeny, "PLATFORM_MISMATCH"), nil
	}

	// 0b. Freshness and confidence are required for execution/submission, not
	// for viewing.
	if requiresFreshState(dc.Request.Action) {
		if dc.Program.Status.ValidUntil.IsZero() || !dc.Program.Status.ValidUntil.After(now) {
			return decide(domain.DecisionRequireMoreEvidence, "STALE_PROGRAM_STATE"), nil
		}
		if dc.Program.Status.Confidence < MinExecutionConfidence {
			return decide(domain.DecisionRequireMoreEvidence, "LOW_CONFIDENCE_STATE"), nil
		}
	}

	// 1. Account-level capability.
	if dc.Request.Action == domain.ActionReportSubmit && !dc.Account.Capability.CanSubmit {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_SUBMIT"), nil
	}
	if dc.Request.Action == domain.ActionReadScope && !dc.Account.Capability.CanViewScope && dc.Program.Scope.Access == domain.ScopeAccessGranted {
		return decide(domain.DecisionRequireHuman, "ACCOUNT_CANNOT_VIEW_SCOPE"), nil
	}

	// 2. Program state for execution/submission only.
	if (dc.Request.Action.NeedsScope() || dc.Request.Action == domain.ActionReportSubmit) &&
		dc.Program.Status.Status != domain.StatusRunning {
		return decide(domain.DecisionDeny, "PROGRAM_NOT_RUNNING"), nil
	}

	// 3. Eligibility only for actions that require participation.
	if dc.Request.Action.EligibilityRequired() {
		if !dc.Program.Eligibility.Decision.Valid() {
			return decide(domain.DecisionRequireMoreEvidence, "ELIGIBILITY_UNKNOWN"), nil
		}
		switch dc.Program.Eligibility.Decision {
		case domain.Ineligible:
			return decide(domain.DecisionDeny, "PROGRAM_INELIGIBLE"), nil
		case domain.HumanActionNeeded:
			return decide(domain.DecisionRequireHuman, "PROGRAM_GATE_HUMAN_ACTION"), nil
		case domain.EligibilityUnknown:
			return decide(domain.DecisionRequireMoreEvidence, "PROGRAM_GATE_UNKNOWN"), nil
		}
	}

	// 4. READ_SCOPE is how a human unlocks a gated scope; it is not execution.
	if dc.Request.Action == domain.ActionReadScope {
		if !dc.Program.Scope.Visibility.Valid() || dc.Program.Scope.Visibility == domain.ScopeUnknown ||
			!dc.Program.Scope.Access.Valid() || dc.Program.Scope.Access == domain.ScopeAccessUnknown {
			return decide(domain.DecisionRequireMoreEvidence, "SCOPE_UNKNOWN"), nil
		}
		if dc.Program.Scope.Access == domain.ScopeAccessGranted && dc.Program.Scope.ContentsKnown {
			return e.allow(dc, now, decide)
		}
		return decide(domain.DecisionRequireHuman, "SCOPE_GATED"), nil
	}

	// 5. Execution actions require granted, known scope.
	if dc.Request.Action.NeedsScope() {
		if dc.Program.Scope.Access != domain.ScopeAccessGranted || !dc.Program.Scope.ContentsKnown {
			if !dc.Program.Scope.Access.Valid() || dc.Program.Scope.Access == domain.ScopeAccessUnknown ||
				!dc.Program.Scope.Visibility.Valid() || dc.Program.Scope.Visibility == domain.ScopeUnknown {
				return decide(domain.DecisionRequireMoreEvidence, "SCOPE_UNKNOWN"), nil
			}
			return decide(domain.DecisionDeny, "SCOPE_GATED"), nil
		}
	}

	// 6. Asset must be explicitly in scope, and it must be the asset named by
	// the request. Checking a different in-scope asset would authorize the
	// wrong target.
	if dc.Request.Action.NeedsAsset() {
		if dc.Request.AssetID == nil || dc.Asset == nil {
			return decide(domain.DecisionDeny, "ASSET_REQUIRED"), nil
		}
		if *dc.Request.AssetID != dc.Asset.ID {
			return decide(domain.DecisionDeny, "ASSET_MISMATCH"), nil
		}
		if dc.Asset.ProgramID != dc.Request.ProgramID {
			return decide(domain.DecisionDeny, "ASSET_PROGRAM_MISMATCH"), nil
		}
		if dc.Asset.Platform != "" && dc.Asset.Platform != dc.Program.Platform {
			return decide(domain.DecisionDeny, "ASSET_PLATFORM_MISMATCH"), nil
		}
		switch dc.Program.Scope.CheckAsset(*dc.Asset) {
		case domain.ScopeMatchIncluded:
		case domain.ScopeMatchConditional:
			return decide(domain.DecisionRequireHuman, "ASSET_CONDITIONAL"), nil
		case domain.ScopeMatchExcluded:
			return decide(domain.DecisionDeny, "ASSET_OUT_OF_SCOPE"), nil
		default:
			return decide(domain.DecisionRequireMoreEvidence, "ASSET_SCOPE_UNKNOWN"), nil
		}
	}

	// 7. Rate/budget constraints, when usage is supplied by the caller.
	if err := checkUsage(dc); err != nil {
		return decide(domain.DecisionDeny, err.Error()), nil
	}

	// 8. Automation policy. FORBIDDEN cannot be overridden by an approval.
	base, ok := e.matrix[dc.Request.Action]
	if !ok {
		return decide(domain.DecisionDeny, "NO_ACTION_POLICY"), nil
	}
	if cap, has := automationCapability(dc.Program.Automation, dc.Request.Action); has {
		switch cap {
		case domain.CapForbidden:
			return decide(domain.DecisionDeny, "AUTOMATION_FORBIDDEN"), nil
		case domain.CapUnknown:
			return decide(domain.DecisionRequireMoreEvidence, "AUTOMATION_UNKNOWN"), nil
		case domain.CapConditional:
			// CONDITIONAL can add a human gate to an otherwise allowed or
			// human-gated action, but it must never loosen a hard DENY.
			if base != domain.DecisionDeny {
				base = domain.DecisionRequireHuman
			}
		case domain.CapAllowed:
			if overridableByAutomation(dc.Request.Action) {
				base = domain.DecisionAllow
			}
		}
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
		if err := validateApproval(dc.HumanApproval, dc.Request, dc.ResourceID, now); err != nil {
			return decide(domain.DecisionRequireHuman, "INVALID_APPROVAL"), nil
		}
		if dc.HumanApproval.SingleUse {
			if e.ledger == nil {
				return decide(domain.DecisionRequireHuman, "NO_APPROVAL_LEDGER"), nil
			}
			if err := e.ledger.Consume(ctx, dc.HumanApproval.ID); err != nil {
				if errors.Is(err, domain.ErrApprovalConsumed) {
					return decide(domain.DecisionRequireHuman, "APPROVAL_ALREADY_CONSUMED"), nil
				}
				return domain.ActionDecision{}, fmt.Errorf("policy: consume approval: %w", err)
			}
		}
	}
	return e.allow(dc, now, decide)
}

func (e *Engine) allow(dc DecisionContext, now time.Time, decide func(domain.Decision, ...string) domain.ActionDecision) (domain.ActionDecision, error) {
	// An ALLOW must not outlive the state or approval it was derived from.
	expires := now.Add(5 * time.Minute)
	// Only cap by a still-fresh state; view actions may deliberately proceed
	// with stale state and must not get an already-expired ALLOW.
	if until := dc.Program.Status.ValidUntil; until.After(now) && until.Before(expires) {
		expires = until
	}
	if a := dc.HumanApproval; a != nil && !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(expires) {
		expires = a.ExpiresAt
	}
	out := decide(domain.DecisionAllow)
	out.ExpiresAt = &expires
	out.Constraints = &dc.Program.Constraints
	return out, nil
}

func validateApproval(a *domain.HumanApproval, req domain.ActionRequest, resourceID string, now time.Time) error {
	if a == nil {
		return fmt.Errorf("approval is nil")
	}
	if a.ID == "" || a.ApprovedBy == "" || a.ApprovedAt.IsZero() {
		return fmt.Errorf("approval missing id/approver/time")
	}
	if a.RequestID == "" || a.RequestID != req.ID {
		return fmt.Errorf("approval request mismatch")
	}
	if a.Action != req.Action {
		return fmt.Errorf("approval action mismatch")
	}
	if a.ProgramID == "" || a.ProgramID != req.ProgramID {
		return fmt.Errorf("approval program mismatch")
	}
	if (a.AssetID == nil) != (req.AssetID == nil) ||
		(a.AssetID != nil && req.AssetID != nil && *a.AssetID != *req.AssetID) {
		return fmt.Errorf("approval asset mismatch")
	}
	if req.Action == domain.ActionReportSubmit {
		if a.ResourceID == "" || resourceID == "" || a.ResourceID != resourceID {
			return fmt.Errorf("approval report resource mismatch")
		}
	}
	if a.ParametersHash != parametersHash(req.Parameters) {
		return fmt.Errorf("approval parameters mismatch")
	}
	if a.ApprovedAt.After(now) {
		return fmt.Errorf("approval is from the future")
	}
	if a.ExpiresAt.IsZero() || !a.ExpiresAt.After(now) {
		return fmt.Errorf("approval expired")
	}
	if req.Action.HumanOnly() && !a.SingleUse {
		return fmt.Errorf("human-only approval must be single-use")
	}
	return nil
}

func parametersHash(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

func requiresFreshState(a domain.Action) bool {
	switch a {
	case domain.ActionViewProgram, domain.ActionViewRules:
		return false
	default:
		return true
	}
}

func automationCapability(p domain.AutomationPolicy, a domain.Action) (domain.CapabilityStatus, bool) {
	switch a {
	case domain.ActionPassiveAnalysis:
		return p.AIAnalysis, true
	case domain.ActionActiveHTTP:
		return p.ActiveHTTP, true
	case domain.ActionDirectoryEnum:
		return p.DirectoryEnumeration, true
	case domain.ActionSubdomainDiscovery:
		return p.SubdomainDiscovery, true
	case domain.ActionAutomatedScan:
		return p.AutomatedScanning, true
	case domain.ActionHighConcurrency:
		return p.HighConcurrency, true
	case domain.ActionBruteForce:
		return p.BruteForce, true
	case domain.ActionCredentialStuffing:
		return p.CredentialStuffing, true
	case domain.ActionFileUpload:
		return p.FileUpload, true
	case domain.ActionStateMutation:
		return p.StateMutation, true
	case domain.ActionRCEVerify:
		return p.RCEVerification, true
	case domain.ActionSensitiveDataRead:
		return p.SensitiveDataRead, true
	case domain.ActionInternalNetwork:
		return p.InternalNetwork, true
	case domain.ActionLateralMovement:
		return p.LateralMovement, true
	case domain.ActionDoS:
		return p.DoS, true
	default:
		return "", false
	}
}

func checkUsage(dc DecisionContext) error {
	for _, limit := range dc.Program.Constraints.PerInterfaceDailyLimits {
		if limit.Interface == "" || limit.Amount <= 0 {
			return fmt.Errorf("INVALID_DAILY_LIMIT")
		}
		if used := dc.UsageToday[limit.Interface]; used >= limit.Amount {
			return fmt.Errorf("DAILY_LIMIT_EXCEEDED:%s", limit.Interface)
		}
	}
	if cap := dc.Program.Constraints.BudgetCap; cap != nil {
		if cap.Amount <= 0 || cap.Currency == "" {
			return fmt.Errorf("INVALID_BUDGET_CAP")
		}
		if dc.BudgetSpent != nil {
			if cap.Currency != dc.BudgetSpent.Currency {
				return fmt.Errorf("BUDGET_CURRENCY_MISMATCH")
			}
			if dc.BudgetSpent.Amount >= cap.Amount {
				return fmt.Errorf("BUDGET_EXCEEDED")
			}
		}
	}
	return nil
}

func overridableByAutomation(a domain.Action) bool {
	switch a {
	case domain.ActionActiveHTTP, domain.ActionDirectoryEnum, domain.ActionSubdomainDiscovery,
		domain.ActionAutomatedScan, domain.ActionHighConcurrency, domain.ActionBruteForce,
		domain.ActionFileUpload, domain.ActionStateMutation, domain.ActionRCEVerify,
		domain.ActionSensitiveDataRead:
		return true
	default:
		return false
	}
}
