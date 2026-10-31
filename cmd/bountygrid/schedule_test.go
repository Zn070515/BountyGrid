package main

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

var testNow = time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)

type fakeScheduleStore struct {
	programs []domain.Program
	states   []domain.DerivedState
	account  *domain.Account
	opps     []domain.Opportunity
	tasks    []domain.ResearchTask
	comp     *domain.CompetitionSnapshot
}

func (f *fakeScheduleStore) ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error) {
	return f.programs, nil
}
func (f *fakeScheduleStore) ListStates(ctx context.Context, platform domain.Platform, subject domain.StateSubject) ([]domain.DerivedState, error) {
	return f.states, nil
}
func (f *fakeScheduleStore) GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error) {
	if f.account == nil {
		return domain.Account{}, postgres.ErrNotFound
	}
	return *f.account, nil
}
func (f *fakeScheduleStore) GetLatestCompetitionSnapshot(ctx context.Context, platform domain.Platform, scope string) (domain.CompetitionSnapshot, error) {
	if f.comp == nil {
		return domain.CompetitionSnapshot{}, postgres.ErrNotFound
	}
	return *f.comp, nil
}
func (f *fakeScheduleStore) PutOpportunity(ctx context.Context, o domain.Opportunity) error {
	f.opps = append(f.opps, o)
	return nil
}
func (f *fakeScheduleStore) PutResearchTask(ctx context.Context, t domain.ResearchTask) error {
	f.tasks = append(f.tasks, t)
	return nil
}

func schedProgram(id string, status domain.ProgramStatus, eligible bool, scopeGranted bool) domain.Program {
	min, max := int64(1000), int64(10000)
	p := domain.Program{
		ID: domain.ProgramID(id), Platform: domain.PlatformVulbox, Name: id, Type: domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: status, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test"},
		Scope:       domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: testNow},
		Reward:      domain.RewardPolicy{Min: &domain.Money{Amount: min, Currency: "CNY"}, Max: &domain.Money{Amount: max, Currency: "CNY"}, TaxBasis: domain.TaxUnknown},
		ObservedAt:  testNow,
	}
	if !eligible {
		p.Eligibility.Decision = domain.HumanActionNeeded
		p.Eligibility.Gates = []domain.EligibilityGate{{
			Kind: domain.GateProgramSignup, Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true,
		}}
	}
	if !scopeGranted {
		p.Scope.Access = domain.ScopeAccessNotGranted
		p.Scope.ContentsKnown = false
	}
	return p
}

func schedState(p domain.Program, status string) domain.DerivedState {
	pid := p.ID
	return domain.DerivedState{
		ID: domain.StateID("state/" + string(p.ID) + "/program_status"), Subject: domain.StateProgramState,
		Platform: p.Platform, ProgramID: &pid, State: status,
		Inputs: []domain.FactID{"f1"}, ObservedAt: testNow, DerivedAt: testNow,
		ValidUntil: testNow.Add(time.Hour), Confidence: 1, RuleVersion: "test",
	}
}

func TestExecuteSchedule(t *testing.T) {
	p1 := schedProgram("release", domain.StatusRunning, true, true)
	p2 := schedProgram("gate", domain.StatusRunning, false, true)
	p3 := schedProgram("paused", domain.StatusPaused, true, true)
	p4 := schedProgram("nostate", domain.StatusRunning, true, true)
	acct := domain.Account{ID: "vulbox/account", Platform: domain.PlatformVulbox, ObservedAt: testNow,
		Capability: domain.AccountCapability{CanSubmit: true, EvaluatedAt: testNow}}
	store := &fakeScheduleStore{
		programs: []domain.Program{p1, p2, p3, p4},
		states:   []domain.DerivedState{schedState(p1, "RUNNING"), schedState(p2, "RUNNING"), schedState(p3, "PAUSED")},
		account:  &acct,
	}
	out, err := executeSchedule(context.Background(), store, scheduleOptions{Platform: "vulbox", Top: 5, Persist: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Research != 1 || out.Human != 1 || out.NoState != 1 {
		t.Fatalf("out = %+v", out)
	}
	if len(out.ResearchQueue) != 1 || out.ResearchQueue[0].ProgramID != "release" {
		t.Fatalf("research = %+v", out.ResearchQueue)
	}
	if out.ExcludedReasons["PROGRAM_NOT_RUNNING"] != 1 || out.ExcludedReasons["NO_STATE"] != 1 {
		t.Fatalf("excluded = %+v", out.ExcludedReasons)
	}
	if len(store.opps) != 2 || len(store.tasks) != 2 {
		t.Fatalf("persisted = %d opps / %d tasks", len(store.opps), len(store.tasks))
	}
	statuses := map[string]bool{}
	for _, task := range store.tasks {
		statuses[task.Status] = true
	}
	if !statuses["QUEUED"] || !statuses["HUMAN_ACTION_REQUIRED"] {
		t.Fatalf("task statuses = %+v", statuses)
	}
}

func TestExecuteScheduleUsesCompetitionSnapshot(t *testing.T) {
	p := schedProgram("p1", domain.StatusRunning, true, true)
	top10 := 0.708
	dup := 0.5
	sat := 0.6
	comp := &domain.CompetitionSnapshot{
		ID: "comp", Platform: domain.PlatformVulbox, Scope: "PLATFORM", ObservedAt: testNow,
		Top10Share: &top10, DuplicatePressure: &dup, Saturation: &sat, Confidence: 0.7,
	}
	acct := domain.Account{ID: "vulbox/account", Platform: domain.PlatformVulbox, ObservedAt: testNow,
		Capability: domain.AccountCapability{CanSubmit: true, EvaluatedAt: testNow}}
	store := &fakeScheduleStore{
		programs: []domain.Program{p},
		states:   []domain.DerivedState{schedState(p, "RUNNING")},
		account:  &acct,
		comp:     comp,
	}
	out, err := executeSchedule(context.Background(), store, scheduleOptions{Platform: "vulbox", Top: 1}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ResearchQueue) != 1 {
		t.Fatalf("research = %+v", out.ResearchQueue)
	}
	if out.ResearchQueue[0].Confidence != 0.7 {
		t.Fatalf("confidence = %v, want snapshot 0.7", out.ResearchQueue[0].Confidence)
	}
	if out.ResearchQueue[0].CompetitionPenalty <= 0 {
		t.Fatal("competition penalty missing")
	}
}

func TestExecuteScheduleMissingAccountFailsClosed(t *testing.T) {
	p := schedProgram("p1", domain.StatusRunning, true, true)
	store := &fakeScheduleStore{
		programs: []domain.Program{p},
		states:   []domain.DerivedState{schedState(p, "RUNNING")},
	}
	out, err := executeSchedule(context.Background(), store, scheduleOptions{Platform: "vulbox", Top: 5}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Research != 0 || out.Human != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.HumanQueue[0].Reason != "ACCOUNT_CAPABILITY" {
		t.Fatalf("reason = %s", out.HumanQueue[0].Reason)
	}
}

func TestParseScheduleOptions(t *testing.T) {
	opts, err := parseScheduleOptions([]string{"-platform", "xianzhi", "-top", "3", "-persist"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Platform != "xianzhi" || opts.Top != 3 || !opts.Persist {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseScheduleOptions([]string{"-top", "0"}); err == nil {
		t.Fatal("zero top should fail")
	}
}
