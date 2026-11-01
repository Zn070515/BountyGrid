package opportunity

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)

func money(v int64) *domain.Money { return &domain.Money{Amount: v, Currency: "CNY"} }
func ptrFloat(v float64) *float64 { return &v }

func scoringProgram(id string, min, max int64, typ domain.ProgramType) domain.Program {
	automation := domain.AutomationPolicy{
		AIAnalysis: domain.CapUnknown, AutonomousAgent: domain.CapUnknown, ActiveHTTP: domain.CapUnknown,
		BrowserAutomation: domain.CapUnknown, AutomatedScanning: domain.CapUnknown,
		DirectoryEnumeration: domain.CapUnknown, SubdomainDiscovery: domain.CapUnknown,
		HighConcurrency: domain.CapUnknown, BruteForce: domain.CapUnknown, CredentialStuffing: domain.CapUnknown,
		FileUpload: domain.CapUnknown, StateMutation: domain.CapUnknown, RCEVerification: domain.CapUnknown,
		SensitiveDataRead: domain.CapUnknown, InternalNetwork: domain.CapUnknown,
		LateralMovement: domain.CapUnknown, DoS: domain.CapUnknown,
	}
	return domain.Program{
		ID: domain.ProgramID(id), Platform: domain.PlatformButian, Name: id, Type: typ,
		Status:      domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test"},
		Scope:       domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: testNow},
		Reward:      domain.RewardPolicy{Min: money(min), Max: money(max), TaxBasis: domain.TaxUnknown},
		Automation:  automation,
		ObservedAt:  testNow,
	}
}

func testCompetition(top10, dup, sat float64, conf domain.Confidence) *domain.CompetitionSnapshot {
	return &domain.CompetitionSnapshot{
		ID: "c1", Platform: domain.PlatformButian, Scope: "PLATFORM", ObservedAt: testNow,
		Top10Share: ptrFloat(top10), DuplicatePressure: ptrFloat(dup), Saturation: ptrFloat(sat),
		Confidence: conf,
	}
}

func TestEngineMissingCompetitionLowersConfidence(t *testing.T) {
	opp, err := NewEngine().Evaluate(Input{Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC), Status: domain.StatusRunning, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if opp.Confidence != 0.2 {
		t.Fatalf("confidence = %v, want 0.2", opp.Confidence)
	}
	if opp.CompetitionPenalty <= 0 {
		t.Fatal("missing competition should carry a conservative penalty")
	}
	found := false
	for _, s := range opp.Inputs {
		if len(s) > 10 && s[:10] == "confidence" {
			found = true
		}
	}
	if !found {
		t.Fatalf("inputs not explainable: %v", opp.Inputs)
	}
}

func TestEngineMissingTop10ShareLowersConfidence(t *testing.T) {
	comp := &domain.CompetitionSnapshot{
		ID: "c1", Platform: domain.PlatformButian, Scope: "PLATFORM", ObservedAt: testNow,
		DuplicatePressure: ptrFloat(0.2), Saturation: ptrFloat(0.2), Confidence: 1,
	}
	opp, err := NewEngine().Evaluate(Input{Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC), Competition: comp, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if opp.Confidence != missingShareConf {
		t.Fatalf("confidence = %v, want capped %v", opp.Confidence, missingShareConf)
	}
}

func TestEngineNoReward(t *testing.T) {
	p := scoringProgram("p1", 1, 2, domain.TypeBrandSRC)
	p.Reward.Min, p.Reward.Max = nil, nil
	if _, err := NewEngine().Evaluate(Input{Program: p, Status: domain.StatusRunning, Now: testNow}); !errors.Is(err, ErrNoReward) {
		t.Fatalf("err = %v, want ErrNoReward", err)
	}
}

func TestEngineDoesNotSortByMaxBounty(t *testing.T) {
	e := NewEngine()
	big := scoringProgram("big", 1000, 100000, domain.TypeBrandSRC)
	small := scoringProgram("small", 8000, 10000, domain.TypeBrandSRC)
	bigOpp, err := e.Evaluate(Input{Program: big, Status: domain.StatusRunning, Competition: testCompetition(1, 0.5, 1, 0.9), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	smallOpp, err := e.Evaluate(Input{Program: small, Status: domain.StatusRunning, Competition: testCompetition(0, 0, 0, 0.9), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if *smallOpp.ExpectedReturnPerCost <= *bigOpp.ExpectedReturnPerCost {
		t.Fatalf("small max bounty should outrank the concentrated big one: big=%v small=%v", *bigOpp.ExpectedReturnPerCost, *smallOpp.ExpectedReturnPerCost)
	}
}

func TestEngineOpportunityIDsAreAppendOnly(t *testing.T) {
	e := NewEngine()
	p := scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC)
	first, err := e.Evaluate(Input{Program: p, Status: domain.StatusRunning, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Evaluate(Input{Program: p, Status: domain.StatusRunning, Now: testNow.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("two evaluations share an ID: %s", first.ID)
	}
}

func TestEngineDropsExpiredCompetition(t *testing.T) {
	e := NewEngine()
	comp := testCompetition(0.1, 0.1, 0.1, 1)
	comp.ObservedAt = testNow.Add(-31 * 24 * time.Hour)
	comp.ValidUntil = testNow.Add(-time.Hour)
	opp, err := e.Evaluate(Input{
		Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC),
		Status:  domain.StatusRunning, Competition: comp, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opp.Confidence != e.NoDataConf {
		t.Fatalf("expired competition confidence = %v, want no-data %v", opp.Confidence, e.NoDataConf)
	}
	found := false
	for _, s := range opp.Inputs {
		if s == "competition=expired_or_stale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expiry not explained: %v", opp.Inputs)
	}
}

func TestEngineDecaysOldCompetitionConfidence(t *testing.T) {
	e := NewEngine()
	comp := testCompetition(0.5, 0.5, 0.5, 0.8)
	comp.ObservedAt = testNow.Add(-15 * 24 * time.Hour)
	opp, err := e.Evaluate(Input{
		Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC),
		Status:  domain.StatusRunning, Competition: comp, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opp.Confidence >= comp.Confidence || opp.Confidence <= 0 {
		t.Fatalf("15-day-old competition confidence = %v, want between 0 and %v", opp.Confidence, comp.Confidence)
	}
}

func TestEngineCompetitionSamples(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		name string
		prog domain.Program
		comp *domain.CompetitionSnapshot
	}{
		{"xianzhi", scoringProgram("xianzhi", 300, 3900, domain.TypeBrandSRC), testCompetition(0.708, 0.5, 0.6, 0.7)},
		{"vulbox", scoringProgram("vulbox", 2500, 9276, domain.TypeEnterpriseSRC), testCompetition(0.9, 0.7, 0.8, 0.7)},
		{"butian", scoringProgram("butian", 5980, 100000, domain.TypeEnterpriseSRC), testCompetition(0.8, 0.6, 0.9, 0.5)},
	}
	for _, c := range cases {
		opp, err := e.Evaluate(Input{Program: c.prog, Status: domain.StatusRunning, Competition: c.comp, Now: testNow})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if opp.CompetitionPenalty <= 0 || opp.CompetitionPenalty >= 1 {
			t.Fatalf("%s penalty = %v", c.name, opp.CompetitionPenalty)
		}
		if opp.Confidence != c.comp.Confidence {
			t.Fatalf("%s confidence = %v", c.name, opp.Confidence)
		}
	}
}

func BenchmarkEvaluate(b *testing.B) {
	e := NewEngine()
	p := scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC)
	comp := testCompetition(0.5, 0.5, 0.5, 0.7)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := e.Evaluate(Input{Program: p, Status: domain.StatusRunning, Competition: comp, Now: testNow}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestHistoryLowersScoreForLowAcceptance(t *testing.T) {
	engine := NewEngine()
	base := Input{Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC),
		Status: domain.StatusRunning, Competition: testCompetition(0.5, 0.5, 0.5, 0.8), Now: testNow}
	without, err := engine.Evaluate(base)
	if err != nil {
		t.Fatal(err)
	}
	base.History = &History{Submitted: 20, Accepted: 2, Paid: 2,
		AvgPaid: domain.Money{Amount: 500, Currency: "CNY"}}
	with, err := engine.Evaluate(base)
	if err != nil {
		t.Fatal(err)
	}
	if *with.ExpectedReturnPerCost >= *without.ExpectedReturnPerCost {
		t.Fatalf("history did not lower score: with=%v without=%v",
			*with.ExpectedReturnPerCost, *without.ExpectedReturnPerCost)
	}
	joined := ""
	for _, s := range with.Inputs {
		joined += s + " "
	}
	if !strings.Contains(joined, "history_submitted=20") || !strings.Contains(joined, "history_accepted=2") {
		t.Fatalf("history inputs missing: %v", with.Inputs)
	}
}

func TestHistoryUsesActualCost(t *testing.T) {
	engine := NewEngine()
	in := Input{Program: scoringProgram("p1", 1000, 10000, domain.TypeBrandSRC),
		Status: domain.StatusRunning, Competition: testCompetition(0.5, 0.5, 0.5, 0.8), Now: testNow}
	cheap, err := engine.Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	in.History = &History{ActualCost: domain.Money{Amount: 100000, Currency: "CNY"}}
	expensive, err := engine.Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if *expensive.ExpectedReturnPerCost >= *cheap.ExpectedReturnPerCost {
		t.Fatal("actual cost did not lower the score")
	}
}
