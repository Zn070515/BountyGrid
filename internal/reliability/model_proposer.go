package reliability

import (
	"context"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

// DefaultModelProposerMaxTokens bounds a proposal response. The planner only
// needs one small JSON object per step; a larger cap invites the model to write
// a long plan the loop will discard.
const DefaultModelProposerMaxTokens = 512

const modelProposalInstruction = `你是短程 planner。只输出一个 JSON 对象，不要数组、Markdown 或解释。
字段：{"action":"...","tool":"...","parameters":{},"rationale":"...","expected_observation":"...","confidence":0.9}
action 只能从上下文 AVAILABLE ACTIONS 中选择；parameters 必须是 JSON 对象，不要复制上下文中的字段或值。
`

// ModelRouter is the narrow Router surface the proposer needs. Keeping it
// narrow lets tests and alternative routers be injected without importing the
// whole router configuration.
type ModelRouter interface {
	Route(ctx context.Context, req model.ModelRequest) (model.ModelResponse, error)
}

// ModelProposerConfig configures a Router-backed Proposer. The proposer is the
// only place where model output uses the Model Router's audit, budget and
// egress policy; the reliability loop still filters every proposal.
type ModelProposerConfig struct {
	Router    ModelRouter
	ProgramID domain.ProgramID
	// TaskID is used for the stable call ID. Empty falls back to the session's
	// TaskID, so a worker does not have to duplicate it.
	TaskID domain.TaskID
	// DataClass defaults to INTERNAL. A caller that knows the compiled context
	// contains SENSITIVE material must raise it; the router takes the maximum
	// with Provenance and the program's egress ceiling still applies.
	DataClass  model.DataClass
	Provenance []model.DataClass
	// MaxTokens is the response cap. Zero uses DefaultModelProposerMaxTokens.
	MaxTokens int
	// Budget is an optional per-call money budget understood by the Router.
	Budget domain.Money
	// DisableStableCallID opts out of the worker-safe stable CallID. Leave the
	// zero value for production workers: a crash retry must not pay twice.
	DisableStableCallID  bool
	RequiredCapabilities []model.Capability
}

// ModelProposer adapts a Model Router to the reliability Proposer interfaces.
// It never executes anything: it returns candidates that RankProposals and the
// Action Gateway still have to approve.
type ModelProposer struct {
	cfg ModelProposerConfig
}

func NewModelProposer(cfg ModelProposerConfig) (*ModelProposer, error) {
	if cfg.Router == nil {
		return nil, fmt.Errorf("%w: model router is nil", ErrLoopConfiguration)
	}
	if cfg.DataClass == "" {
		cfg.DataClass = model.DataInternal
	}
	if !cfg.DataClass.Valid() {
		return nil, fmt.Errorf("%w: invalid data class %q", ErrLoopConfiguration, cfg.DataClass)
	}
	for _, d := range cfg.Provenance {
		if !d.Valid() {
			return nil, fmt.Errorf("%w: invalid provenance data class %q", ErrLoopConfiguration, d)
		}
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultModelProposerMaxTokens
	}
	if cfg.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: max tokens must be positive", ErrLoopConfiguration)
	}
	cfg.Provenance = append([]model.DataClass(nil), cfg.Provenance...)
	cfg.RequiredCapabilities = append([]model.Capability(nil), cfg.RequiredCapabilities...)
	return &ModelProposer{cfg: cfg}, nil
}

// Propose implements Proposer.
func (p *ModelProposer) Propose(ctx context.Context, in ProposeInput) ([]ActionProposal, error) {
	proposals, _, err := p.ProposeWithUsage(ctx, in)
	return proposals, err
}

// ProposeWithUsage implements UsageProposer: the Router's token usage, actual
// cost and logical CallID are returned with the proposals so the loop can
// charge the session budget and persist the ledger link on the executed step.
func (p *ModelProposer) ProposeWithUsage(ctx context.Context, in ProposeInput) ([]ActionProposal, ProposerUsage, error) {
	req, err := p.request(in)
	if err != nil {
		return nil, ProposerUsage{}, err
	}
	resp, err := p.cfg.Router.Route(ctx, req)
	if err != nil {
		return nil, ProposerUsage{}, err
	}
	usage := ProposerUsage{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		Cost:         resp.Cost,
		CallID:       req.CallID,
		Model:        resp.Model,
	}
	proposals, parseErr := ParseProposals(resp.Content)
	if parseErr != nil {
		// The provider was paid but produced nothing executable. Charge the
		// usage and propose nothing; the loop's detector handles the stall.
		return nil, usage, nil
	}
	return proposals, usage, nil
}

func (p *ModelProposer) request(in ProposeInput) (model.ModelRequest, error) {
	taskID := p.cfg.TaskID
	if taskID == "" {
		taskID = in.Session.TaskID
	}
	stepID := fmt.Sprintf("step-%d", in.Step)
	req := model.ModelRequest{
		TaskID:     taskID,
		ProgramID:  p.cfg.ProgramID,
		StepID:     stepID,
		Purpose:    model.PurposePlan,
		DataClass:  p.cfg.DataClass,
		Provenance: append([]model.DataClass(nil), p.cfg.Provenance...),
		Input: []model.Message{
			{Role: "system", Content: modelProposalInstruction},
			{Role: "user", Content: in.Context.Text},
		},
		MaxTokens:            p.cfg.MaxTokens,
		Budget:               p.cfg.Budget,
		RequiredCapabilities: append([]model.Capability(nil), p.cfg.RequiredCapabilities...),
	}
	if !p.cfg.DisableStableCallID {
		req.CallID = model.StableCallID(taskID, stepID, model.PurposePlan, model.PromptHash(req))
	}
	if err := req.Validate(); err != nil {
		return model.ModelRequest{}, fmt.Errorf("%w: %v", ErrLoopConfiguration, err)
	}
	return req, nil
}

var (
	_ Proposer      = (*ModelProposer)(nil)
	_ UsageProposer = (*ModelProposer)(nil)
)
