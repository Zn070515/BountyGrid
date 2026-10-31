package xianzhi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

func mapTaskStatus(code int) domain.ProgramStatus {
	switch code {
	case 4:
		return domain.StatusRunning
	case 5:
		return domain.StatusEnded
	case 6:
		return domain.StatusPaused
	default:
		return domain.StatusUnknown
	}
}

func mapTaskType(code int) (domain.ProgramType, error) {
	switch code {
	case 1:
		return domain.TypeBrandSRC, nil
	case 2:
		return domain.TypeCrowdTest, nil
	default:
		return "", fmt.Errorf("xianzhi: unknown task type %d: %w", code, adapter.ErrUnsupported)
	}
}

var bountyRE = regexp.MustCompile(`^\s*[¥￥]?\s*(\d+)\s*[-~至]\s*[¥￥]?\s*(\d+)\s*元?\s*$`)

// parseBounty accepts "20 - 3375", "¥100-¥2000" and "-" (no bounty stated).
func parseBounty(s string) (min, max int64) {
	m := bountyRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0
	}
	min, _ = strconv.ParseInt(m[1], 10, 64)
	max, _ = strconv.ParseInt(m[2], 10, 64)
	if max < min {
		min, max = max, min
	}
	return min, max
}

func unixMillis(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

func hasButton(buttons []string, want string) bool {
	for _, b := range buttons {
		if strings.TrimSpace(b) == want {
			return true
		}
	}
	return false
}

func eligibilityForCard(c taskCard, evID domain.EvidenceID, at time.Time) domain.EligibilityState {
	hasSignup := hasButton(c.Buttons, "我要报名")
	hasSubmit := hasButton(c.Buttons, "提交漏洞") || hasButton(c.Buttons, "已报名")
	gates := make([]domain.EligibilityGate, 0, 1)
	decision := domain.EligibilityUnknown
	switch {
	case hasSubmit:
		gates = append(gates, taskGate(domain.GateProgramSignup, domain.GateSatisfied, true, evID))
		decision = domain.Eligible
	case hasSignup:
		gates = append(gates, taskGate(domain.GateProgramSignup, domain.GateUnsatisfied, true, evID))
		decision = domain.HumanActionNeeded
	default:
		gates = append(gates, taskGate(domain.GateProgramSignup, domain.GateUnknown, false, evID))
	}
	return domain.EligibilityState{
		Decision:      decision,
		Gates:         gates,
		EvaluatedAt:   at,
		PolicyVersion: extractor,
	}
}

func taskGate(kind domain.GateKind, satisfied domain.GateSatisfaction, blocking bool, evID domain.EvidenceID) domain.EligibilityGate {
	return domain.EligibilityGate{
		Kind: kind, Required: true, Satisfied: satisfied, Blocking: blocking,
		Evidence: []domain.EvidenceID{evID},
	}
}

func taskProgram(c taskCard, evID domain.EvidenceID, at time.Time) (domain.Program, []domain.ObservedFact, error) {
	if c.Missing || c.ID == 0 {
		return domain.Program{}, nil, fmt.Errorf("xianzhi: task card has no id: %w", adapter.ErrUnsupported)
	}
	ptype, err := mapTaskType(c.Type)
	if err != nil {
		return domain.Program{}, nil, err
	}
	pid := domain.ProgramID(fmt.Sprintf("xianzhi/%d", c.ID))
	status := mapTaskStatus(c.Status)
	start := unixMillis(c.TaskBegin)
	end := unixMillis(c.TaskEnd)

	fs := []domain.ObservedFact{
		taskFact(evID, pid, facts.PredProgramStatus, string(status), "", at),
		taskFact(evID, pid, facts.PredProgramPaused, status == domain.StatusPaused, "", at),
		taskFact(evID, pid, facts.PredScopeVisibility, string(domain.ScopeGatedBySignup), "", at),
		taskFact(evID, pid, "xianzhi.task.type", c.Type, "", at),
		taskFact(evID, pid, "xianzhi.task.status_code", c.Status, "", at),
		taskFact(evID, pid, "xianzhi.task.registration_limit", c.RegistrationLimit, "", at),
		taskFact(evID, pid, "xianzhi.task.bounty", c.Bounty, "", at),
		taskFact(evID, pid, "xianzhi.task.buttons", c.Buttons, "", at),
	}
	statusInputs := []domain.FactID{fs[0].ID, fs[1].ID}
	if start != nil {
		fs = append(fs, taskFact(evID, pid, facts.PredProgramStart, *start, facts.UnitDateTime, at))
		statusInputs = append(statusInputs, fs[len(fs)-1].ID)
	}
	if end != nil {
		fs = append(fs, taskFact(evID, pid, facts.PredProgramEnd, *end, facts.UnitDateTime, at))
		statusInputs = append(statusInputs, fs[len(fs)-1].ID)
	}
	if t := unixMillis(c.RegistrationBegin); t != nil {
		fs = append(fs, taskFact(evID, pid, "xianzhi.task.registration_start", *t, facts.UnitDateTime, at))
	}
	if t := unixMillis(c.RegistrationEnd); t != nil {
		fs = append(fs, taskFact(evID, pid, "xianzhi.task.registration_end", *t, facts.UnitDateTime, at))
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = strings.TrimSpace(c.Title)
	}
	prog := domain.Program{
		ID:       pid,
		Platform: domain.PlatformXianzhi,
		NativeID: strconv.FormatInt(c.ID, 10),
		Name:     name,
		Type:     ptype,
		Status: domain.ProgramState{
			Status:     status,
			Reason:     "xianzhi_task_list_dom",
			ObservedAt: at,
			ValidUntil: at.Add(30 * time.Minute),
			Confidence: 1,
			Inputs:     statusInputs,
			Evidence:   []domain.EvidenceID{evID},
		},
		Eligibility: eligibilityForCard(c, evID, at),
		// The task list never discloses assets before signup. Keep access
		// unknown; SyncScope decides GATED vs granted from the detail page.
		Scope: domain.Scope{
			Visibility:    domain.ScopeGatedBySignup,
			Access:        domain.ScopeAccessUnknown,
			ContentsKnown: false,
			ObservedAt:    at,
			Evidence:      []domain.EvidenceID{evID},
		},
		Reward:      rewardPolicy(c.Bounty),
		Automation:  conservativeAutomation(),
		Evidence:    []domain.EvidenceID{evID},
		ObservedAt:  at,
		NextCheckAt: at.Add(30 * time.Minute),
	}
	if err := prog.Validate(); err != nil {
		return domain.Program{}, nil, fmt.Errorf("xianzhi: invalid program %s: %w", pid, err)
	}
	return prog, fs, nil
}

func taskFact(evID domain.EvidenceID, pid domain.ProgramID, pred string, value any, unit string, at time.Time) domain.ObservedFact {
	return domain.ObservedFact{
		ID: domain.FactID(fmt.Sprintf("%s/%s/%s",
			evID, strings.ReplaceAll(string(pid), "/", "_"), strings.ReplaceAll(pred, ".", "_"))),
		Subject:          domain.SubjectProgram,
		Platform:         domain.PlatformXianzhi,
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

func rewardPolicy(bounty string) domain.RewardPolicy {
	min, max := parseBounty(bounty)
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
