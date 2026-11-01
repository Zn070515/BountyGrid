package reliability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type fakeProposer struct {
	calls  int
	inputs []ProposeInput
	fn     func(in ProposeInput) ([]ActionProposal, error)
}

func (f *fakeProposer) Propose(_ context.Context, in ProposeInput) ([]ActionProposal, error) {
	f.calls++
	f.inputs = append(f.inputs, in)
	if f.fn != nil {
		return f.fn(in)
	}
	return nil, nil
}

type fakeExecutor struct {
	calls int
	got   []ActionProposal
	fn    func(p ActionProposal) (ExecutionResult, error)
}

func (f *fakeExecutor) Execute(_ context.Context, p ActionProposal) (ExecutionResult, error) {
	f.calls++
	f.got = append(f.got, p)
	if f.fn != nil {
		return f.fn(p)
	}
	return ExecutionResult{}, nil
}

func loopSession(t *testing.T) *ResearchSession {
	t.Helper()
	s := NewResearchSession("sess-loop", "task-loop", testNow)
	s.ProgramID = "prog-loop"
	s.Budget = Budget{MaxSteps: 10, MaxToolCalls: 10, MaxTokens: 100000}
	return s
}

func loopAuthorization() Authorization {
	return Authorization{
		AccountID:      "acct-loop",
		ProgramID:      "prog-loop",
		AllowedActions: []domain.Action{domain.ActionViewRules, domain.ActionPassiveAnalysis, domain.ActionReadScope},
		ExpiresAt:      testNow.Add(time.Hour),
	}
}

func testProposal(action domain.Action, confidence float64) ActionProposal {
	return ActionProposal{
		ID:         "proposal-" + string(action),
		Action:     action,
		Tool:       strings.ToLower(string(action)),
		Parameters: []byte(`{"asset":"asset-1"}`),
		Rationale:  "next step",
		Confidence: confidence,
	}
}

func fixedNow() time.Time { return testNow }

func TestLoopExecutesExactlyOneActionPerStep(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{
			testProposal(domain.ActionViewRules, 0.9),
			testProposal(domain.ActionPassiveAnalysis, 0.8),
			testProposal(domain.ActionReadScope, 0.7),
		}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := loopSession(t)
	result, err := loop.Run(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.StepsExecuted() != 1 || executor.calls != 1 || proposer.calls != 1 {
		t.Fatalf("executed=%d executor=%d proposer=%d, want one each",
			result.StepsExecuted(), executor.calls, proposer.calls)
	}
	if result.Stop != StopMaxSteps {
		t.Fatalf("stop = %s, want MAX_STEPS", result.Stop)
	}
}

func TestLoopFiltersDisallowedActions(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{
			testProposal(domain.ActionViewProgram, 0.99), // not in the authorization
			testProposal(domain.ActionViewRules, 0.10),
		}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(t.Context(), loopSession(t))
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.got[0].Action != domain.ActionViewRules {
		t.Fatalf("executed %s, want VIEW_RULES", executor.got[0].Action)
	}
	if result.Steps[0].Proposal.Action != domain.ActionViewRules {
		t.Fatalf("recorded action = %s", result.Steps[0].Proposal.Action)
	}
}

func TestLoopStopsAtMaxSteps(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.5)}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 3, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(t.Context(), loopSession(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopMaxSteps {
		t.Fatalf("stop = %s, want MAX_STEPS", result.Stop)
	}
	if result.StepsExecuted() != 3 || executor.calls != 3 {
		t.Fatalf("steps=%d calls=%d, want 3", result.StepsExecuted(), executor.calls)
	}
}

func TestLoopReplansFromNewObservation(t *testing.T) {
	var observedCounts []int
	var secondLast *Observation
	proposer := &fakeProposer{fn: func(in ProposeInput) ([]ActionProposal, error) {
		observedCounts = append(observedCounts, len(in.Session.RecentObservations))
		if len(observedCounts) == 2 {
			secondLast = in.LastObservation
		}
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.5)}, nil
	}}
	executor := &fakeExecutor{fn: func(p ActionProposal) (ExecutionResult, error) {
		return ExecutionResult{Observation: NewObservation(
			p.Tool, p.Parameters, "first response", "first observation", nil, testNow,
		)}, nil
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 2, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := loopSession(t)
	if _, err := loop.Run(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if len(observedCounts) != 2 {
		t.Fatalf("proposer calls = %d, want 2", len(observedCounts))
	}
	if observedCounts[0] != 0 {
		t.Fatalf("first proposal saw %d observations, want 0", observedCounts[0])
	}
	if observedCounts[1] != 1 {
		t.Fatalf("replan saw %d observations, want 1", observedCounts[1])
	}
	if secondLast == nil || secondLast.Content != "first observation" {
		t.Fatalf("replan did not receive the new observation: %+v", secondLast)
	}
	if len(s.Attempts) != 2 || len(s.RecentObservations) != 2 {
		t.Fatalf("session attempts=%d observations=%d", len(s.Attempts), len(s.RecentObservations))
	}
}

func TestLoopStopsWhenDetectorStalls(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.5)}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 20, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := loopSession(t)
	result, err := loop.Run(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopStalled {
		t.Fatalf("stop = %s, want STALLED", result.Stop)
	}
	if result.StallAction != ActionForceAlternative {
		t.Fatalf("stall action = %s, want FORCE_ALTERNATIVE", result.StallAction)
	}
	if result.StepsExecuted() != 5 {
		t.Fatalf("steps = %d, want 5", result.StepsExecuted())
	}
	if s.State != StateStalled {
		t.Fatalf("session state = %s, want STALLED", s.State)
	}
}

func TestLoopStopsWhenBudgetExhausted(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.5)}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 10, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := loopSession(t)
	s.Budget.MaxToolCalls = 1
	result, err := loop.Run(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopBudgetExhausted {
		t.Fatalf("stop = %s, want BUDGET_EXHAUSTED", result.Stop)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if s.State != StateParked {
		t.Fatalf("session state = %s, want PARKED", s.State)
	}
}

func TestLoopRejectsExpiredAuthorization(t *testing.T) {
	auth := loopAuthorization()
	auth.ExpiresAt = testNow.Add(-time.Minute)
	loop, err := NewLoop(LoopConfig{
		Proposer: &fakeProposer{}, Executor: &fakeExecutor{},
		Authorization: auth, MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(t.Context(), loopSession(t))
	if !errors.Is(err, ErrAuthorizationExpired) {
		t.Fatalf("want ErrAuthorizationExpired, got %v", err)
	}
}

func TestExecutorFailureIsObservedAndNotEchoed(t *testing.T) {
	sig := NewFailureSignature("view_rules", "INVALID_ARGUMENT", []byte(`{"password":"hunter2"}`))
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.5)}, nil
	}}
	executor := &fakeExecutor{fn: func(ActionProposal) (ExecutionResult, error) {
		return ExecutionResult{Failure: &sig}, errors.New("raw failure password=hunter2")
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor,
		Authorization: loopAuthorization(), MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := loopSession(t)
	result, err := loop.Run(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d", len(result.Steps))
	}
	step := result.Steps[0]
	if step.Failure == nil || step.Failure.ErrorClass != "INVALID_ARGUMENT" {
		t.Fatalf("failure = %+v", step.Failure)
	}
	if step.ErrClass != "INVALID_ARGUMENT" {
		t.Fatalf("error class = %q", step.ErrClass)
	}
	if len(s.Attempts) != 1 || s.Attempts[0].Failure == nil {
		t.Fatalf("attempt failure was not recorded")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("session invalid after failure: %v", err)
	}
	if strings.Contains(s.RecentObservations[0].Content, "hunter2") ||
		strings.Contains(s.Attempts[0].Failure.Key(), "hunter2") {
		t.Fatalf("raw failure text leaked into state")
	}
}

func TestRankProposalsFiltersHumanOnlyAndRequiresAsset(t *testing.T) {
	asset := domain.AssetID("asset-1")
	auth := Authorization{
		AccountID: "a", ProgramID: "p", AssetID: &asset,
		AllowedActions: []domain.Action{domain.ActionReportSubmit, domain.ActionViewRules, domain.ActionActiveHTTP},
	}
	noAsset := testProposal(domain.ActionActiveHTTP, 0.9)
	noAsset.AssetID = nil
	wrongAsset := testProposal(domain.ActionActiveHTTP, 0.8)
	wrong := domain.AssetID("other")
	wrongAsset.AssetID = &wrong
	rightAsset := testProposal(domain.ActionActiveHTTP, 0.7)
	rightAsset.AssetID = &asset
	human := testProposal(domain.ActionReportSubmit, 1)
	read := testProposal(domain.ActionViewRules, 0.1)

	ranked := RankProposals([]ActionProposal{human, noAsset, wrongAsset, rightAsset, read}, auth, Budget{}, testNow)
	var actions []domain.Action
	for _, p := range ranked {
		actions = append(actions, p.Action)
	}
	if len(ranked) != 2 {
		t.Fatalf("ranked %v, want exactly ACTIVE_HTTP and VIEW_RULES", actions)
	}
	for _, p := range ranked {
		if p.Action == domain.ActionReportSubmit {
			t.Fatal("human-only action was not filtered")
		}
	}
	if ranked[0].Action != domain.ActionActiveHTTP {
		t.Fatalf("first ranked = %s", ranked[0].Action)
	}
}

func TestProposalSecretIsRejected(t *testing.T) {
	p := testProposal(domain.ActionViewRules, 0.5)
	p.Parameters = []byte(`{"token":"abcdefgh12345678"}`)
	if err := p.Validate(); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("want ErrSecretDetected, got %v", err)
	}
}
