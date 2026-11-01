package butian

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	butianfacts "github.com/Zn070515/BountyGrid/internal/facts/butian"
	"github.com/Zn070515/BountyGrid/internal/state"
)

var butianBountyRE = regexp.MustCompile(`奖金范围[：:]\s*([\d,]+)\s*~\s*([\d,]+)\s*元?`)

// parseBounty reads the exclusive-SRC card text ("奖金范围：50 ~ 20,000元").
func parseBounty(text string) (min, max int64) {
	m := butianBountyRE.FindStringSubmatch(text)
	if m == nil {
		return 0, 0
	}
	min = commasToInt(m[1])
	max = commasToInt(m[2])
	if max < min {
		min, max = max, min
	}
	return min, max
}

func commasToInt(s string) int64 {
	v, _ := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	return v
}

func cardProgram(c srcCard, evID domain.EvidenceID, at time.Time) (domain.Program, []domain.ObservedFact, error) {
	name := strings.TrimSpace(c.Name)
	if c.Missing || name == "" {
		return domain.Program{}, nil, fmt.Errorf("butian: exclusive SRC card has no name: %w", adapter.ErrUnsupported)
	}
	sum := sha1.Sum([]byte(name))
	pid := domain.ProgramID("butian/src/" + hex.EncodeToString(sum[:6]))
	status := domain.StatusRunning
	reason := "butian_exclusive_src_card"
	switch {
	case strings.Contains(c.Text, "已结束"):
		status = domain.StatusEnded
	case strings.Contains(c.Text, "暂停"):
		status = domain.StatusPaused
	}

	fs := []domain.ObservedFact{
		cardFact(evID, pid, facts.PredProgramStatus, string(status), "", at),
		cardFact(evID, pid, facts.PredProgramPaused, status == domain.StatusPaused, "", at),
		cardFact(evID, pid, facts.PredScopeVisibility, string(domain.ScopeGatedBySignup), "", at),
		cardFact(evID, pid, "butian.src.name", name, "", at),
		cardFact(evID, pid, "butian.src.type", "EXCLUSIVE_SRC", "", at),
		cardFact(evID, pid, "butian.src.buttons", c.Buttons, "", at),
		cardFact(evID, pid, "butian.src.text", truncate(c.Text, 300), "", at),
	}
	statusInputs := []domain.FactID{fs[0].ID, fs[1].ID}
	min, max := parseBounty(c.Text)
	reward := domain.RewardPolicy{TaxBasis: domain.TaxUnknown}
	if min > 0 {
		reward.Min = &domain.Money{Amount: min, Currency: "CNY"}
	}
	if max > 0 {
		reward.Max = &domain.Money{Amount: max, Currency: "CNY"}
		reward.Types = []domain.RewardType{domain.RewardCash}
	}

	pidCopy := pid
	// The card states that these gates are required; it cannot prove whether
	// this account satisfies them. Practical certification is resolved at
	// policy time from Account.PracticalCertification, so it must not be frozen
	// as UNSATISFIED here. Signup is inferred only from an explicit
	// "已报名"/"立即报名"-style phrase.
	gates := []domain.EligibilityGate{
		{Kind: domain.GateProgramSignup, Required: true, Satisfied: signupSatisfaction(c.Text, c.Buttons), Blocking: true, Evidence: []domain.EvidenceID{evID}},
		{Kind: domain.GatePracticalCertification, ProgramID: &pidCopy, Required: true, Satisfied: domain.GateUnknown, Blocking: true, Evidence: []domain.EvidenceID{evID}},
	}
	prog := domain.Program{
		ID:       pid,
		Platform: domain.PlatformButian,
		NativeID: name,
		Name:     name,
		Type:     domain.TypePrivateExclusive,
		Status: domain.ProgramState{
			Status: status, Reason: reason, ObservedAt: at,
			ValidUntil: at.Add(30 * time.Minute), Confidence: 1,
			Inputs: statusInputs, Evidence: []domain.EvidenceID{evID},
		},
		Eligibility: domain.EligibilityState{
			Decision: eligibilityFromGates(gates), Gates: gates,
			EvaluatedAt: at, PolicyVersion: extractor,
		},
		Scope: domain.Scope{
			Visibility:    domain.ScopeGatedBySignup,
			Access:        domain.ScopeAccessNotGranted,
			ContentsKnown: false,
			ObservedAt:    at,
			Evidence:      []domain.EvidenceID{evID},
		},
		Reward:      reward,
		Automation:  conservativeAutomation(),
		Evidence:    []domain.EvidenceID{evID},
		ObservedAt:  at,
		NextCheckAt: at.Add(30 * time.Minute),
	}
	if err := prog.Validate(); err != nil {
		return domain.Program{}, nil, fmt.Errorf("butian: invalid program %s: %w", pid, err)
	}
	return prog, fs, nil
}

func cardFact(evID domain.EvidenceID, pid domain.ProgramID, pred string, value any, unit string, at time.Time) domain.ObservedFact {
	return domain.ObservedFact{
		ID: domain.FactID(fmt.Sprintf("%s/%s/%s",
			evID, strings.ReplaceAll(string(pid), "/", "_"), strings.ReplaceAll(pred, ".", "_"))),
		Subject:          domain.SubjectProgram,
		Platform:         domain.PlatformButian,
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

func accountFact(ev domain.EvidenceID, accountID domain.AccountID, pred string, value any, at time.Time) domain.ObservedFact {
	return domain.ObservedFact{
		ID:               domain.FactID(fmt.Sprintf("%s/%s", ev, strings.ReplaceAll(pred, ".", "_"))),
		Subject:          domain.SubjectAccount,
		Platform:         domain.PlatformButian,
		AccountID:        &accountID,
		Predicate:        pred,
		Value:            value,
		ObservedAt:       at,
		EvidenceRefs:     []domain.EvidenceID{ev},
		Confidence:       1,
		ExtractorVersion: extractor,
	}
}

// publicWelfareProgram maps one public-welfare vendor row. These vendors have
// no program-level scope disclosure; assets remain UNKNOWN until a human opens
// the vendor's assets.
func publicWelfareProgram(v pubVendor, evID domain.EvidenceID, at time.Time) (domain.Program, []domain.ObservedFact, error) {
	if strings.TrimSpace(v.CompanyID) == "" || strings.TrimSpace(v.CompanyName) == "" {
		return domain.Program{}, nil, fmt.Errorf("butian: public welfare vendor missing id/name: %w", adapter.ErrUnsupported)
	}
	pid := domain.ProgramID("butian/pub/" + v.CompanyID)
	// The list proves only that the vendor is listed. It does not state a
	// program status, so RUNNING is a derived state from `listed=true`, not an
	// ObservedFact pretending the platform said it.
	listed := cardFact(evID, pid, "butian.pub.listed", true, "", at)
	fs := []domain.ObservedFact{
		listed,
		cardFact(evID, pid, facts.PredScopeVisibility, string(domain.ScopeUnknown), "", at),
		cardFact(evID, pid, "butian.pub.company_id", v.CompanyID, "", at),
		cardFact(evID, pid, "butian.pub.company_name", v.CompanyName, "", at),
		cardFact(evID, pid, "butian.pub.source", publicWelfareAPI, "", at),
	}
	prog := domain.Program{
		ID: pid, Platform: domain.PlatformButian, NativeID: v.CompanyID,
		Name: v.CompanyName, Type: domain.TypePublicWelfareSRC,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, Reason: "derived_from_public_welfare_listing",
			ObservedAt: at, ValidUntil: at.Add(30 * time.Minute), Confidence: 0.8,
			Inputs: []domain.FactID{listed.ID}, Evidence: []domain.EvidenceID{evID},
		},
		Eligibility: domain.EligibilityState{
			Decision: domain.Eligible, EvaluatedAt: at, PolicyVersion: extractor,
		},
		Scope: domain.Scope{
			Visibility:    domain.ScopeUnknown,
			Access:        domain.ScopeAccessUnknown,
			ContentsKnown: false,
			ObservedAt:    at,
			Evidence:      []domain.EvidenceID{evID},
		},
		Reward:      domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation:  conservativeAutomation(),
		Evidence:    []domain.EvidenceID{evID},
		ObservedAt:  at,
		NextCheckAt: at.Add(30 * time.Minute),
	}
	if err := prog.Validate(); err != nil {
		return domain.Program{}, nil, fmt.Errorf("butian: invalid public welfare program %s: %w", pid, err)
	}
	return prog, fs, nil
}

// capturedText is one rendered page plus the Evidence that stores it.
type capturedText struct {
	Text       string
	EvidenceID domain.EvidenceID
}

// growProgram derives the single GROW campus program. Each page is parsed by
// the versioned GrowExtractor so every fact keeps the EvidenceID of the page
// that stated it; the state is then derived from the combined FactSet.
func growProgram(ctx context.Context, pages []capturedText, at time.Time) (domain.Program, []domain.ObservedFact, error) {
	pid := domain.ProgramID("butian/grow")
	if len(pages) == 0 {
		return domain.Program{}, nil, fmt.Errorf("butian: GROW has no captured page: %w", adapter.ErrUnsupported)
	}
	var fs []domain.ObservedFact
	var evIDs []domain.EvidenceID
	seenEV := make(map[domain.EvidenceID]struct{})
	for _, p := range pages {
		got, err := (butianfacts.GrowExtractor{}).Extract(ctx, facts.Input{
			Platform: domain.PlatformButian, ProgramID: &pid, Text: p.Text,
			CapturedAt: at, EvidenceID: p.EvidenceID,
		})
		if err != nil {
			return domain.Program{}, nil, err
		}
		fs = append(fs, got...)
		if _, ok := seenEV[p.EvidenceID]; !ok {
			seenEV[p.EvidenceID] = struct{}{}
			evIDs = append(evIDs, p.EvidenceID)
		}
	}
	in, err := state.ProgramInputFromFacts(facts.NewFactSet(fs))
	if err != nil {
		return domain.Program{}, nil, err
	}
	if in.RegistrationStart == nil || in.RegistrationEnd == nil || in.Start == nil || in.End == nil {
		return domain.Program{}, nil, fmt.Errorf("butian: GROW registration/activity dates missing: %w", adapter.ErrUnsupported)
	}
	st, err := state.DeriveProgramState(in, at, 30*time.Minute)
	if err != nil {
		return domain.Program{}, nil, err
	}

	// Gates come only from phrases the page actually states. A registration
	// window proves the requirement, not that this account has not signed up;
	// satisfaction is inferred only from explicit "已报名"/"立即报名" wording.
	gates := make([]domain.EligibilityGate, 0, 3)
	if ev := growEvidence(pages, "报名时间", "立即报名", "已报名", "已加入", "报名成功", "未报名"); ev != "" {
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateProgramSignup, Required: true,
			Satisfied: gateFromText(pages, []string{"已报名", "已加入", "报名成功"}, []string{"立即报名", "未报名"}),
			Blocking:  true,
			Evidence:  []domain.EvidenceID{ev},
		})
	}
	for _, c := range []struct {
		kind    domain.GateKind
		needles []string
	}{
		{domain.GateStudentVerification, []string{"学生认证待完成", "学生认证未完成", "尚未完成学生认证"}},
		{domain.GateTeamMembership, []string{"尚未加入校园团队", "未加入校园团队"}},
	} {
		if ev := growEvidence(pages, c.needles...); ev != "" {
			gates = append(gates, domain.EligibilityGate{
				Kind: c.kind, Required: true,
				Satisfied: domain.GateUnsatisfied, Blocking: true,
				Evidence: []domain.EvidenceID{ev},
			})
		}
	}
	scope := domain.Scope{
		Visibility:    domain.ScopeGatedBySignup,
		Access:        domain.ScopeAccessNotGranted,
		ContentsKnown: false,
		ObservedAt:    at,
		Evidence:      evIDs,
	}
	prog := domain.Program{
		ID: pid, Platform: domain.PlatformButian, NativeID: "grow",
		Name: "补天 GROW", Type: domain.TypeCampusProgram,
		Status: st,
		Eligibility: domain.EligibilityState{
			Decision: eligibilityFromGates(gates), Gates: gates,
			EvaluatedAt: at, PolicyVersion: extractor,
		},
		Scope:       scope,
		Reward:      domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation:  conservativeAutomation(),
		Evidence:    evIDs,
		ObservedAt:  at,
		NextCheckAt: at.Add(30 * time.Minute),
	}
	if err := prog.Validate(); err != nil {
		return domain.Program{}, nil, fmt.Errorf("butian: invalid GROW program: %w", err)
	}
	return prog, fs, nil
}

// growEvidence returns the EvidenceID of the first page containing any needle,
// or "" when no page states it.
func growEvidence(pages []capturedText, needles ...string) domain.EvidenceID {
	for _, n := range needles {
		for _, p := range pages {
			if strings.Contains(p.Text, n) {
				return p.EvidenceID
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------- account ----------

type accountText struct {
	Level      string
	HonorCoins int
	Points     int
	Bounty     float64
	Submitted  int
	Pending    int
	Rank       string
	Identity   *bool
	Practical  *bool
	Compliance *bool
	BankBound  *bool
}

var (
	reLevel     = regexp.MustCompile(`(实习白帽|初级白帽|中级白帽|高级白帽|精英白帽|安全专家)`)
	reHonor     = regexp.MustCompile(`(\d+)\s*荣誉币`)
	rePoints    = regexp.MustCompile(`(\d+)\s*积分`)
	reBounty    = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*已获奖金`)
	reSubmitted = regexp.MustCompile(`已提交\s*(\d+)`)
	rePending   = regexp.MustCompile(`待审核\s*(\d+)`)
	reRank      = regexp.MustCompile(`补天排名\s*([-\d]+)`)
)

func parseAccountText(text string) accountText {
	first := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(text)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	out := accountText{
		Level:      first(reLevel),
		HonorCoins: atoiSafe(first(reHonor)),
		Points:     atoiSafe(first(rePoints)),
		Submitted:  atoiSafe(first(reSubmitted)),
		Pending:    atoiSafe(first(rePending)),
		Rank:       first(reRank),
	}
	if v := first(reBounty); v != "" {
		out.Bounty, _ = strconv.ParseFloat(v, 64)
	}

	switch {
	case strings.Contains(text, "未完成实战认证"):
		v := false
		out.Practical = &v
	case strings.Contains(text, "已完成实战认证"), strings.Contains(text, "实战认证已认证"):
		v := true
		out.Practical = &v
	}

	switch {
	case strings.Contains(text, "未实名"):
		v := false
		out.Identity = &v
	case strings.Contains(text, "已实名"), strings.Contains(text, "实名认证") && strings.Contains(text, "已认证"):
		v := true
		out.Identity = &v
	}

	if strings.Contains(text, "银行卡") {
		switch {
		case strings.Contains(text, "银行卡未绑定"), strings.Contains(text, "未绑定银行卡"):
			v := false
			out.BankBound = &v
		case strings.Contains(text, "银行卡已绑定"), strings.Contains(text, "已绑定银行卡"):
			v := true
			out.BankBound = &v
		}
	}
	return out
}

func atoiSafe(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

// signupSatisfaction infers enrollment only from explicit wording. A missing
// phrase is UNKNOWN, never UNSATISFIED: "there is a registration window" is not
// the same fact as "this account has not registered".
func signupSatisfaction(text string, buttons []string) domain.GateSatisfaction {
	return gateFromText(
		[]capturedText{{Text: text + " " + strings.Join(buttons, " ")}},
		[]string{"已报名", "已加入", "报名成功"},
		[]string{"立即报名", "未报名"},
	)
}

func gateFromText(pages []capturedText, satisfied, unsatisfied []string) domain.GateSatisfaction {
	for _, needle := range satisfied {
		for _, p := range pages {
			if strings.Contains(p.Text, needle) {
				return domain.GateSatisfied
			}
		}
	}
	for _, needle := range unsatisfied {
		for _, p := range pages {
			if strings.Contains(p.Text, needle) {
				return domain.GateUnsatisfied
			}
		}
	}
	return domain.GateUnknown
}

func eligibilityFromGates(gates []domain.EligibilityGate) domain.EligibilityDecision {
	humanBlock, nonHumanBlock, unknown := false, false, false
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
	switch {
	case humanBlock:
		return domain.HumanActionNeeded
	case nonHumanBlock:
		return domain.Ineligible
	case unknown:
		return domain.EligibilityUnknown
	default:
		return domain.Eligible
	}
}

func parseCompliance(text string) *bool {
	switch {
	// Only an explicit "not completed" marker means false. The bare phrase
	// "本季度合规考核" also appears on completed pages ("本季度合规考核已完成"),
	// so it must not be treated as failure.
	case strings.Contains(text, "合规考核未完成"):
		v := false
		return &v
	case strings.Contains(text, "合规考核已完成"), strings.Contains(text, "考核通过"), strings.Contains(text, "已通过合规考核"):
		v := true
		return &v
	default:
		return nil
	}
}
