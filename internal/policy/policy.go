package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

const DefaultPolicyVersion = "policy-v1"

// MinExecutionConfidence is the floor from architecture/evidence-facts.md:
// a fact below 0.8 cannot by itself drive an execution decision.
const MinExecutionConfidence domain.Confidence = 0.8

type DecisionContext struct {
	Request domain.ActionRequest
	Account domain.Account
	Program domain.Program
	// Asset must be the canonical asset resolved from Program.Scope by the
	// Action Gateway. Policy never accepts an asset value from an untrusted
	// caller.
	Asset         *domain.Asset
	Competition   *domain.CompetitionSnapshot
	UsageToday    map[string]int64 // per-interface daily usage
	BudgetSpent   *domain.Money
	ResourceID    string
	Now           time.Time
	PolicyVersion string
	HumanApproval *domain.HumanApproval
	Environment   *domain.ExecutionEnvironment
}

type Engine struct {
	matrix map[domain.Action]domain.Decision
}

func NewEngine() *Engine {
	return &Engine{matrix: DefaultMatrix()}
}

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
	// Merge the program's gates with the account's observed state. A program
	// gate records a requirement; whether this account satisfies it must be
	// evaluated now, not frozen into the Program row.
	elig, err := effectiveEligibility(dc, version, now)
	if err != nil {
		return domain.ActionDecision{}, err
	}
	decide := func(d domain.Decision, reasons ...string) domain.ActionDecision {
		return domain.ActionDecision{
			ID:                domain.DecisionID(decisionID(dc.Request.ID, now)),
			RequestID:         dc.Request.ID,
			ProgramID:         dc.Request.ProgramID,
			Action:            dc.Request.Action,
			Decision:          d,
			Reasons:           reasons,
			GatesEvaluated:    elig.Gates,
			PolicyVersion:     version,
			DecidedAt:         now,
			HumanApproval:     dc.HumanApproval,
			Request:           dc.Request,
			ResourceID:        dc.ResourceID,
			UsageToday:        dc.UsageToday,
			BudgetSpent:       dc.BudgetSpent,
			Environment:       dc.Environment,
			RequestParameters: string(dc.Request.Parameters),
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

	// 1. Account-level capability. A program-scoped gate is evaluated with the
	// account's observed state in effectiveEligibility below; these checks cover
	// the account-wide switches.
	if dc.Request.Action == domain.ActionReportSubmit && !dc.Account.Capability.CanSubmit {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_SUBMIT"), nil
	}
	if dc.Request.Action == domain.ActionProgramSignup && !dc.Account.Capability.CanJoinPrograms {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_JOIN_PROGRAMS"), nil
	}
	if dc.Request.Action == domain.ActionPayout && !dc.Account.Capability.CanPayout {
		return decide(domain.DecisionDeny, "ACCOUNT_CANNOT_PAYOUT"), nil
	}
	if dc.Request.Action == domain.ActionReadScope && !dc.Account.Capability.CanViewScope && dc.Program.Scope.Access == domain.ScopeAccessGranted {
		return decide(domain.DecisionRequireHuman, "ACCOUNT_CANNOT_VIEW_SCOPE"), nil
	}

	// 2. Program state for execution/submission only. Signup must not be
	// approved for a program that has ended or is paused.
	if (dc.Request.Action.NeedsScope() || dc.Request.Action == domain.ActionReportSubmit ||
		dc.Request.Action == domain.ActionProgramSignup) &&
		dc.Program.Status.Status != domain.StatusRunning {
		return decide(domain.DecisionDeny, "PROGRAM_NOT_RUNNING"), nil
	}

	// 3. Eligibility only for actions that require participation.
	if dc.Request.Action.EligibilityRequired() {
		if !elig.Decision.Valid() {
			return decide(domain.DecisionRequireMoreEvidence, "ELIGIBILITY_UNKNOWN"), nil
		}
		switch elig.Decision {
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
		if dc.Request.AssetID == nil {
			return decide(domain.DecisionDeny, "ASSET_REQUIRED"), nil
		}
		if dc.Asset == nil {
			// The requested ID is not present in the program's stored scope.
			// Never trust a caller-supplied asset to fill this in.
			return decide(domain.DecisionDeny, "ASSET_UNKNOWN"), nil
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

	// 7. Rate/budget constraints. Missing usage/budget is unknown, not zero.
	if d, reason := checkUsage(dc); d != "" {
		return decide(d, reason), nil
	}

	// 7b. Runtime constraints. A constraint that the caller cannot prove is
	// satisfied produces REQUIRE_MORE_EVIDENCE, never ALLOW.
	if d, reason := checkExecutionEnvironment(dc, now); d != "" {
		return decide(d, reason), nil
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
		// Validate but do not consume: consumption happens atomically in
		// Gateway.Authorize, immediately before execution. Consuming here would
		// burn a single-use approval even if the decision failed to persist or
		// the user never executed it.
		if err := validateApproval(dc.HumanApproval, dc.Request, dc.ResourceID, now); err != nil {
			return decide(domain.DecisionRequireHuman, "INVALID_APPROVAL"), nil
		}
	}
	return e.allow(dc, now, decide)
}

// decisionID avoids long digit runs: the secret scanner treats 16-19 digit
// numbers as bank cards, which would reject audit payloads containing the ID.
func decisionID(requestID string, now time.Time) string {
	return fmt.Sprintf("dec-%s-%s", requestID, now.UTC().Format("20060102T150405.000000000"))
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
	// Accept both the raw and canonical hashes: approvals created before the
	// canonical hash existed keep working, while a request that round-tripped
	// through JSONB can still match.
	if a.ParametersHash != parametersHash(req.Parameters) &&
		a.ParametersHash != domain.CanonicalParametersHash(req.Parameters) {
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

// checkUsage validates per-interface daily limits and the budget cap. A
// constraint with no observation is unknown, not "zero used": returning ALLOW
// because UsageToday/BudgetSpent were omitted would fail open.
func checkUsage(dc DecisionContext) (domain.Decision, string) {
	limits := dc.Program.Constraints.PerInterfaceDailyLimits
	for _, limit := range limits {
		if limit.Interface == "" || limit.Amount <= 0 {
			return domain.DecisionDeny, "INVALID_DAILY_LIMIT"
		}
	}
	if len(limits) > 0 && dc.UsageToday == nil {
		return domain.DecisionRequireMoreEvidence, "USAGE_UNKNOWN"
	}
	for _, limit := range limits {
		used, observed := dc.UsageToday[limit.Interface]
		if !observed {
			return domain.DecisionRequireMoreEvidence, "USAGE_UNKNOWN:" + limit.Interface
		}
		if used >= limit.Amount {
			return domain.DecisionDeny, "DAILY_LIMIT_EXCEEDED:" + limit.Interface
		}
	}
	if cap := dc.Program.Constraints.BudgetCap; cap != nil {
		if cap.Amount <= 0 || cap.Currency == "" {
			return domain.DecisionDeny, "INVALID_BUDGET_CAP"
		}
		if dc.BudgetSpent == nil {
			return domain.DecisionRequireMoreEvidence, "BUDGET_UNKNOWN"
		}
		if cap.Currency != dc.BudgetSpent.Currency {
			return domain.DecisionDeny, "BUDGET_CURRENCY_MISMATCH"
		}
		if dc.BudgetSpent.Amount >= cap.Amount {
			return domain.DecisionDeny, "BUDGET_EXCEEDED"
		}
	}
	return "", ""
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
