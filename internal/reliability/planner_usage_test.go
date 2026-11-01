package reliability

import (
	"context"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type usageProposer struct {
	proposals []ActionProposal
	usage     ProposerUsage
	calls     int
}

func (p *usageProposer) Propose(ctx context.Context, in ProposeInput) ([]ActionProposal, error) {
	proposals, _, err := p.ProposeWithUsage(ctx, in)
	return proposals, err
}

func (p *usageProposer) ProposeWithUsage(context.Context, ProposeInput) ([]ActionProposal, ProposerUsage, error) {
	p.calls++
	return p.proposals, p.usage, nil
}

func TestLoopChargesProposerUsageAndStopsBeforeOverspend(t *testing.T) {
	proposer := &usageProposer{
		proposals: []ActionProposal{testProposal(domain.ActionViewRules, 0.9)},
		usage:     ProposerUsage{InputTokens: 50, OutputTokens: 10},
	}
	executor := &fakeExecutor{}
	s := loopSession(t)
	s.Budget = Budget{MaxSteps: 5, MaxToolCalls: 5, MaxTokens: 100}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 5, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	// Step 1 charges 60 and executes; step 2 charges another 60, crosses the
	// 100-token ceiling, and must not execute its proposal.
	if result.Stop != StopBudgetExhausted {
		t.Fatalf("stop = %s, want BUDGET_EXHAUSTED", result.Stop)
	}
	if proposer.calls != 2 || executor.calls != 1 || len(result.Steps) != 1 {
		t.Fatalf("proposer=%d executor=%d steps=%d, want 2/1/1",
			proposer.calls, executor.calls, len(result.Steps))
	}
	if s.Budget.TokensUsed != 120 {
		t.Fatalf("tokens used = %d, want 120", s.Budget.TokensUsed)
	}
	if s.Budget.StepsUsed != 1 {
		t.Fatalf("steps used = %d, want 1", s.Budget.StepsUsed)
	}
	if s.State != StateParked {
		t.Fatalf("state = %s, want PARKED", s.State)
	}
	if got := result.Steps[0].Usage.Total(); got != 60 {
		t.Fatalf("step usage = %d, want 60", got)
	}
}

func TestLoopUsageIsUnboundedWhenMaxTokensIsZero(t *testing.T) {
	proposer := &usageProposer{
		proposals: []ActionProposal{testProposal(domain.ActionViewRules, 0.9)},
		usage:     ProposerUsage{InputTokens: 50, OutputTokens: 10},
	}
	executor := &fakeExecutor{}
	s := loopSession(t)
	s.Budget = Budget{MaxSteps: 2, MaxToolCalls: 2}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 2, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stop != StopMaxSteps || executor.calls != 2 {
		t.Fatalf("stop=%s executor=%d, want MAX_STEPS/2", result.Stop, executor.calls)
	}
	if s.Budget.TokensUsed != 120 {
		t.Fatalf("tokens used = %d, want 120", s.Budget.TokensUsed)
	}
}

func TestProposerUsageIgnoresNegativeAndReportsTotal(t *testing.T) {
	if got := (ProposerUsage{InputTokens: -1, OutputTokens: 5}).Total(); got != 0 {
		t.Fatalf("negative usage total = %d, want 0", got)
	}
	if got := (ProposerUsage{InputTokens: 3, OutputTokens: 4}).Total(); got != 7 {
		t.Fatalf("usage total = %d, want 7", got)
	}
}
