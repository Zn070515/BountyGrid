package opportunity

import (
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func testAccount(canSubmit bool) domain.Account {
	return domain.Account{
		ID: "acc1", Platform: domain.PlatformButian, ObservedAt: testNow,
		Capability: domain.AccountCapability{CanSubmit: canSubmit, EvaluatedAt: testNow},
	}
}

func candidate(id string, status domain.ProgramStatus, eligible bool, scopeGranted, canSubmit bool, max int64, typ domain.ProgramType) Candidate {
	p := scoringProgram(id, 1000, max, typ)
	p.Status.Status = status
	if eligible {
		p.Eligibility.Decision = domain.Eligible
	} else {
		p.Eligibility.Decision = domain.HumanActionNeeded
		p.Eligibility.Gates = []domain.EligibilityGate{{
			Kind: domain.GateProgramSignup, Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true,
		}}
	}
	if scopeGranted {
		p.Scope.Access = domain.ScopeAccessGranted
		p.Scope.ContentsKnown = true
	} else {
		p.Scope.Access = domain.ScopeAccessNotGranted
		p.Scope.ContentsKnown = false
	}
	return Candidate{Program: p, Status: status, Account: testAccount(canSubmit), Competition: testCompetition(0.2, 0.2, 0.2, 0.9)}
}

func TestScheduleHardFilters(t *testing.T) {
	cands := []Candidate{
		candidate("paused", domain.StatusPaused, true, true, true, 10000, domain.TypeBrandSRC),
		candidate("gate", domain.StatusRunning, false, true, true, 10000, domain.TypeBrandSRC),
		candidate("scope", domain.StatusRunning, true, false, true, 10000, domain.TypeBrandSRC),
		candidate("account", domain.StatusRunning, true, true, false, 10000, domain.TypeBrandSRC),
		candidate("ready", domain.StatusRunning, true, true, true, 10000, domain.TypeBrandSRC),
	}
	res := Schedule(NewEngine(), cands, Options{TopK: 5, Now: testNow, MaxPerPlatform: 5})
	if res.ResearchCount != 1 || res.HumanCount != 3 || res.ExcludedCount != 1 {
		t.Fatalf("result = research %d human %d excluded %d", res.ResearchCount, res.HumanCount, res.ExcludedCount)
	}
	if res.ResearchQueue[0].Program.ID != "ready" || res.ResearchQueue[0].Reason != "READY" {
		t.Fatalf("research queue = %+v", res.ResearchQueue)
	}
	reasons := map[string]bool{}
	for _, h := range res.HumanQueue {
		reasons[h.Reason] = true
	}
	for _, want := range []string{"PROGRAM_GATE", "SCOPE_GATED", "ACCOUNT_CAPABILITY"} {
		if !reasons[want] {
			t.Fatalf("missing human reason %s: %+v", want, res.HumanQueue)
		}
	}
}

func TestScheduleQuotas(t *testing.T) {
	var cands []Candidate
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		cands = append(cands, candidate(id, domain.StatusRunning, true, true, true, 10000, domain.TypeBrandSRC))
	}
	res := Schedule(NewEngine(), cands, Options{TopK: 2, MaxPerPlatform: 1, Now: testNow})
	if res.ResearchCount != 1 {
		t.Fatalf("research = %d, want 1 (platform quota)", res.ResearchCount)
	}
}

func TestScheduleEnterpriseReserve(t *testing.T) {
	cands := []Candidate{
		candidate("b1", domain.StatusRunning, true, true, true, 100000, domain.TypeBrandSRC),
		candidate("b2", domain.StatusRunning, true, true, true, 90000, domain.TypeBrandSRC),
		candidate("e1", domain.StatusRunning, true, true, true, 1000, domain.TypeEnterpriseSRC),
	}
	res := Schedule(NewEngine(), cands, Options{TopK: 2, MaxPerPlatform: 5, ReserveEnterprise: true, Now: testNow})
	if res.ResearchCount != 2 {
		t.Fatalf("research = %d", res.ResearchCount)
	}
	found := false
	for _, s := range res.ResearchQueue {
		if s.Program.Type == domain.TypeEnterpriseSRC {
			found = true
		}
	}
	if !found {
		t.Fatalf("enterprise reserve missing: %+v", res.ResearchQueue)
	}
}

func TestScheduleCooldown(t *testing.T) {
	c := candidate("p1", domain.StatusRunning, true, true, true, 10000, domain.TypeBrandSRC)
	res := Schedule(NewEngine(), []Candidate{c}, Options{TopK: 1, Now: testNow, Existing: map[domain.ProgramID]time.Time{"p1": testNow.Add(-time.Hour)}})
	if res.ExcludedCount != 1 || res.Excluded[0].Reason != "COOLDOWN" {
		t.Fatalf("result = %+v", res.Excluded)
	}
}

func TestScheduleSortsByScore(t *testing.T) {
	big := candidate("big", domain.StatusRunning, true, true, true, 100000, domain.TypeBrandSRC)
	big.Competition = testCompetition(1, 0.5, 1, 0.9)
	small := candidate("small", domain.StatusRunning, true, true, true, 10000, domain.TypeBrandSRC)
	small.Program.Reward.Min = money(8000)
	small.Competition = testCompetition(0, 0, 0, 0.9)
	res := Schedule(NewEngine(), []Candidate{big, small}, Options{TopK: 2, MaxPerPlatform: 5, Now: testNow})
	if len(res.ResearchQueue) != 2 || res.ResearchQueue[0].Program.ID != "small" {
		t.Fatalf("queue = %+v", res.ResearchQueue)
	}
}
