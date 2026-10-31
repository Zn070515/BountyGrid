package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"time"
)

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

// Equal reports whether two observations are the same fact. Values are
// compared by their JSON encoding so a value that round-tripped through JSONB
// (e.g. int -> float64) still compares equal. Timestamps are compared at
// PostgreSQL microsecond precision.
func (f ObservedFact) Equal(o ObservedFact) bool {
	if f.ID != o.ID || f.Subject != o.Subject || f.Platform != o.Platform ||
		f.Predicate != o.Predicate || f.Unit != o.Unit ||
		!f.ObservedAt.Truncate(time.Microsecond).Equal(o.ObservedAt.Truncate(time.Microsecond)) ||
		f.Confidence != o.Confidence || f.ExtractorVersion != o.ExtractorVersion {
		return false
	}
	if !reflect.DeepEqual(f.AccountID, o.AccountID) ||
		!reflect.DeepEqual(f.ProgramID, o.ProgramID) ||
		!reflect.DeepEqual(f.AssetID, o.AssetID) {
		return false
	}
	fv, err := json.Marshal(f.Value)
	if err != nil {
		return false
	}
	ov, err := json.Marshal(o.Value)
	if err != nil || !bytes.Equal(fv, ov) {
		return false
	}
	fe, err := json.Marshal(f.EvidenceRefs)
	if err != nil {
		return false
	}
	oe, err := json.Marshal(o.EvidenceRefs)
	return err == nil && bytes.Equal(fe, oe)
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
