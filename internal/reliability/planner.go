package reliability

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// MaxProposalsPerStep is the hard ceiling on what a model may propose for one
// step. The planner ranks the list, filters it, and executes exactly one item.
const MaxProposalsPerStep = 3

// ActionProposal is untrusted model output. It is not an ActionRequest and it
// carries no authorization; the loop filters it against the explicit
// Authorization ceiling and a later Action Gateway still has to allow it.
type ActionProposal struct {
	ID                  string
	Action              domain.Action
	Tool                string
	AssetID             *domain.AssetID
	Parameters          json.RawMessage
	Rationale           string
	ExpectedObservation string
	HypothesisID        string
	Confidence          float64
	// EstimatedCost is the model's relative cost estimate; lower is preferred
	// when confidence is equal. It is not a monetary cost.
	EstimatedCost int
}

func (p ActionProposal) Validate() error {
	if !p.Action.Valid() {
		return fmt.Errorf("%w: invalid action %q", ErrInvalidProposal, p.Action)
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("%w: confidence %v out of range", ErrInvalidProposal, p.Confidence)
	}
	if p.EstimatedCost < 0 {
		return fmt.Errorf("%w: estimated cost is negative", ErrInvalidProposal)
	}
	if len(p.Parameters) > 0 && !json.Valid(p.Parameters) {
		return fmt.Errorf("%w: parameters are not valid json", ErrInvalidProposal)
	}
	if proposalContainsSecret(p.Parameters) ||
		containsSecret(p.Rationale) ||
		containsSecret(p.ExpectedObservation) {
		return fmt.Errorf("%w: proposal contains a sensitive value", ErrSecretDetected)
	}
	return nil
}

// Proposer is the model-facing interface. It may return 1..MaxProposalsPerStep
// proposals; returning more is truncated to the top-ranked three.
type Proposer interface {
	Propose(ctx context.Context, in ProposeInput) ([]ActionProposal, error)
}

// Planner is an alias for Proposer kept for callers that use the planning term.
type Planner = Proposer

// ProposerUsage is the model/provider usage for one planning call. The Loop
// adds it to the session budget; executor tokens are still accounted
// separately from ExecutionResult.Tokens.
type ProposerUsage struct {
	InputTokens  int
	OutputTokens int
}

// Total returns the tokens charged to the session budget.
func (u ProposerUsage) Total() int {
	if u.InputTokens < 0 || u.OutputTokens < 0 {
		return 0
	}
	return u.InputTokens + u.OutputTokens
}

// UsageProposer is implemented by proposers that can report their own model
// usage. The plain Proposer interface stays backward compatible; a proposer
// that does not implement this interface simply reports no usage.
type UsageProposer interface {
	ProposeWithUsage(ctx context.Context, in ProposeInput) ([]ActionProposal, ProposerUsage, error)
}

func (l *Loop) propose(ctx context.Context, in ProposeInput) ([]ActionProposal, ProposerUsage, error) {
	if up, ok := l.proposer.(UsageProposer); ok {
		return up.ProposeWithUsage(ctx, in)
	}
	proposals, err := l.proposer.Propose(ctx, in)
	return proposals, ProposerUsage{}, err
}

// SessionSnapshot is an immutable-by-value copy of the research session. A
// proposer may inspect or mutate its own copy; it cannot mutate the live
// session because it never receives a pointer to it.
type SessionSnapshot ResearchSession

// Snapshot returns a deep copy of the session for a model-facing proposer.
func (s *ResearchSession) Snapshot() SessionSnapshot {
	if s == nil {
		return SessionSnapshot{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return SessionSnapshot{}
	}
	var out SessionSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return SessionSnapshot{}
	}
	return out
}

// ProposeInput is everything the model-facing proposer may see. It deliberately
// contains no store, network or execution capability, and the session is a
// value snapshot rather than a mutable pointer.
type ProposeInput struct {
	Session         SessionSnapshot
	Context         CompiledContext
	Authorization   Authorization
	Budget          Budget
	Step            int
	LastObservation *Observation
	LastFailure     *FailureSignature
}

// ExecutionResult is what the deterministic Executor returns for one proposal.
type ExecutionResult struct {
	Observation Observation
	Evidence    []domain.EvidenceID
	Fact        *FactRecord
	Failure     *FailureSignature
	// Tokens is the model/provider usage for this step, if any.
	Tokens int
}

// Executor runs exactly one proposal. Implementations must still use the
// Action Gateway; this interface is not an authorization boundary.
type Executor interface {
	Execute(ctx context.Context, proposal ActionProposal) (ExecutionResult, error)
}

// StopReason explains why a loop stopped.
type StopReason string

const (
	StopNoProposals        StopReason = "NO_PROPOSALS"
	StopNoAllowedProposals StopReason = "NO_ALLOWED_PROPOSALS"
	StopMaxSteps           StopReason = "MAX_STEPS"
	StopBudgetExhausted    StopReason = "BUDGET_EXHAUSTED"
	StopStalled            StopReason = "STALLED"
	StopCompleted          StopReason = "COMPLETED"
)

// ExecutedStep is one action that passed filtering and was handed to the
// executor. ErrClass is a normalized class, never a raw provider message.
type ExecutedStep struct {
	Step        int
	Proposal    ActionProposal
	AttemptID   string
	Observation Observation
	Failure     *FailureSignature
	Detection   Detection
	ErrClass    string
	// Usage is the proposer's model usage for this step (zero if the
	// proposer does not implement UsageProposer).
	Usage ProposerUsage
}

// Step is one executor-backed iteration. It is an alias for ExecutedStep.
type Step = ExecutedStep

// LoopResult is the deterministic summary of a short-horizon run.
type LoopResult struct {
	SessionID     string
	Steps         []ExecutedStep
	Stop          StopReason
	StallAction   DetectorAction
	LastDetection Detection
}

// StepsExecuted returns the number of executor calls.
func (r LoopResult) StepsExecuted() int { return len(r.Steps) }

// LoopConfig configures a short-horizon loop. A loop has no long-horizon free
// plan: each iteration compiles a fresh context, proposes at most three actions,
// executes one and replans from the observation.
type LoopConfig struct {
	Proposer      Proposer
	Executor      Executor
	Compiler      *ContextCompiler
	Authorization Authorization
	Evidence      []EvidenceView
	ContextBudget ContextBudget
	// MaxSteps bounds iterations. Zero uses the session Budget.MaxSteps when
	// set, otherwise 5.
	MaxSteps int
	Detector *LoopDetector
	// Store checkpoints the session after every executed step and at exit, so
	// a STALLED/PARKED/HANDOFF session survives a process restart. A nil Store
	// is rejected unless AllowEphemeral is set.
	Store SessionStore
	// AllowEphemeral opts out of durable checkpointing. The zero value requires
	// Store: the pre-execution STARTED checkpoint is the only record that a
	// crash may have produced a side effect, so an in-memory loop must be an
	// explicit, named choice rather than an omission.
	AllowEphemeral bool
	Now            func() time.Time
}

// Loop is the deterministic short-horizon controller.
type Loop struct {
	proposer      Proposer
	executor      Executor
	compiler      *ContextCompiler
	authorization Authorization
	evidence      []EvidenceView
	contextBudget ContextBudget
	maxSteps      int
	detector      *LoopDetector
	store         SessionStore
	now           func() time.Time
}

// NewLoop validates the configuration.
func NewLoop(cfg LoopConfig) (*Loop, error) {
	if cfg.Proposer == nil {
		return nil, fmt.Errorf("%w: proposer is nil", ErrLoopConfiguration)
	}
	if cfg.Executor == nil {
		return nil, fmt.Errorf("%w: executor is nil", ErrLoopConfiguration)
	}
	if cfg.Store == nil && !cfg.AllowEphemeral {
		return nil, fmt.Errorf("%w: LoopConfig.Store is nil (set AllowEphemeral to run without checkpointing)", ErrSessionStoreRequired)
	}
	if err := cfg.Authorization.Validate(); err != nil {
		return nil, err
	}
	for _, ev := range cfg.Evidence {
		if err := ev.Validate(); err != nil {
			return nil, err
		}
	}
	if cfg.Compiler == nil {
		cfg.Compiler = NewDefaultContextCompiler()
	}
	if cfg.Detector == nil {
		cfg.Detector = NewLoopDetector(DetectorConfig{})
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 5
	}
	return &Loop{
		proposer:      cfg.Proposer,
		executor:      cfg.Executor,
		compiler:      cfg.Compiler,
		authorization: cfg.Authorization,
		evidence:      append([]EvidenceView(nil), cfg.Evidence...),
		contextBudget: cfg.ContextBudget,
		maxSteps:      maxSteps,
		detector:      cfg.Detector,
		store:         cfg.Store,
		now:           cfg.Now,
	}, nil
}

// Run executes at most one action per iteration. A tool failure is observed and
// fed back into the next context; only infrastructure/context errors abort the
// run. The model never gets a multi-step plan it can execute in one shot.
func (l *Loop) Run(ctx context.Context, session *ResearchSession) (LoopResult, error) {
	if session == nil {
		return LoopResult{}, fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if err := session.Validate(); err != nil {
		return LoopResult{}, err
	}
	if session.State == "" {
		session.State = StateRunning
	}
	if session.State != StateRunning && session.State != StateCreated {
		return LoopResult{}, fmt.Errorf("%w: session state is %s", ErrInvalidClaim, session.State)
	}
	// A session loaded from a crashed process may still contain STARTED
	// attempts. No execution can be in flight in this run, so treat their
	// outcome as unknown before proposing anything.
	if session.interruptInFlight(l.now().UTC()) {
		if err := l.checkpoint(ctx, session); err != nil {
			return LoopResult{SessionID: session.ID}, err
		}
	}
	if now := l.now().UTC(); l.authorization.Expired(now) {
		return LoopResult{SessionID: session.ID, Stop: StopNoAllowedProposals}, fmt.Errorf("%w", ErrAuthorizationExpired)
	}

	maxSteps := l.maxSteps
	if session.Budget.MaxSteps > 0 && session.Budget.MaxSteps < maxSteps {
		maxSteps = session.Budget.MaxSteps
	}
	result := LoopResult{SessionID: session.ID}
	var lastObservation *Observation
	var lastFailure *FailureSignature

	for step := 1; step <= maxSteps; step++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
		now := l.now().UTC()
		if session.Budget.Exhausted() {
			result.Stop = StopBudgetExhausted
			break
		}
		if l.authorization.Expired(now) {
			result.Stop = StopNoAllowedProposals
			result.LastDetection = Detection{Verdict: DetectionStalled, Action: ActionHandoff, Reasons: []string{"AUTHORIZATION_EXPIRED"}}
			result.StallAction = ActionHandoff
			session.State = StateParked
			break
		}

		compiled, err := l.compiler.Compile(ctx, session, l.authorization, l.evidence, l.contextBudget)
		if err != nil {
			return result, err
		}
		proposals, usage, err := l.propose(ctx, ProposeInput{
			Session:         session.Snapshot(),
			Context:         compiled,
			Authorization:   l.authorization,
			Budget:          session.Budget,
			Step:            step,
			LastObservation: cloneObservationPtr(lastObservation),
			LastFailure:     cloneFailurePtr(lastFailure),
		})
		// Planning itself consumes the model budget. Charge reported usage even
		// when the proposer also returns an error: a provider that billed a
		// timed-out request still spent those tokens, and dropping them would
		// make the persisted budget understate real spend.
		session.Budget.AddTokens(usage.Total())
		if err != nil {
			return result, fmt.Errorf("reliability: proposer: %w", err)
		}
		// Stop if the call exhausted the token ceiling: the proposal was
		// produced under an over-budget request.
		if session.Budget.MaxTokens > 0 && session.Budget.TokensUsed >= session.Budget.MaxTokens {
			result.Stop = StopBudgetExhausted
			break
		}
		if len(proposals) == 0 {
			result.Stop = StopNoProposals
			break
		}
		ranked := RankProposals(proposals, l.authorization, session.Budget, now)
		if len(ranked) > 0 {
			// A previous run may have started this exact tool+args and crashed
			// before recording the outcome. Replaying it could repeat a side
			// effect, so only a different action/argument set is eligible.
			allowed := ranked[:0]
			for _, p := range ranked {
				if session.HasInterruptedAttempt(proposalTool(p), HashArgs(p.Parameters)) {
					continue
				}
				allowed = append(allowed, p)
			}
			ranked = allowed
		}
		if len(ranked) == 0 {
			result.Stop = StopNoAllowedProposals
			break
		}
		proposal := ranked[0]

		if !session.Budget.ConsumeStep() {
			result.Stop = StopBudgetExhausted
			break
		}
		if !session.Budget.ConsumeToolCall() {
			result.Stop = StopBudgetExhausted
			break
		}

		startedAt := l.now().UTC()
		// Include the session's attempt ordinal: step numbering restarts at 1
		// for every Run/Resume, so step+proposal alone would collide and let
		// ReplaceAttempt overwrite an older completed attempt.
		attemptID := StableID("attempt", fmt.Sprint(len(session.Attempts)+1), fmt.Sprint(step),
			proposal.ID, proposalTool(proposal), HashArgs(proposal.Parameters))
		startedAttempt := Attempt{
			ID: attemptID, Step: step, ProposalID: proposal.ID,
			ToolName: proposalTool(proposal), Action: proposal.Action,
			ArgsHash: HashArgs(proposal.Parameters),
			Status:   AttemptStarted, StartedAt: startedAt,
		}
		// The STARTED record and its checkpoint must exist before the tool can
		// have a side effect; if the process dies after this point, the next
		// run knows the outcome is unknown instead of silently retrying.
		if err := session.AddAttempt(startedAttempt); err != nil {
			return result, err
		}
		if err := l.checkpoint(ctx, session); err != nil {
			return result, err
		}
		execResult, execErr := l.executor.Execute(ctx, proposal)
		if ctx != nil && ctx.Err() != nil {
			return result, ctx.Err()
		}

		failure := cloneFailurePtr(execResult.Failure)
		errClass := ""
		if execErr != nil && failure == nil {
			sig := NewFailureSignature(proposalTool(proposal), "EXECUTION_ERROR", proposal.Parameters)
			failure = &sig
		}
		if failure != nil {
			if err := failure.Validate(); err != nil {
				return result, err
			}
			errClass = failure.ErrorClass
		}
		if execErr != nil && errClass == "" {
			errClass = "EXECUTION_ERROR"
		}

		obs := execResult.Observation
		if obs.ID == "" {
			content := "execution completed"
			if errClass != "" {
				content = "execution failed: " + errClass
			}
			obs = NewObservation(proposalTool(proposal), proposal.Parameters, content, content, nil, startedAt)
		}
		if obs.ToolName == "" {
			obs.ToolName = proposalTool(proposal)
		}
		if obs.ArgsHash == "" {
			obs.ArgsHash = HashArgs(proposal.Parameters)
		}
		if obs.ResponseHash == "" {
			obs.ResponseHash = HashString(obs.Content)
		}
		if obs.ObservedAt.IsZero() {
			obs.ObservedAt = startedAt
		}
		obs.Evidence = mergeEvidence(obs.Evidence, execResult.Evidence)
		if obs.Sequence == 0 {
			obs.Sequence = len(session.RecentObservations) + 1
		}
		// A fixed test clock can make two identical calls produce the same
		// content-addressed observation ID. Session observation IDs must stay
		// unique, so a collision is disambiguated by sequence/time.
		if obs.ID == "" || sessionHasObservationID(session, obs.ID) {
			obs.ID = StableID("obs", obs.ToolName, obs.ArgsHash, obs.ResponseHash,
				fmt.Sprint(obs.Sequence), obs.ObservedAt.Format(time.RFC3339Nano))
		}
		if err := session.AddObservation(obs); err != nil {
			return result, err
		}

		var factID string
		if execResult.Fact != nil {
			fact := *execResult.Fact
			if fact.CreatedAt.IsZero() {
				fact.CreatedAt = now
			}
			if fact.UpdatedAt.IsZero() {
				fact.UpdatedAt = fact.CreatedAt
			}
			if strings.TrimSpace(fact.ID) == "" {
				fact.ID = StableID("fact", string(fact.Claim.Level), fact.Claim.Statement,
					utc(fact.CreatedAt).Format(time.RFC3339Nano))
			}
			factID = fact.ID
			if !sessionHasFactID(session, fact.ID) {
				if err := session.AddFact(fact); err != nil {
					return result, err
				}
			}
			// An already-known fact is not progress; factID is still set so the
			// detector's seen-set treats it as unchanged.
		}

		attempt := startedAttempt
		attempt.Status = AttemptCompleted
		if failure != nil {
			attempt.Status = AttemptFailed
		}
		attempt.ObservationID = obs.ID
		attempt.Evidence = dedupeEvidence(obs.Evidence)
		attempt.Failure = cloneFailurePtr(failure)
		attempt.FinishedAt = l.now().UTC()
		if err := session.ReplaceAttempt(attempt); err != nil {
			return result, err
		}
		session.Budget.AddTokens(execResult.Tokens)

		detection := l.detector.Observe(StepSignal{
			ToolName:       attempt.ToolName,
			ArgsHash:       attempt.ArgsHash,
			ResponseHash:   obs.ResponseHash,
			ErrorSignature: cloneFailurePtr(failure),
			HypothesisID:   proposal.HypothesisID,
			Evidence:       attempt.Evidence,
			FactID:         factID,
			TokensUsed:     session.Budget.TokensUsed,
			ToolCallsUsed:  session.Budget.ToolCallsUsed,
			At:             attempt.FinishedAt,
		})
		result.LastDetection = detection
		result.Steps = append(result.Steps, ExecutedStep{
			Step:        step,
			Proposal:    proposal,
			AttemptID:   attempt.ID,
			Observation: obs,
			Failure:     cloneFailurePtr(failure),
			Detection:   detection,
			ErrClass:    errClass,
			Usage:       usage,
		})
		lastObservation = &obs
		lastFailure = cloneFailurePtr(failure)

		if detection.Verdict == DetectionStalled {
			result.Stop = StopStalled
			result.StallAction = detection.Action
			session.State = StateStalled
			if err := l.checkpoint(ctx, session); err != nil {
				return result, err
			}
			break
		}
		// Every recorded step is a durable checkpoint: a crash after the
		// observation/attempt are appended cannot lose the learned state.
		if err := l.checkpoint(ctx, session); err != nil {
			return result, err
		}
	}

	if result.Stop == "" {
		result.Stop = StopMaxSteps
	}
	switch result.Stop {
	case StopStalled:
		session.State = StateStalled
	case StopBudgetExhausted:
		session.State = StateParked
	}
	if now := l.now().UTC(); now.After(session.UpdatedAt) {
		session.UpdatedAt = now
	}
	if err := l.checkpoint(ctx, session); err != nil {
		return result, err
	}
	return result, nil
}

// checkpoint persists the current session when a store is configured. A
// persistence failure aborts the run: silently continuing would mean the
// durable recovery point and the in-memory state disagree.
func (l *Loop) checkpoint(ctx context.Context, session *ResearchSession) error {
	if l.store == nil {
		return nil
	}
	if err := l.store.Save(ctx, session); err != nil {
		return fmt.Errorf("reliability: persist session %s: %w", session.ID, err)
	}
	return nil
}

// RankProposals filters model proposals against the authorization ceiling,
// budget and asset requirements, then ranks at most three deterministically:
// confidence descending, estimated cost ascending, original order as the
// tie-break.
func RankProposals(proposals []ActionProposal, authorization Authorization, budget Budget, now time.Time) []ActionProposal {
	if authorization.Expired(now) || budget.Exhausted() {
		return nil
	}
	type candidate struct {
		proposal ActionProposal
		index    int
	}
	candidates := make([]candidate, 0, len(proposals))
	for i, raw := range proposals {
		p := normalizeProposal(raw)
		if err := p.Validate(); err != nil {
			continue
		}
		if !authorization.Allows(p.Action) {
			continue
		}
		// Human-only actions never execute from the autonomous loop. They may
		// be proposed as information, but a human must perform them.
		if p.Action.HumanOnly() {
			continue
		}
		if p.Action.NeedsAsset() {
			// Execution needs an authoritative asset from the authorization.
			// A caller/model cannot invent one.
			if authorization.AssetID == nil || p.AssetID == nil || *authorization.AssetID != *p.AssetID {
				continue
			}
		} else if authorization.AssetID != nil && p.AssetID != nil && *authorization.AssetID != *p.AssetID {
			continue
		}
		candidates = append(candidates, candidate{proposal: p, index: i})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.proposal.Confidence != b.proposal.Confidence {
			return a.proposal.Confidence > b.proposal.Confidence
		}
		if a.proposal.EstimatedCost != b.proposal.EstimatedCost {
			return a.proposal.EstimatedCost < b.proposal.EstimatedCost
		}
		return a.index < b.index
	})
	if len(candidates) > MaxProposalsPerStep {
		candidates = candidates[:MaxProposalsPerStep]
	}
	out := make([]ActionProposal, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.proposal)
	}
	return out
}

func normalizeProposal(p ActionProposal) ActionProposal {
	if strings.TrimSpace(p.Tool) == "" {
		p.Tool = strings.ToLower(string(p.Action))
	}
	if strings.TrimSpace(p.ID) == "" {
		p.ID = StableID("proposal", string(p.Action), p.Tool, HashArgs(p.Parameters), p.Rationale, p.HypothesisID)
	}
	return p
}

// proposalContainsSecret scans both the raw JSON and a quote-stripped form.
// The redaction patterns are written for header/URL syntax (token=..., token:
// ...), so a JSON key/value pair such as "token":"value" needs the second pass.
func proposalContainsSecret(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	s := string(raw)
	if containsSecret(s) {
		return true
	}
	stripped := strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(s)
	return containsSecret(stripped)
}

func proposalTool(p ActionProposal) string {
	if strings.TrimSpace(p.Tool) != "" {
		return p.Tool
	}
	return strings.ToLower(string(p.Action))
}

func cloneObservationPtr(o *Observation) *Observation {
	if o == nil {
		return nil
	}
	out := *o
	out.Evidence = append([]domain.EvidenceID(nil), o.Evidence...)
	out.FactIDs = append([]string(nil), o.FactIDs...)
	return &out
}

func cloneFailurePtr(f *FailureSignature) *FailureSignature {
	if f == nil {
		return nil
	}
	out := *f
	return &out
}

func sessionHasObservationID(s *ResearchSession, id string) bool {
	for _, o := range s.RecentObservations {
		if o.ID == id {
			return true
		}
	}
	return false
}

func sessionHasFactID(s *ResearchSession, id string) bool {
	for _, f := range s.KnownFacts {
		if f.ID == id {
			return true
		}
	}
	return false
}
