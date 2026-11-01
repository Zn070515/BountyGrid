package reliability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

type fakeModelRouter struct {
	request  model.ModelRequest
	response model.ModelResponse
	err      error
	calls    int
}

func (f *fakeModelRouter) Route(_ context.Context, req model.ModelRequest) (model.ModelResponse, error) {
	f.calls++
	f.request = req
	return f.response, f.err
}

func modelProposeInput() ProposeInput {
	return ProposeInput{
		Session: SessionSnapshot{ID: "sess-1", TaskID: "task-1"},
		Context: CompiledContext{Text: "TASK\nconfirm scope\nAVAILABLE ACTIONS\n- READ_SCOPE"},
		Step:    2,
	}
}

const validProposalJSON = `{"action":"READ_SCOPE","tool":"read_scope","parameters":{},"rationale":"check scope","confidence":0.9}`

func TestModelProposerRoutesAndReturnsUsage(t *testing.T) {
	router := &fakeModelRouter{response: model.ModelResponse{
		Content: validProposalJSON,
		Usage:   model.Usage{InputTokens: 30, OutputTokens: 12},
		Model:   "local-small", Provider: "static",
		Cost: domain.Money{Amount: 7, Currency: "CNY"},
	}}
	proposer, err := NewModelProposer(ModelProposerConfig{
		Router: router, ProgramID: "p1", TaskID: "task-1", MaxTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposals, usage, err := proposer.ProposeWithUsage(context.Background(), modelProposeInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || proposals[0].Action != domain.ActionReadScope || proposals[0].Tool != "read_scope" {
		t.Fatalf("proposals = %+v", proposals)
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 12 || usage.Total() != 42 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.Cost.Amount != 7 || usage.Cost.Currency != "CNY" || usage.Model != "local-small" {
		t.Fatalf("cost/model = %+v", usage)
	}
	if usage.CallID == "" {
		t.Fatal("stable call id was not recorded on the step usage")
	}

	if router.calls != 1 {
		t.Fatalf("router calls = %d, want 1", router.calls)
	}
	req := router.request
	if req.Purpose != model.PurposePlan || req.DataClass != model.DataInternal {
		t.Fatalf("purpose/data class = %s/%s", req.Purpose, req.DataClass)
	}
	if req.ProgramID != "p1" || req.TaskID != "task-1" || req.StepID != "step-2" || req.MaxTokens != 128 {
		t.Fatalf("request identity = %+v", req)
	}
	if len(req.Input) != 2 || req.Input[0].Role != "system" || req.Input[1].Role != "user" {
		t.Fatalf("messages = %+v", req.Input)
	}
	if !strings.Contains(req.Input[1].Content, "confirm scope") {
		t.Fatalf("compiled context missing from prompt: %q", req.Input[1].Content)
	}
	wantCallID := model.StableCallID("task-1", "step-2", model.PurposePlan, model.PromptHash(req))
	if usage.CallID != wantCallID || req.CallID != wantCallID {
		t.Fatalf("call id = %q, want %q", usage.CallID, wantCallID)
	}
}

func TestModelProposerUsesSessionTaskIDAndCanDisableStableCallID(t *testing.T) {
	router := &fakeModelRouter{response: model.ModelResponse{Content: validProposalJSON}}
	proposer, err := NewModelProposer(ModelProposerConfig{
		Router: router, ProgramID: "p1", MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposer.ProposeWithUsage(context.Background(), modelProposeInput()); err != nil {
		t.Fatal(err)
	}
	if router.request.TaskID != "task-1" || router.request.CallID == "" {
		t.Fatalf("request = %+v", router.request)
	}

	router = &fakeModelRouter{response: model.ModelResponse{Content: validProposalJSON}}
	proposer, err = NewModelProposer(ModelProposerConfig{
		Router: router, ProgramID: "p1", MaxTokens: 64, DisableStableCallID: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposer.ProposeWithUsage(context.Background(), modelProposeInput()); err != nil {
		t.Fatal(err)
	}
	if router.request.CallID != "" {
		t.Fatalf("call id = %q, want empty", router.request.CallID)
	}
}

func TestModelProposerMalformedOutputIsNoProposal(t *testing.T) {
	router := &fakeModelRouter{response: model.ModelResponse{
		Content: "I cannot do that.",
		Usage:   model.Usage{InputTokens: 5, OutputTokens: 3},
	}}
	proposer, err := NewModelProposer(ModelProposerConfig{Router: router, ProgramID: "p1", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	proposals, usage, err := proposer.ProposeWithUsage(context.Background(), modelProposeInput())
	if err != nil {
		t.Fatalf("malformed output must be no-proposal, got error: %v", err)
	}
	if len(proposals) != 0 || usage.Total() != 8 {
		t.Fatalf("proposals=%+v usage=%+v", proposals, usage)
	}
}

func TestModelProposerPropagatesRouterError(t *testing.T) {
	boom := errors.New("no provider")
	router := &fakeModelRouter{err: boom}
	proposer, err := NewModelProposer(ModelProposerConfig{Router: router, ProgramID: "p1", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposer.ProposeWithUsage(context.Background(), modelProposeInput()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestNewModelProposerValidatesConfig(t *testing.T) {
	if _, err := NewModelProposer(ModelProposerConfig{}); err == nil {
		t.Fatal("nil router accepted")
	}
	if _, err := NewModelProposer(ModelProposerConfig{Router: &fakeModelRouter{}, DataClass: "BOGUS"}); err == nil {
		t.Fatal("invalid data class accepted")
	}
	if _, err := NewModelProposer(ModelProposerConfig{Router: &fakeModelRouter{}, MaxTokens: -1}); err == nil {
		t.Fatal("negative max tokens accepted")
	}
}

// The joint-budget test: a loop driven by the Router-backed proposer charges
// the provider's token usage to the session and records the ModelCall linkage
// (CallID/model) and actual cost on the executed step.
func TestLoopRecordsModelProposerUsageAndCallLink(t *testing.T) {
	router := &fakeModelRouter{response: model.ModelResponse{
		Content: validProposalJSON,
		Usage:   model.Usage{InputTokens: 11, OutputTokens: 4},
		Model:   "local-small", Provider: "static",
		Cost: domain.Money{Amount: 3, Currency: "CNY"},
	}}
	proposer, err := NewModelProposer(ModelProposerConfig{
		Router: router, ProgramID: "prog-loop", MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := loopSession(t)
	session.Budget = Budget{MaxSteps: 1, MaxToolCalls: 1, MaxTokens: 100}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: &fakeExecutor{}, Authorization: loopAuthorization(),
		MaxSteps: 1, AllowEphemeral: true, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if session.Budget.TokensUsed != 15 {
		t.Fatalf("tokens used = %d, want 15", session.Budget.TokensUsed)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(result.Steps))
	}
	usage := result.Steps[0].Usage
	if usage.InputTokens != 11 || usage.OutputTokens != 4 || usage.Total() != 15 {
		t.Fatalf("step usage = %+v", usage)
	}
	if usage.CallID == "" || usage.Model != "local-small" ||
		usage.Cost.Amount != 3 || usage.Cost.Currency != "CNY" {
		t.Fatalf("step ledger link = %+v", usage)
	}
	if usage.CallID != model.StableCallID("task-loop", "step-1", model.PurposePlan, model.PromptHash(router.request)) {
		t.Fatalf("call id = %q", usage.CallID)
	}
}

func TestParseProposalsVariants(t *testing.T) {
	one, err := ParseProposals(validProposalJSON)
	if err != nil || len(one) != 1 || one[0].Action != domain.ActionReadScope {
		t.Fatalf("object = %+v err=%v", one, err)
	}
	list, err := ParseProposals("```json\n[" + validProposalJSON + "," + validProposalJSON + "]\n```")
	if err != nil || len(list) != 2 {
		t.Fatalf("array = %+v err=%v", list, err)
	}
	prose, err := ParseProposals("好的，建议如下：\n" + validProposalJSON + "\n请不要执行其他动作。")
	if err != nil || len(prose) != 1 {
		t.Fatalf("prose = %+v err=%v", prose, err)
	}
	suffix, err := ParseProposals(`{"action":"READ_SCOPE|human_only=false","tool":"read_scope|extra","parameters":{},"confidence":0.5}`)
	if err != nil || len(suffix) != 1 || suffix[0].Action != domain.ActionReadScope || suffix[0].Tool != "read_scope" {
		t.Fatalf("suffix = %+v err=%v", suffix, err)
	}
	if _, err := ParseProposals("not json at all"); err == nil {
		t.Fatal("invalid output accepted")
	}
	if _, err := ParseProposals(""); err == nil {
		t.Fatal("empty output accepted")
	}
}
