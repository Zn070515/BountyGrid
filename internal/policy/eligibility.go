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

	byKey := make(map[string]domain.EligibilityGate)
	add := func(g domain.EligibilityGate) error {
		if err := g.Validate(); err != nil {
			return err
		}
		key := gateKey(g)
		if prev, ok := byKey[key]; ok {
			byKey[key] = mergeGate(prev, g)
		} else {
			byKey[key] = g
		}
		return nil
	}
	applies := func(g domain.EligibilityGate) bool {
		return g.ProgramID == nil || *g.ProgramID == program.ID
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
		if !applies(g) {
			continue
		}
		if err := add(g); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	for _, g := range account.Capability.BlockingReasons {
		if !applies(g) {
			continue
		}
		if err := add(g); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	// A program gate records a requirement. The satisfaction in the Program row
	// is only what the program page could prove; overlay the account's observed
	// state so a certification earned later is not permanently blocked by a
	// stale UNSATISFIED value.
	for _, g := range program.Gates {
		if err := add(applyAccountGate(g, account, program.ID)); err != nil {
			return domain.EligibilityState{}, err
		}
	}
	for _, g := range program.Eligibility.Gates {
		if err := add(applyAccountGate(g, account, program.ID)); err != nil {
			return domain.EligibilityState{}, err
		}
	}

	gates := make([]domain.EligibilityGate, 0, len(byKey))
	for _, g := range byKey {
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

// applyAccountGate resolves a gate's satisfaction from the account's observed
// state. Unknown account values leave the gate as-is (usually UNKNOWN).
func applyAccountGate(g domain.EligibilityGate, account domain.Account, programID domain.ProgramID) domain.EligibilityGate {
	if g.ProgramID != nil && *g.ProgramID != programID {
		return g
	}
	switch g.Kind {
	case domain.GatePracticalCertification:
		if account.PracticalCertification != nil {
			g.Satisfied = satisfaction(*account.PracticalCertification)
		}
	case domain.GateComplianceExam:
		if account.ComplianceExamCompleted != nil {
			g.Satisfied = satisfaction(*account.ComplianceExamCompleted)
		}
	case domain.GateIdentityVerified:
		if account.IdentityVerified != nil {
			g.Satisfied = satisfaction(*account.IdentityVerified)
		}
	case domain.GateBankBinding:
		if account.BankBound != nil {
			g.Satisfied = satisfaction(*account.BankBound)
		}
	case domain.GateWechatBinding:
		if account.WechatBound != nil {
			g.Satisfied = satisfaction(*account.WechatBound)
		}
	case domain.GateProgramSignup:
		for _, joined := range account.JoinedProgramIDs {
			if joined == programID {
				g.Satisfied = domain.GateSatisfied
				break
			}
		}
	}
	return g
}

func satisfaction(ok bool) domain.GateSatisfaction {
	if ok {
		return domain.GateSatisfied
	}
	return domain.GateUnsatisfied
}

func gateKey(g domain.EligibilityGate) string {
	pid := ""
	if g.ProgramID != nil {
		pid = string(*g.ProgramID)
	}
	return string(g.Kind) + "|" + pid
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
