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
}
