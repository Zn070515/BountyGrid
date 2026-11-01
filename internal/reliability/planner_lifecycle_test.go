package reliability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// capturingSessionStore keeps every checkpoint so a test can inspect the
// durable state while the executor is still blocked.
type capturingSessionStore struct {
	mu    sync.Mutex
	saved []*ResearchSession
}

func (s *capturingSessionStore) Save(_ context.Context, session *ResearchSession) error {
	copied := ResearchSession(session.Snapshot())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, &copied)
	return nil
}

func (s *capturingSessionStore) Load(context.Context, string) (*ResearchSession, error) {
	return nil, ErrNotFound
}

func (s *capturingSessionStore) List(context.Context) ([]string, error) { return nil, nil }

func (s *capturingSessionStore) last() *ResearchSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return nil
	}
	return s.saved[len(s.saved)-1]
}

func TestLoopPersistsStartedAttemptBeforeExecution(t *testing.T) {
	store := &capturingSessionStore{}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	executor := &fakeExecutor{fn: func(ActionProposal) (ExecutionResult, error) {
		entered <- struct{}{}
		<-release
		return ExecutionResult{Evidence: []domain.EvidenceID{"ev-1"}}, nil
	}}
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.9)}, nil
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 1, Store: store, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := loop.Run(context.Background(), loopSession(t))
		done <- err
	}()
	<-entered

	mid := store.last()
	if mid == nil || len(mid.Attempts) != 1 {
		t.Fatalf("mid-run checkpoint = %+v", mid)
	}
	if got := mid.Attempts[0].Status; got != AttemptStarted {
		t.Fatalf("attempt status before execution = %q, want STARTED", got)
	}
	if !mid.Attempts[0].FinishedAt.IsZero() || mid.Attempts[0].ObservationID != "" {
		t.Fatalf("STARTED attempt already has an outcome: %+v", mid.Attempts[0])
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final := store.last()
	if final == nil || len(final.Attempts) != 1 {
		t.Fatalf("final checkpoint = %+v", final)
	}
	if final.Attempts[0].Status != AttemptCompleted || final.Attempts[0].FinishedAt.IsZero() ||
		final.Attempts[0].ObservationID == "" {
		t.Fatalf("final attempt = %+v", final.Attempts[0])
	}
}

func TestInterruptedAttemptOnlyBlocksSameToolAndArgs(t *testing.T) {
	s := loopSession(t)
	blocked := testProposal(domain.ActionViewRules, 0.9)
	blocked.ID = "p-blocked"
	allowed := testProposal(domain.ActionViewRules, 0.8)
	allowed.ID = "p-allowed"
	allowed.Parameters = []byte(`{"asset":"asset-2"}`)
	s.Attempts = append(s.Attempts, Attempt{
		ID: "attempt-interrupted", Step: 1, ProposalID: blocked.ID,
		ToolName: proposalTool(blocked), Action: blocked.Action,
		ArgsHash: HashArgs(blocked.Parameters),
		Status:   AttemptInterrupted, StartedAt: testNow, FinishedAt: testNow,
	})

	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{blocked, allowed}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopMaxSteps || executor.calls != 1 {
		t.Fatalf("stop=%s executor calls=%d", result.Stop, executor.calls)
	}
	if got := executor.got[0].ID; got != "p-allowed" {
		t.Fatalf("executed %q; the interrupted tool+args must be skipped", got)
	}
}

func TestInterruptedOnlyProposalsStopTheLoop(t *testing.T) {
	s := loopSession(t)
	proposal := testProposal(domain.ActionViewRules, 0.9)
	s.Attempts = append(s.Attempts, Attempt{
		ID: "attempt-interrupted", Step: 1,
		ToolName: proposalTool(proposal), Action: proposal.Action,
		ArgsHash: HashArgs(proposal.Parameters),
		Status:   AttemptInterrupted, StartedAt: testNow, FinishedAt: testNow,
	})
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{proposal}, nil
	}}
	executor := &fakeExecutor{}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 1, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopNoAllowedProposals || executor.calls != 0 {
		t.Fatalf("stop=%s calls=%d, want NO_ALLOWED_PROPOSALS/0", result.Stop, executor.calls)
	}
}

func TestLoopRequiresStoreWhenConfigured(t *testing.T) {
	cfg := LoopConfig{
		Proposer: &fakeProposer{}, Executor: &fakeExecutor{},
		Authorization: loopAuthorization(), RequireStore: true,
	}
	if _, err := NewLoop(cfg); !errors.Is(err, ErrSessionStoreRequired) {
		t.Fatalf("err = %v, want ErrSessionStoreRequired", err)
	}
	cfg.Store = &capturingSessionStore{}
	if _, err := NewLoop(cfg); err != nil {
		t.Fatalf("loop with store rejected: %v", err)
	}
}

func TestRunInterruptsInFlightAttemptsFromACrashedRun(t *testing.T) {
	store := &capturingSessionStore{}
	s := loopSession(t)
	s.Attempts = []Attempt{{
		ID: "attempt-crashed", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:crashed",
		Status: AttemptStarted, StartedAt: testNow.Add(-time.Minute),
	}}
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return nil, nil
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: &fakeExecutor{}, Authorization: loopAuthorization(),
		MaxSteps: 1, Store: store, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.Attempts[0].Status != AttemptInterrupted {
		t.Fatalf("status = %q, want INTERRUPTED", s.Attempts[0].Status)
	}
	if !s.Attempts[0].FinishedAt.Equal(testNow) {
		t.Fatalf("finished_at = %s, want %s", s.Attempts[0].FinishedAt, testNow)
	}
	persisted := store.last()
	if persisted == nil || persisted.Attempts[0].Status != AttemptInterrupted {
		t.Fatalf("persisted attempt = %+v", persisted)
	}
}

func TestResumeInterruptsStartedAttempt(t *testing.T) {
	s := loopSession(t)
	s.State = StateStalled
	s.Attempts = []Attempt{{
		ID: "attempt-in-flight", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:x",
		Status: AttemptStarted, StartedAt: testNow,
	}}
	at := testNow.Add(time.Minute)
	if err := s.Resume(at); err != nil {
		t.Fatal(err)
	}
	if s.Attempts[0].Status != AttemptInterrupted || !s.Attempts[0].FinishedAt.Equal(at) {
		t.Fatalf("attempt after resume = %+v", s.Attempts[0])
	}
}

func TestSameProposalAcrossRunsGetsDistinctAttempts(t *testing.T) {
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.9)}, nil
	}}
	executor := &fakeExecutor{fn: func(ActionProposal) (ExecutionResult, error) {
		return ExecutionResult{Evidence: []domain.EvidenceID{"ev-1"}}, nil
	}}
	s := loopSession(t)
	s.Budget = Budget{MaxSteps: 10, MaxToolCalls: 10, MaxTokens: 100000}
	newLoop := func() *Loop {
		loop, err := NewLoop(LoopConfig{
			Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
			MaxSteps: 1, Now: fixedNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		return loop
	}
	if _, err := newLoop().Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	firstID := s.Attempts[0].ID
	firstObservation := s.Attempts[0].ObservationID
	if _, err := newLoop().Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(s.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(s.Attempts))
	}
	if s.Attempts[0].ID == s.Attempts[1].ID {
		t.Fatalf("attempt id collided across runs: %s", firstID)
	}
	if s.Attempts[0].ObservationID != firstObservation {
		t.Fatalf("second run overwrote the first attempt's observation: %q -> %q",
			firstObservation, s.Attempts[0].ObservationID)
	}
	for i, a := range s.Attempts {
		if a.Status != AttemptCompleted || a.ObservationID == "" {
			t.Fatalf("attempt %d = %+v", i, a)
		}
	}
}

func TestAddAttemptRejectsDuplicateID(t *testing.T) {
	s := loopSession(t)
	a := Attempt{
		ID: "attempt-dup", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:x",
		Status: AttemptCompleted, StartedAt: testNow, FinishedAt: testNow,
	}
	if err := s.AddAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAttempt(a); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("duplicate attempt err = %v, want ErrInvalidClaim", err)
	}
}

func TestReplaceAttemptRefusesTerminalAttempt(t *testing.T) {
	s := loopSession(t)
	a := Attempt{
		ID: "attempt-done", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:x",
		Status: AttemptCompleted, StartedAt: testNow, FinishedAt: testNow,
	}
	if err := s.AddAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAttempt(a); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("replace terminal attempt err = %v, want ErrInvalidClaim", err)
	}
}

func TestSessionValidateRejectsDuplicateAttemptIDs(t *testing.T) {
	s := loopSession(t)
	a := Attempt{
		ID: "attempt-dup", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:x",
		Status: AttemptCompleted, StartedAt: testNow, FinishedAt: testNow,
	}
	// Bypass AddAttempt to simulate a corrupt/legacy persisted session.
	s.Attempts = []Attempt{a, a}
	if err := s.Validate(); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("validate err = %v, want ErrInvalidClaim", err)
	}
}

func TestReplaceAttemptCannotRewriteIdentity(t *testing.T) {
	s := loopSession(t)
	started := Attempt{
		ID: "attempt-fixed", Step: 1, ToolName: "view_rules",
		Action: domain.ActionViewRules, ArgsHash: "sha256:x",
		Status: AttemptStarted, StartedAt: testNow,
	}
	if err := s.AddAttempt(started); err != nil {
		t.Fatal(err)
	}
	changed := started
	changed.ToolName = "read_scope"
	changed.Status = AttemptCompleted
	changed.FinishedAt = testNow
	if err := s.ReplaceAttempt(changed); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("identity rewrite err = %v, want ErrInvalidClaim", err)
	}
	missing := started
	missing.ID = "attempt-missing"
	missing.Status = AttemptCompleted
	missing.FinishedAt = testNow
	if err := s.ReplaceAttempt(missing); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("missing attempt err = %v, want ErrInvalidClaim", err)
	}
}
