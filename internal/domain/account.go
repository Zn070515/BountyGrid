package domain

import "time"

type EligibilityGate struct {
	Kind GateKind
	// ProgramID nil means account-wide; non-nil means applies only to that program.
	ProgramID *ProgramID
	Required  bool
	Satisfied GateSatisfaction
	Blocking  bool
	Evidence  []EvidenceID
	Notes     string
}

// EligibilityState is a decision plus the gates that produced it.
type EligibilityState struct {
	Decision      EligibilityDecision
	Gates         []EligibilityGate
	EvaluatedAt   time.Time
	PolicyVersion string
	Notes         string
}

func (s EligibilityState) Blocking() []EligibilityGate {
	var out []EligibilityGate
	for _, g := range s.Gates {
		if g.Required && g.Blocking && g.Satisfied == GateUnsatisfied {
			out = append(out, g)
		}
	}
	return out
}

func (s EligibilityState) HasUnknown() bool {
	for _, g := range s.Gates {
		if g.Required && g.Satisfied == GateUnknown {
			return true
		}
	}
	return false
}

// AccountCapability is the account-level answer, independent of any program.
type AccountCapability struct {
	CanViewPrograms bool
	CanViewScope    bool
	CanJoinPrograms bool
	CanSubmit       bool
	CanPayout       bool
	BlockingReasons []EligibilityGate
	EvaluatedAt     time.Time
	// ValidUntil is an optional explicit expiry for the capability snapshot.
	// Zero falls back to EvaluatedAt+AccountCapabilityMaxAge.
	ValidUntil time.Time
}

type Account struct {
	ID                      AccountID
	Platform                Platform
	Registered              bool
	RegistrationTime        *time.Time
	IdentityVerified        *bool
	Level                   string
	LevelExp                *int
	LevelExpNext            *int
	ReputationScore         *float64
	ReputationMetric        string
	Rank                    *string
	NewcomerStatus          string
	ComplianceExamCompleted *bool
	PracticalCertification  *bool
	BankBound               *bool
	WechatBound             *bool
	PendingSubmissionLimit  *int
	JoinedProgramIDs        []ProgramID
	Capability              AccountCapability
	Gates                   []EligibilityGate
	ObservedAt              time.Time
}
