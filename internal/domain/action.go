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
	ID             string
	Actor          ActorRef
	AccountID      AccountID
	ProgramID      ProgramID
	AssetID        *AssetID
	Action         Action
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
}
