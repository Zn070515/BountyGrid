package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrReceiptNotFound means no execution has been recorded for the key.
	ErrReceiptNotFound = errors.New("execution receipt not found")
	// ErrReceiptExists means another execution already reserved the key.
	ErrReceiptExists = errors.New("execution receipt already exists")
)

// ExecutionEnvironmentMaxAge bounds how old a runtime observation may be before
// policy treats it as unknown. Constraints such as VPN/RPS cannot be satisfied
// by a measurement captured hours earlier.
const ExecutionEnvironmentMaxAge = 5 * time.Minute

// ExecutionEnvironment is the observed runtime state used by Policy together
// with Program.Constraints. A nil/zero field means "not observed"; policy must
// fail closed (REQUIRE_MORE_EVIDENCE) rather than assume the constraint is met.
type ExecutionEnvironment struct {
	ObservedAt time.Time

	CurrentRPS         *float64
	CurrentConcurrency *int

	// VPNConnected is YES only when the VPN tunnel was actively verified.
	VPNConnected TriState
	// EgressIP is the public source IP observed for the execution path.
	EgressIP string
	// ResearcherHeaders reports whether the platform-required researcher
	// header was attached. nil means it was not checked.
	ResearcherHeaders *bool
	// TestAccountID identifies the dedicated test account used for the action.
	TestAccountID string
	// TestAccountReportingReady reports whether the test-account usage is ready
	// to be reported in the platform-required format.
	TestAccountReportingReady *bool
}

// Validate rejects impossible measurements. Unknown/zero values are allowed
// because the policy layer treats them as absent, not as satisfied.
func (e ExecutionEnvironment) Validate() error {
	if e.CurrentRPS != nil && *e.CurrentRPS < 0 {
		return fmt.Errorf("execution environment: current_rps is negative")
	}
	if e.CurrentConcurrency != nil && *e.CurrentConcurrency < 0 {
		return fmt.Errorf("execution environment: current_concurrency is negative")
	}
	if e.VPNConnected != "" && !e.VPNConnected.Valid() {
		return fmt.Errorf("execution environment: invalid vpn state %q", e.VPNConnected)
	}
	return nil
}

// ExecutionReceipt makes an execution idempotent and auditable. The
// idempotency key is unique; a retried request reads the receipt instead of
// executing the tool a second time.
type ExecutionReceipt struct {
	ID             string
	IdempotencyKey string
	DecisionID     DecisionID
	ToolName       string
	Action         Action
	AccountID      AccountID
	ProgramID      ProgramID
	AssetID        *AssetID
	ResourceID     string
	// ParametersHash is the canonical hash of the request parameters. A replay
	// with a different request under the same key must be rejected.
	ParametersHash string
	// Actor is retained so a replayed execution can write the same audit
	// attribution without the original request object.
	Actor     ActorRef
	Status    ReceiptStatus
	Output    string
	Evidence  []EvidenceID
	Error     string
	StartedAt time.Time
	UpdatedAt time.Time
}

type ReceiptStatus string

const (
	// ReceiptStarted reserves the idempotency key before the tool runs. A
	// receipt left in this state means the process crashed mid-execution; a
	// retry must not execute the tool again.
	ReceiptStarted ReceiptStatus = "STARTED"
	// ReceiptExecuted means the tool ran but the audit/receipt commit is not
	// confirmed. A retry completes the bookkeeping without re-executing.
	ReceiptExecuted ReceiptStatus = "EXECUTED"
	// ReceiptCompleted means the tool ran and the audit event was written.
	ReceiptCompleted ReceiptStatus = "COMPLETED"
	// ReceiptFailed means the tool returned an error before producing a
	// side-effect receipt; the request may be retried.
	ReceiptFailed ReceiptStatus = "FAILED"
)

func (s ReceiptStatus) Valid() bool {
	switch s {
	case ReceiptStarted, ReceiptExecuted, ReceiptCompleted, ReceiptFailed:
		return true
	default:
		return false
	}
}

func (r ExecutionReceipt) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("receipt: id is empty")
	}
	if r.IdempotencyKey == "" {
		return fmt.Errorf("receipt %s: idempotency key is empty", r.ID)
	}
	if !r.Status.Valid() {
		return fmt.Errorf("receipt %s: invalid status %q", r.ID, r.Status)
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("receipt %s: started_at is zero", r.ID)
	}
	if r.Actor.Kind != "" && !r.Actor.Kind.Valid() {
		return fmt.Errorf("receipt %s: invalid actor kind %q", r.ID, r.Actor.Kind)
	}
	return nil
}
