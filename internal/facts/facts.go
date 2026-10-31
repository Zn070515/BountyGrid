package facts

import (
	"context"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// Canonical predicates used by state derivation. Platform-native predicates
// may coexist; state rules only read these canonical keys.
const (
	PredProgramStatus     = "program.status"
	PredProgramPaused     = "program.paused"
	PredProgramStart      = "program.start"
	PredProgramEnd        = "program.end"
	PredRegistrationStart = "program.registration_start"
	PredRegistrationEnd   = "program.registration_end"
	PredActivityStart     = "program.activity_start"
	PredActivityEnd       = "program.activity_end"
	PredPhaseStart        = "program.phase_start"
	PredPhaseEnd          = "program.phase_end"
	PredScopeVisibility   = "program.scope_visibility"
	PredComplianceExam    = "account.compliance_exam_completed"
	PredPracticalCert     = "account.practical_certification"
	PredIdentityVerified  = "account.identity_verified"
	PredBankBound         = "account.bank_bound"
	PredPendingLimit      = "account.pending_submission_limit"
)

// Input is a captured page/text that an extractor may parse. Raw HTML may be
// added later; extractors should never invent values that are not present.
type Input struct {
	Platform   domain.Platform
	ProgramID  *domain.ProgramID
	URL        string
	PageTitle  string
	Text       string
	CapturedAt time.Time
	EvidenceID domain.EvidenceID
}

type Extractor interface {
	Name() string
	Version() string
	Extract(ctx context.Context, in Input) ([]domain.ObservedFact, error)
}

// FactSet is a convenience view over facts for state rules.
type FactSet []domain.ObservedFact

func NewFactSet(fs []domain.ObservedFact) FactSet { return FactSet(fs) }

func (fs FactSet) find(pred string) (domain.ObservedFact, bool) {
	for i := len(fs) - 1; i >= 0; i-- {
		if fs[i].Predicate == pred {
			return fs[i], true
		}
	}
	return domain.ObservedFact{}, false
}

func (fs FactSet) Time(pred string) (time.Time, bool) {
	f, ok := fs.find(pred)
	if !ok {
		return time.Time{}, false
	}
	switch v := f.Value.(type) {
	case time.Time:
		return v, true
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	default:
		return time.Time{}, false
	}
}

func (fs FactSet) String(pred string) (string, bool) {
	f, ok := fs.find(pred)
	if !ok {
		return "", false
	}
	s, ok := f.Value.(string)
	return s, ok
}

func (fs FactSet) Bool(pred string) (bool, bool) {
	f, ok := fs.find(pred)
	if !ok {
		return false, false
	}
	b, ok := f.Value.(bool)
	return b, ok
}

func (fs FactSet) Int(pred string) (int, bool) {
	f, ok := fs.find(pred)
	if !ok {
		return 0, false
	}
	switch v := f.Value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
