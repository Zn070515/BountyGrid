package policy

import (
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// EvaluateEligibility merges account gates and program gates into one state.
// Account gates are evaluated first in spirit, but both are represented in the
// same gate list for auditability.
func EvaluateEligibility(account domain.Account, program domain.Program, now time.Time, policyVersion string) (domain.EligibilityState, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if policyVersion == "" {
		policyVersion = DefaultPolicyVersion
	}

	byKind := make(map[domain.GateKind]domain.EligibilityGate)
	add := func(g domain.EligibilityGate) error {
		if err := g.Validate(); err != nil {
			return err
		}
		if prev, ok := byKind[g.Kind]; ok {
			byKind[g.Kind] = mergeGate(prev, g)
		} else {
			byKind[g.Kind] = g
		}
		return nil
	}

	if account.IdentityVerified != nil && !*account.IdentityVerified {
		if err := add(domain.EligibilityGate{
			Kind: domain.GateIdentityVerified, Required: true,
			Satisfied: domain.GateUnsatisfied, Blocking: true,
		}); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	if account.ComplianceExamCompleted != nil && !*account.ComplianceExamCompleted {
		if err := add(domain.EligibilityGate{
			Kind: domain.GateComplianceExam, Required: true,
			Satisfied: domain.GateUnsatisfied, Blocking: true,
		}); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	for _, g := range account.Gates {
		if err := add(g); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	for _, g := range account.Capability.BlockingReasons {
		if err := add(g); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	for _, g := range program.Gates {
		if err := add(g); err != nil {
			return domain.EligibilityState{}, err
		}
	}

	gates := make([]domain.EligibilityGate, 0, len(byKind))
	for _, g := range byKind {
		gates = append(gates, g)
	}

	humanBlock := false
	nonHumanBlock := false
	unknown := false
	for _, g := range gates {
		if !g.Required {
			continue
		}
		if g.Satisfied == domain.GateUnknown {
			unknown = true
		}
		if g.Blocking && g.Satisfied == domain.GateUnsatisfied {
			if g.Kind.HumanResolvable() {
				humanBlock = true
			} else {
				nonHumanBlock = true
			}
		}
	}

	decision := domain.Eligible
	switch {
	case humanBlock:
		decision = domain.HumanActionNeeded
	case nonHumanBlock:
		decision = domain.Ineligible
	case unknown:
		decision = domain.EligibilityUnknown
	}

	st := domain.EligibilityState{
		Decision:      decision,
		Gates:         gates,
		EvaluatedAt:   now,
		PolicyVersion: policyVersion,
	}
	if err := st.Validate(); err != nil {
		return st, fmt.Errorf("policy: eligibility: %w", err)
	}
	return st, nil
}

func mergeGate(a, b domain.EligibilityGate) domain.EligibilityGate {
	out := a
	out.Required = a.Required || b.Required
	out.Blocking = a.Blocking || b.Blocking
	if severity(b.Satisfied) > severity(a.Satisfied) {
		out.Satisfied = b.Satisfied
	}
	out.Evidence = append(append([]domain.EvidenceID{}, a.Evidence...), b.Evidence...)
	if out.Notes == "" {
		out.Notes = b.Notes
	}
	return out
}

func severity(s domain.GateSatisfaction) int {
	switch s {
	case domain.GateUnsatisfied:
		return 3
	case domain.GateUnknown:
		return 2
	case domain.GateSatisfied:
		return 1
	default:
		return 0
	}
}
