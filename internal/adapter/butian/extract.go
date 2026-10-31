package butian

import (
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
	gates := []domain.EligibilityGate{
		{Kind: domain.GateProgramSignup, Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true, Evidence: []domain.EvidenceID{evID}},
		{Kind: domain.GatePracticalCertification, ProgramID: &pidCopy, Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true, Evidence: []domain.EvidenceID{evID}},
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
			Decision: domain.HumanActionNeeded, Gates: gates,
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
