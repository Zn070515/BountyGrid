package butian

import (
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
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
	if prog.Eligibility.Decision != domain.HumanActionNeeded {
		t.Fatalf("eligibility = %s", prog.Eligibility.Decision)
	}
	if len(prog.Eligibility.Gates) != 2 {
		t.Fatalf("gates = %+v", prog.Eligibility.Gates)
	}
	if prog.Eligibility.Gates[1].Kind != domain.GatePracticalCertification || prog.Eligibility.Gates[1].ProgramID == nil {
		t.Fatalf("practical gate is not program-scoped: %+v", prog.Eligibility.Gates[1])
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

func TestParseCompliance(t *testing.T) {
	if v := parseCompliance("本季度合规考核未完成，请先完成考核"); v == nil || *v {
		t.Fatalf("compliance = %v, want false", v)
	}
	if v := parseCompliance("合规考核已完成"); v == nil || !*v {
		t.Fatalf("compliance = %v, want true", v)
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
