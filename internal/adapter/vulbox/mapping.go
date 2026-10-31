package vulbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

const (
	predProjectType      = "vulbox.project.type"
	predProjectStatus    = "vulbox.project.status"
	predProjectJoined    = "vulbox.project.joined"
	predProjectApply     = "vulbox.project.apply"
	predProjectIsSRC     = "vulbox.project.is_src"
	predProjectTitle     = "vulbox.project.title"
	predProjectGoto      = "vulbox.project.goto"
	predProjectRewardMin = "vulbox.project.reward_min"
	predProjectRewardMax = "vulbox.project.reward_max"
	predProjectDifficult = "vulbox.project.difficult"
)

func mapProgramType(code string) (domain.ProgramType, error) {
	switch strings.TrimSpace(code) {
	case "0":
		return domain.TypeGeneralVulnProgram, nil
	case "1":
		return domain.TypePremiumProject, nil
	case "5":
		return domain.TypeCrowdTest, nil
	case "6":
		return domain.TypeOfflineJob, nil
	case "7":
		return domain.TypeEnterpriseSRC, nil
	case "8":
		return domain.TypePublicWelfareSRC, nil
	default:
		return "", fmt.Errorf("vulbox: unknown project type %q: %w", code, adapter.ErrUnsupported)
	}
}

// mapProgramStatus maps the API status code. Unknown codes stay UNKNOWN; they
// must not be guessed into RUNNING.
func mapProgramStatus(code string) domain.ProgramStatus {
	switch strings.TrimSpace(code) {
	case "0":
		return domain.StatusRunning
	case "1":
		return domain.StatusPaused
	case "3":
		return domain.StatusEnded
	default:
		return domain.StatusUnknown
	}
}

func requiresJoin(t domain.ProgramType) bool {
	switch t {
	case domain.TypeEnterpriseSRC, domain.TypePremiumProject, domain.TypeCrowdTest,
		domain.TypeOfflineJob, domain.TypePrivateExclusive:
		return true
	default:
		return false
	}
}

func programFromProject(p apiProject, evID domain.EvidenceID, at time.Time) (domain.Program, []domain.ObservedFact, error) {
	ptype, err := mapProgramType(string(p.Type))
	if err != nil {
		return domain.Program{}, nil, err
	}
	pid := domain.ProgramID("vulbox/" + string(p.ID))
	if p.ID == "" {
		return domain.Program{}, nil, fmt.Errorf("vulbox: project has no id: %w", adapter.ErrUnsupported)
	}
	status := mapProgramStatus(string(p.Status))
	start := time.Unix(int64(p.TaskStime), 0).UTC()
	end := time.Unix(int64(p.TaskEtime), 0).UTC()

	fs := []domain.ObservedFact{
		projectFact(evID, pid, facts.PredProgramStatus, string(status), "", at),
		projectFact(evID, pid, facts.PredProgramPaused, status == domain.StatusPaused, "", at),
		projectFact(evID, pid, facts.PredProgramStart, start, facts.UnitDateTime, at),
		projectFact(evID, pid, facts.PredProgramEnd, end, facts.UnitDateTime, at),
		projectFact(evID, pid, facts.PredScopeVisibility, string(domain.ScopeUnknown), "", at),
		projectFact(evID, pid, predProjectType, string(p.Type), "", at),
		projectFact(evID, pid, predProjectStatus, string(p.Status), "", at),
		projectFact(evID, pid, predProjectJoined, p.IsJoin, "", at),
		projectFact(evID, pid, predProjectApply, p.IsApply, "", at),
		projectFact(evID, pid, predProjectIsSRC, p.TaskIsSrc == "1", "", at),
		projectFact(evID, pid, predProjectTitle, p.TaskTitle, "", at),
		projectFact(evID, pid, predProjectGoto, projectURL(p), "", at),
		projectFact(evID, pid, predProjectRewardMin, p.BonusRange.LowMin, "", at),
		projectFact(evID, pid, predProjectRewardMax, p.BonusRange.SeriousMax, "", at),
		projectFact(evID, pid, predProjectDifficult, p.DifficultName, "", at),
	}
	statusInputs := []domain.FactID{fs[0].ID, fs[1].ID, fs[2].ID, fs[3].ID}

	prog := domain.Program{
		ID:       pid,
		Platform: domain.PlatformVulbox,
		NativeID: string(p.ID),
		Name:     p.TaskTitle,
		Type:     ptype,
		Status: domain.ProgramState{
			Status:     status,
			Reason:     "vulbox_project_list_api",
			ObservedAt: at,
			ValidUntil: at.Add(30 * time.Minute),
			Confidence: 1,
			Inputs:     statusInputs,
			Evidence:   []domain.EvidenceID{evID},
		},
		Eligibility: eligibilityFor(ptype, p, evID, at),
		// The list API does not disclose scope contents. Keep both disclosure
		// and access unknown until SyncScope has observed the program page.
		Scope: domain.Scope{
			Visibility:    domain.ScopeUnknown,
			Access:        domain.ScopeAccessUnknown,
			ContentsKnown: false,
			ObservedAt:    at,
			Evidence:      []domain.EvidenceID{evID},
		},
		Reward:      rewardPolicy(p.BonusRange),
		Automation:  conservativeAutomation(),
		Evidence:    []domain.EvidenceID{evID},
		ObservedAt:  at,
		NextCheckAt: at.Add(30 * time.Minute),
	}
	if err := prog.Validate(); err != nil {
		return domain.Program{}, nil, fmt.Errorf("vulbox: invalid program %s: %w", pid, err)
	}
	return prog, fs, nil
}

func projectFact(evID domain.EvidenceID, pid domain.ProgramID, pred string, value any, unit string, at time.Time) domain.ObservedFact {
	return domain.ObservedFact{
		ID: domain.FactID(fmt.Sprintf("%s/%s/%s",
			evID, strings.ReplaceAll(string(pid), "/", "_"), strings.ReplaceAll(pred, ".", "_"))),
		Subject:          domain.SubjectProgram,
		Platform:         domain.PlatformVulbox,
		ProgramID:        &pid,
		Predicate:        pred,
		Value:            value,
		Unit:             unit,
		ObservedAt:       at,
		EvidenceRefs:     []domain.EvidenceID{evID},
		Confidence:       1,
		ExtractorVersion: extractor,
	}
}

func projectURL(p apiProject) string {
	if strings.TrimSpace(p.Goto) != "" {
		return p.Goto
	}
	return fmt.Sprintf(detailURLFormat, p.ID)
}

func eligibilityFor(ptype domain.ProgramType, p apiProject, evID domain.EvidenceID, at time.Time) domain.EligibilityState {
	gates := make([]domain.EligibilityGate, 0, 1)
	decision := domain.EligibilityUnknown
	switch {
	case p.IsJoin:
		gates = append(gates, gate(domain.GateProgramSignup, true, domain.GateSatisfied, true, evID))
		decision = domain.Eligible
	case p.IsApply:
		gates = append(gates, gate(domain.GateApplicationReview, true, domain.GateUnsatisfied, true, evID))
		decision = domain.HumanActionNeeded
	case requiresJoin(ptype):
		gates = append(gates, gate(domain.GateProgramSignup, true, domain.GateUnsatisfied, true, evID))
		decision = domain.HumanActionNeeded
	default:
		// Ordinary/public-welfare programs may be open or may need joining;
		// the list API does not say. Keep the gate unknown, never eligible.
		gates = append(gates, gate(domain.GateProgramSignup, true, domain.GateUnknown, false, evID))
	}
	return domain.EligibilityState{
		Decision:      decision,
		Gates:         gates,
		EvaluatedAt:   at,
		PolicyVersion: extractor,
	}
}

func gate(kind domain.GateKind, required bool, satisfied domain.GateSatisfaction, blocking bool, evID domain.EvidenceID) domain.EligibilityGate {
	return domain.EligibilityGate{
		Kind: kind, Required: required, Satisfied: satisfied, Blocking: blocking,
		Evidence: []domain.EvidenceID{evID},
	}
}

func rewardPolicy(b apiBonusRange) domain.RewardPolicy {
	min := minPositive(b.LowMin, b.MiddleMin, b.HighMin, b.SeriousMin)
	max := maxPositive(b.LowMax, b.MiddleMax, b.HighMax, b.SeriousMax)
	out := domain.RewardPolicy{TaxBasis: domain.TaxUnknown}
	if min > 0 {
		out.Min = &domain.Money{Amount: min, Currency: "CNY"}
	}
	if max > 0 {
		out.Max = &domain.Money{Amount: max, Currency: "CNY"}
		out.Types = []domain.RewardType{domain.RewardCash}
	}
	return out
}

func minPositive(vs ...int64) int64 {
	var out int64
	for _, v := range vs {
		if v > 0 && (out == 0 || v < out) {
			out = v
		}
	}
	return out
}

func maxPositive(vs ...int64) int64 {
	var out int64
	for _, v := range vs {
		if v > out {
			out = v
		}
	}
	return out
}

// conservativeAutomation is the fail-closed default: the list API carries no
// project-specific automation policy. SyncPolicy fills the real values after
// reading the project page; until then every capability is UNKNOWN.
func conservativeAutomation() domain.AutomationPolicy {
	unknown := domain.CapUnknown
	return domain.AutomationPolicy{
		AIAnalysis: unknown, AutonomousAgent: unknown, ActiveHTTP: unknown,
		BrowserAutomation: unknown, AutomatedScanning: unknown,
		DirectoryEnumeration: unknown, SubdomainDiscovery: unknown,
		HighConcurrency: unknown, BruteForce: unknown, CredentialStuffing: unknown,
		FileUpload: unknown, StateMutation: unknown, RCEVerification: unknown,
		SensitiveDataRead: unknown, InternalNetwork: unknown,
		LateralMovement: unknown, DoS: unknown,
		HumanVerificationRequired: true,
	}
}
