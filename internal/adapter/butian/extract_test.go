package butian

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/Zn070515/BountyGrid/internal/state"
)

var testNow = time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC)

func sampleCard() srcCard {
	return srcCard{
		Name:    "理想汽车",
		Text:    "更新 理想汽车 北京车励行信息技术有限公司（“理想汽车”）非常重视信息安全问题，欢迎广大白帽子提交漏洞。 奖金范围：50 ~ 20,000元提交漏洞",
		Buttons: []string{"提交漏洞"},
	}
}

func TestParseBountyButian(t *testing.T) {
	min, max := parseBounty(sampleCard().Text)
	if min != 50 || max != 20000 {
		t.Fatalf("bounty = %d/%d", min, max)
	}
	if min, max := parseBounty("no bounty"); min != 0 || max != 0 {
		t.Fatalf("empty bounty = %d/%d", min, max)
	}
}

func TestCardProgram(t *testing.T) {
	prog, fs, err := cardProgram(sampleCard(), "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Type != domain.TypePrivateExclusive || prog.Status.Status != domain.StatusRunning {
		t.Fatalf("program = %+v", prog)
	}
	// The card states requirements but cannot prove this account's signup or
	// practical-certification status, so both gates stay UNKNOWN. The policy
	// layer resolves them against the account at decision time.
	if prog.Eligibility.Decision != domain.EligibilityUnknown {
		t.Fatalf("eligibility = %s, want UNKNOWN", prog.Eligibility.Decision)
	}
	if len(prog.Eligibility.Gates) != 2 {
		t.Fatalf("gates = %+v", prog.Eligibility.Gates)
	}
	if prog.Eligibility.Gates[0].Satisfied != domain.GateUnknown {
		t.Fatalf("signup gate = %s, want UNKNOWN", prog.Eligibility.Gates[0].Satisfied)
	}
	if prog.Eligibility.Gates[1].Kind != domain.GatePracticalCertification || prog.Eligibility.Gates[1].ProgramID == nil {
		t.Fatalf("practical gate is not program-scoped: %+v", prog.Eligibility.Gates[1])
	}
	if prog.Eligibility.Gates[1].Satisfied != domain.GateUnknown {
		t.Fatalf("practical gate = %s, want UNKNOWN", prog.Eligibility.Gates[1].Satisfied)
	}
	if prog.Scope.Visibility != domain.ScopeGatedBySignup || prog.Scope.Access != domain.ScopeAccessNotGranted || prog.Scope.ContentsKnown {
		t.Fatalf("scope = %+v", prog.Scope)
	}
	if prog.Reward.Min == nil || prog.Reward.Min.Amount != 50 || prog.Reward.Max == nil || prog.Reward.Max.Amount != 20000 {
		t.Fatalf("reward = %+v", prog.Reward)
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program invalid: %v", err)
	}
	if len(fs) == 0 || len(prog.Evidence) == 0 {
		t.Fatal("missing facts/evidence")
	}
}

func TestCardProgramDeterministicID(t *testing.T) {
	a, _, err := cardProgram(sampleCard(), "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := cardProgram(sampleCard(), "ev2", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("ids differ: %s vs %s", a.ID, b.ID)
	}
}

func TestCardProgramSignupFromExplicitButtons(t *testing.T) {
	c := sampleCard()
	c.Buttons = []string{"立即报名"}
	prog, _, err := cardProgram(c, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Eligibility.Gates[0].Satisfied != domain.GateUnsatisfied {
		t.Fatalf("signup gate = %s, want UNSATISFIED for 立即报名", prog.Eligibility.Gates[0].Satisfied)
	}
	if prog.Eligibility.Decision != domain.HumanActionNeeded {
		t.Fatalf("eligibility = %s, want HUMAN_ACTION_REQUIRED", prog.Eligibility.Decision)
	}

	c.Buttons = []string{"已报名"}
	prog, _, err = cardProgram(c, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Eligibility.Gates[0].Satisfied != domain.GateSatisfied {
		t.Fatalf("signup gate = %s, want SATISFIED for 已报名", prog.Eligibility.Gates[0].Satisfied)
	}
}

func TestPublicWelfareEmitsListedFactNotStatus(t *testing.T) {
	prog, fs, err := publicWelfareProgram(pubVendor{CompanyID: "c1", CompanyName: "Acme"}, "ev-pw", testNow)
	if err != nil {
		t.Fatal(err)
	}
	var nativeID, canonicalID domain.FactID
	for _, f := range fs {
		if f.Predicate == facts.PredProgramStatus || f.Predicate == facts.PredProgramPaused {
			t.Fatalf("public welfare must not emit a platform status fact: %s", f.Predicate)
		}
		switch f.Predicate {
		case "butian.pub.listed":
			nativeID = f.ID
		case facts.PredProgramListed:
			canonicalID = f.ID
		}
	}
	if nativeID == "" || canonicalID == "" {
		t.Fatalf("listed facts missing: native=%q canonical=%q", nativeID, canonicalID)
	}
	if len(prog.Status.Inputs) != 2 {
		t.Fatalf("status inputs = %v, want both listed facts", prog.Status.Inputs)
	}
	if prog.Status.Confidence != 0.8 {
		t.Fatalf("status confidence = %v, want 0.8", prog.Status.Confidence)
	}

	// `derive` must reproduce RUNNING from the same facts (the adapter's
	// hand-written status is not what the scheduler reads).
	in, err := state.ProgramInputFromFacts(facts.NewFactSet(fs))
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.DeriveProgramState(in, testNow, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusRunning {
		t.Fatalf("derived status = %s, want RUNNING from program.listed", st.Status)
	}
	if st.Confidence != 0.8 {
		t.Fatalf("derived confidence = %v, want 0.8", st.Confidence)
	}
}

func growPages(text string) []capturedText {
	return []capturedText{{Text: text, EvidenceID: "ev-grow"}}
}

func TestGrowSignupGateIsUnknownWithoutExplicitState(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	text := "报名时间：2026.8.25~2026.11.30\n活动时间：2026.9.1~2026.11.30"
	prog, _, err := growProgram(context.Background(), growPages(text), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Eligibility.Gates) == 0 {
		t.Fatal("signup gate missing")
	}
	if prog.Eligibility.Gates[0].Satisfied != domain.GateUnknown {
		t.Fatalf("signup gate = %s, want UNKNOWN when only a window is stated", prog.Eligibility.Gates[0].Satisfied)
	}
	if prog.Eligibility.Decision != domain.EligibilityUnknown {
		t.Fatalf("eligibility = %s, want UNKNOWN", prog.Eligibility.Decision)
	}
}

func TestGrowSignupGateFromExplicitText(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	text := "报名时间：2026.8.25~2026.11.30\n活动时间：2026.9.1~2026.11.30\n已报名"
	prog, _, err := growProgram(context.Background(), growPages(text), at)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Eligibility.Gates[0].Satisfied != domain.GateSatisfied {
		t.Fatalf("signup gate = %s, want SATISFIED", prog.Eligibility.Gates[0].Satisfied)
	}
}

func TestParseCompliance(t *testing.T) {
	if v := parseCompliance("本季度合规考核未完成，请先完成考核"); v == nil || *v {
		t.Fatalf("compliance = %v, want false", v)
	}
	if v := parseCompliance("合规考核已完成"); v == nil || !*v {
		t.Fatalf("compliance = %v, want true", v)
	}
	if v := parseCompliance("本季度合规考核已完成，可提交漏洞"); v == nil || !*v {
		t.Fatalf("quarterly completed compliance = %v, want true", v)
	}
	if v := parseCompliance("无关文本"); v != nil {
		t.Fatalf("compliance = %v, want nil", v)
	}
}

func TestParseAccountTextButian(t *testing.T) {
	text := "实习白帽 LV1\n已加入补天0天\n未完成实战认证\n500\n荣誉币\n0\n库币余额\n0\n积分\n0\n已获奖金\n已提交 0\n待审核 0\n补天排名 -\n实名认证 已认证\n银行卡未绑定"
	a := parseAccountText(text)
	if a.Level != "实习白帽" {
		t.Fatalf("level = %q", a.Level)
	}
	if a.Practical == nil || *a.Practical {
		t.Fatalf("practical = %v, want false", a.Practical)
	}
	if a.Identity == nil || !*a.Identity {
		t.Fatalf("identity = %v, want true", a.Identity)
	}
	if a.BankBound == nil || *a.BankBound {
		t.Fatalf("bank = %v, want false", a.BankBound)
	}
	if a.HonorCoins != 500 {
		t.Fatalf("honor = %d", a.HonorCoins)
	}
}
