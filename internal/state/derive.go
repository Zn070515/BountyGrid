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
}

func ProgramInputFromFacts(fs facts.FactSet) (ProgramInput, error) {
	var in ProgramInput
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
	return in, nil
}

// DeriveProgramState computes RUNNING/PAUSED/UPCOMING/ENDED and a TTL. It does
// not mutate the program; the caller stores the result with evidence.
func DeriveProgramState(in ProgramInput, now time.Time, ttl time.Duration) (domain.ProgramState, error) {
	now = now.UTC()
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	status := domain.StatusUnknown
	reason := ""

	switch {
	case in.Paused:
		status = domain.StatusPaused
		reason = "paused_fact"
	case in.Status != nil:
		status = *in.Status
		reason = "explicit_status_fact"
	case in.End != nil && now.After(*in.End):
		status = domain.StatusEnded
		reason = "after_end"
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
	return domain.ProgramState{
		Status:     status,
		Reason:     reason,
		ObservedAt: now,
		ValidUntil: now.Add(ttl),
		Confidence: 0.9,
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
		if !g.Required || !g.Blocking || g.Satisfied != domain.GateUnsatisfied {
			continue
		}
		cap.BlockingReasons = append(cap.BlockingReasons, g)
		switch g.Kind {
		case domain.GateComplianceExam, domain.GatePracticalCertification:
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
