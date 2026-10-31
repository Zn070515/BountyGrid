package domain

import "time"

// Evidence is an immutable capture of a platform page, announcement, API
// response, or screenshot. RawRef points to local/object storage; secrets must
// be redacted before persisting.
type Evidence struct {
	ID          EvidenceID
	Platform    Platform
	SourceType  SourceType
	Transport   TransportKind
	URL         string
	PageTitle   string
	CapturedAt  time.Time
	ContentHash string
	RawRef      string
	Redactions  []string
}

type FactSubject string

const (
	SubjectAccount FactSubject = "ACCOUNT"
	SubjectProgram FactSubject = "PROGRAM"
	SubjectAsset   FactSubject = "ASSET"
	SubjectPayout  FactSubject = "PAYOUT"
)

// ObservedFact states what was observed, never whether an action is allowed.
type ObservedFact struct {
	ID               FactID
	Subject          FactSubject
	Platform         Platform
	AccountID        *AccountID
	ProgramID        *ProgramID
	AssetID          *AssetID
	Predicate        string
	Value            any
	Unit             string
	ObservedAt       time.Time
	EvidenceRefs     []EvidenceID
	Confidence       Confidence
	ExtractorVersion string
}

type StateSubject string

const (
	StateProgramState    StateSubject = "PROGRAM_STATE"
	StateRegistration    StateSubject = "REGISTRATION_STATE"
	StateScopeVisibility StateSubject = "SCOPE_VISIBILITY"
	StateAccountAbility  StateSubject = "ACCOUNT_ABILITY"
	StateCompetition     StateSubject = "COMPETITION"
)

// DerivedState is a recomputable conclusion from facts.
type DerivedState struct {
	ID          StateID
	Subject     StateSubject
	Platform    Platform
	AccountID   *AccountID
	ProgramID   *ProgramID
	State       string
	Inputs      []FactID
	ObservedAt  time.Time
	DerivedAt   time.Time
	ValidUntil  time.Time
	Confidence  Confidence
	RuleVersion string
}
