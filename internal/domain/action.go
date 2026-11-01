package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrApprovalConsumed is returned when a single-use approval has already been
// spent. Other ledger errors are infrastructure failures and must surface.
var ErrApprovalConsumed = errors.New("approval already consumed")

type ActorRef struct {
	Kind ActorKind
	ID   string
}

type ActionRequest struct {
	ID        string
	Actor     ActorRef
	AccountID AccountID
	ProgramID ProgramID
	AssetID   *AssetID
	Action    Action
	// Method is the HTTP method for network actions. Policy checks it against
	// Constraints.AllowedMethods/ForbiddenMethods; an empty method fails closed
	// when the program restricts methods.
	Method         string
	Parameters     json.RawMessage
	Purpose        string
	RequestedAt    time.Time
	Evidence       []EvidenceID
	IdempotencyKey string
}

type HumanApproval struct {
	ID             string
	ApprovedBy     string
	ApprovedAt     time.Time
	RequestID      string
	Action         Action
	ProgramID      ProgramID
	AssetID        *AssetID
	ResourceID     string // report/finding/task ID for resource-scoped actions
	ParametersHash string
	Scope          string
	ExpiresAt      time.Time
	SingleUse      bool
	Comment        string
}

type ActionDecision struct {
	ID             DecisionID
	RequestID      string
	ProgramID      ProgramID
	Action         Action
	Decision       Decision
	Reasons        []string
	GatesEvaluated []EligibilityGate
	PolicyVersion  string
	DecidedAt      time.Time
	HumanApproval  *HumanApproval
	ExpiresAt      *time.Time
	Constraints    *Constraints

	// Request and the runtime inputs are persisted with the decision so
	// Authorize can reload authoritative state and re-run the policy instead of
	// trusting a decision that was made before the scope/policy changed.
	Request     ActionRequest
	ResourceID  string
	UsageToday  map[string]int64
	BudgetSpent *Money
	Environment *ExecutionEnvironment
	// AssetTarget is the canonical asset value resolved from Program.Scope by
	// the Action Gateway. Harness egress binds to it so a tool cannot retarget a
	// request at another host while keeping the approved AssetID.
	AssetTarget string
	// ScopeRevision is a digest of the scope snapshot the decision was evaluated
	// against. A later scope edit changes the revision and invalidates the
	// approval path.
	ScopeRevision string
	// PolicyRevision is a digest of the automation/constraints snapshot.
	PolicyRevision string
	// RuleIDs lists the declarative policy rules that matched this request. It is
	// audit provenance, not an authority: the effective decision is still the
	// strictest merged decision after all domain gates.
	RuleIDs []string
	// RuleSetFingerprint identifies the exact rule set used for the decision. A
	// later Authorize with a different rule set must not ride this approval.
	RuleSetFingerprint string
	// RequestParameters preserves the exact parameter bytes supplied by the
	// caller. PostgreSQL JSONB normalizes JSON whitespace/key order, so the
	// nested Request.Parameters cannot be used to re-validate an approval hash.
	RequestParameters string
}
