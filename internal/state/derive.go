package state

import (
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

// ProgramInput is the canonical, platform-neutral view used by state rules.
// Adapters map platform-native facts into this shape.
type ProgramInput struct {
	Status            *domain.ProgramStatus
	Paused            bool
	Start             *time.Time
	End               *time.Time
	RegistrationStart *time.Time
	RegistrationEnd   *time.Time
	ScopeVisibility   domain.ScopeVisibility
	FactIDs           []domain.FactID
	Evidence          []domain.EvidenceID
	// Confidence is the minimum confidence of the facts used by the derivation.
	// It is zero when no facts were present, which keeps the result unexecutable.
	Confidence domain.Confidence
	// OldestObservedAt is the oldest observed_at among the facts actually
	// selected for the derivation. A derived state must not outlive its inputs.
	OldestObservedAt time.Time
}

// DefaultFactTTL bounds how long a fact may keep driving a derived state. A
// derived state whose newest relevant fact is older than this cannot be
// produced at all, which is what stops an old page from being "refreshed" into
// a fresh-looking state by re-running derive.
const DefaultFactTTL = 30 * 24 * time.Hour

func ProgramInputFromFacts(fs facts.FactSet) (ProgramInput, error) {
	var in ProgramInput
	seenFact := make(map[domain.FactID]struct{})
	seenEvidence := make(map[domain.EvidenceID]struct{})
	minConfidence := domain.Confidence(1)
	haveConfidence := false
	badConfidence := ""
	record := func(pred string) {
		f, ok := fs.Fact(pred)
		if !ok {
			return
		}
		if !f.Confidence.Valid() {
			badConfidence = string(f.ID)
			return
		}
		if !haveConfidence || f.Confidence < minConfidence {
			minConfidence = f.Confidence
			haveConfidence = true
		}
		if _, ok := seenFact[f.ID]; !ok {
			seenFact[f.ID] = struct{}{}
			in.FactIDs = append(in.FactIDs, f.ID)
		}
		for _, ev := range f.EvidenceRefs {
			if _, ok := seenEvidence[ev]; !ok {
				seenEvidence[ev] = struct{}{}
				in.Evidence = append(in.Evidence, ev)
			}
		}
	}
	predicates := []string{
		facts.PredProgramStatus, facts.PredProgramPaused, facts.PredProgramStart,
		facts.PredProgramEnd, facts.PredActivityStart, facts.PredActivityEnd,
		facts.PredRegistrationStart, facts.PredRegistrationEnd, facts.PredScopeVisibility,
	}
	for _, p := range predicates {
		record(p)
	}
	// Track the age of the facts the derivation will actually read (the latest
	// observation per predicate), not the age of every historical fact.
	for _, p := range predicates {
		f, ok := fs.Fact(p)
		if !ok {
			continue
		}
		if in.OldestObservedAt.IsZero() || f.ObservedAt.Before(in.OldestObservedAt) {
			in.OldestObservedAt = f.ObservedAt
		}
	}
	if s, ok := fs.String(facts.PredProgramStatus); ok && s != "" {
		st := domain.ProgramStatus(s)
		if !st.Valid() {
			return in, fmt.Errorf("state: invalid program.status %q", s)
		}
		in.Status = &st
	}
	if b, ok := fs.Bool(facts.PredProgramPaused); ok {
		in.Paused = b
	}
	if t, ok := fs.Time(facts.PredProgramStart); ok {
		in.Start = &t
	} else if t, ok := fs.Time(facts.PredActivityStart); ok {
		in.Start = &t
	}
	if t, ok := fs.Time(facts.PredProgramEnd); ok {
		in.End = &t
	} else if t, ok := fs.Time(facts.PredActivityEnd); ok {
		in.End = &t
	}
	if t, ok := fs.Time(facts.PredRegistrationStart); ok {
		in.RegistrationStart = &t
	}
	if t, ok := fs.Time(facts.PredRegistrationEnd); ok {
		in.RegistrationEnd = &t
	}
	if s, ok := fs.String(facts.PredScopeVisibility); ok && s != "" {
		v := domain.ScopeVisibility(s)
		if !v.Valid() {
			return in, fmt.Errorf("state: invalid scope visibility %q", s)
		}
		in.ScopeVisibility = v
	}
	if badConfidence != "" {
		return in, fmt.Errorf("state: fact %s has confidence out of range", badConfidence)
	}
	if haveConfidence {
		in.Confidence = minConfidence
	}
	return in, nil
}

// DeriveProgramState computes RUNNING/PAUSED/UPCOMING/ENDED and a TTL. It does
// not mutate the program; the caller stores the result with evidence.
func DeriveProgramState(in ProgramInput, now time.Time, ttl time.Duration) (domain.ProgramState, error) {
	now = now.UTC()
	if !in.Confidence.Valid() {
		return domain.ProgramState{}, fmt.Errorf("state: confidence %v out of range", in.Confidence)
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	status := domain.StatusUnknown
	reason := ""

	switch {
	case in.Paused:
		status = domain.StatusPaused
		reason = "paused_fact"
	case in.Status != nil && (*in.Status == domain.StatusPaused || *in.Status == domain.StatusEnded):
		// An explicit pause/stop is authoritative even if a date window says
		// otherwise.
		status = *in.Status
		reason = "explicit_status_fact"
	case in.End != nil && now.After(*in.End):
		// A passed inclusive end date wins over a stale explicit RUNNING
		// fact. Platforms often keep the old status for hours after the end.
		status = domain.StatusEnded
		reason = "after_end"
	case in.Status != nil:
		status = *in.Status
		reason = "explicit_status_fact"
	case in.Start != nil && now.Before(*in.Start):
		status = domain.StatusUpcoming
		reason = "before_start"
	case in.Start != nil || in.End != nil:
		status = domain.StatusRunning
		reason = "within_window"
	}

	if !status.Valid() {
		return domain.ProgramState{}, fmt.Errorf("state: invalid derived status %q", status)
	}
	// The state may not be fresher than the facts behind it. Re-running derive
	// with two-month-old facts must not manufacture a new 30-minute window.
	observed := in.OldestObservedAt
	validUntil := now.Add(ttl)
	if !observed.IsZero() {
		if factUntil := observed.Add(DefaultFactTTL); factUntil.Before(validUntil) {
			validUntil = factUntil
		}
	} else {
		observed = now
	}
	if !validUntil.After(now) {
		return domain.ProgramState{}, fmt.Errorf("state: input facts are older than the fact TTL (%s)", DefaultFactTTL)
	}
	return domain.ProgramState{
		Status:     status,
		Reason:     reason,
		ObservedAt: observed,
		ValidUntil: validUntil,
		Confidence: in.Confidence,
		Inputs:     in.FactIDs,
		Evidence:   in.Evidence,
	}, nil
}

// DeriveRegistrationOpen returns whether signup is open at now.
func DeriveRegistrationOpen(in ProgramInput, now time.Time) (bool, error) {
	if in.RegistrationStart == nil || in.RegistrationEnd == nil {
		return false, fmt.Errorf("state: registration window is unknown")
	}
	now = now.UTC()
	return !now.Before(*in.RegistrationStart) && !now.After(*in.RegistrationEnd), nil
}

// DeriveScopeVisibility normalizes missing visibility to UNKNOWN.
func DeriveScopeVisibility(in ProgramInput) (domain.ScopeVisibility, error) {
	if in.ScopeVisibility == "" {
		return domain.ScopeUnknown, nil
	}
	if !in.ScopeVisibility.Valid() {
		return "", fmt.Errorf("state: invalid scope visibility %q", in.ScopeVisibility)
	}
	return in.ScopeVisibility, nil
}

// DeriveAccountCapability computes account-level abilities from gates. Program
// gates must not be passed here; account gates only.
func DeriveAccountCapability(gates []domain.EligibilityGate, identity, compliance, bank *bool, now time.Time) domain.AccountCapability {
	identityOK := identity != nil && *identity
	cap := domain.AccountCapability{
		CanViewPrograms: true,
		CanViewScope:    identityOK,
		CanJoinPrograms: identityOK,
		CanSubmit:       identityOK,
		CanPayout:       identityOK && bank != nil && *bank,
		EvaluatedAt:     now.UTC(),
	}
	if compliance != nil && !*compliance {
		cap.CanSubmit = false
	}
	for _, g := range gates {
		// Program-scoped gates must not affect account-wide capability.
		if g.ProgramID != nil {
			continue
		}
		if !g.Required || !g.Blocking || g.Satisfied != domain.GateUnsatisfied {
			continue
		}
		cap.BlockingReasons = append(cap.BlockingReasons, g)
		switch g.Kind {
		case domain.GateComplianceExam:
			cap.CanSubmit = false
		case domain.GateBankBinding, domain.GatePayoutSetup:
			cap.CanPayout = false
		case domain.GateIdentityVerified:
			cap.CanViewScope = false
			cap.CanSubmit = false
		}
	}
	return cap
}
