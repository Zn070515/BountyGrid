// Package reliability implements the deterministic, model-facing reliability
// layer for local small models.
//
// The package never calls a model and never performs network or platform I/O.
// It owns the parts of a research run that a small model is bad at:
//
//   - an explicit claim ladder (Observation -> Hypothesis -> Claim -> VerifiedFact)
//     that a model cannot skip;
//   - a curated, budgeted context view instead of raw page dumps;
//   - a short-horizon planner that executes at most one action per step and
//     replans from the resulting observation;
//   - loop/stall detection based on repeated calls, repeated errors, repeated
//     hypotheses and a lack of new evidence/facts;
//   - a synthesized FailureReport that never echoes the raw failed arguments.
//
// Authority stays in the Go control layer. An ActionProposal is a proposal, not
// an authorization; the caller still has to pass it through the Action Gateway.
package reliability

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

// Sentinel errors. Callers should use errors.Is rather than string matching.
var (
	// ErrInvalidClaim means a claim is structurally invalid, for example a
	// CLAIM or OBSERVATION without evidence.
	ErrInvalidClaim = errors.New("reliability: invalid claim")
	// ErrLevelSkipped means a promotion tried to jump over a claim level.
	ErrLevelSkipped = errors.New("reliability: claim level skipped")
	// ErrInvalidPromotion covers same-level, backwards and terminal promotions.
	ErrInvalidPromotion = errors.New("reliability: invalid promotion")
	// ErrEvidenceRequired means the target level requires evidence and none was
	// supplied.
	ErrEvidenceRequired = errors.New("reliability: evidence required")
	// ErrReceiptRequired means VERIFIED_FACT was requested without a COMPLETED
	// execution receipt.
	ErrReceiptRequired = errors.New("reliability: completed execution receipt required")
	// ErrVerificationRequired means VERIFIED_FACT was requested without a
	// VERIFIED verification with evidence.
	ErrVerificationRequired = errors.New("reliability: verified verification evidence required")
	// ErrSecretDetected means free-form state text contained a value matching a
	// secret pattern. Raw secrets must never enter research state.
	ErrSecretDetected = errors.New("reliability: sensitive value detected")
	// ErrBudgetInvalid means a budget with impossible limits was supplied.
	ErrBudgetInvalid = errors.New("reliability: invalid budget")
	// ErrContextBudgetInvalid means a context compiler budget cannot fit the
	// mandatory section anchors.
	ErrContextBudgetInvalid = errors.New("reliability: context budget too small")
	// ErrAuthorizationExpired means the supplied authorization is no longer
	// valid; no action may be proposed or executed.
	ErrAuthorizationExpired = errors.New("reliability: authorization expired")
	// ErrInvalidProposal means a model proposal is structurally invalid.
	ErrInvalidProposal = errors.New("reliability: invalid action proposal")
	// ErrLoopConfiguration means a Loop was created without proposer/executor.
	ErrLoopConfiguration = errors.New("reliability: invalid loop configuration")
	// ErrInvalidResume means Resume was called on a session that is not
	// STALLED or PARKED. A RUNNING/COMPLETED/ABORTED session must not be
	// reopened implicitly.
	ErrInvalidResume = errors.New("reliability: session cannot be resumed")
)

// ResearchState is the coarse lifecycle of one research session. A model may
// propose transitions but only the deterministic layer sets them.
type ResearchState string

const (
	StateCreated   ResearchState = "CREATED"
	StateRunning   ResearchState = "RUNNING"
	StateStalled   ResearchState = "STALLED"
	StateParked    ResearchState = "PARKED"
	StateCompleted ResearchState = "COMPLETED"
	StateAborted   ResearchState = "ABORTED"
)

func (s ResearchState) Valid() bool {
	switch s {
	case StateCreated, StateRunning, StateStalled, StateParked, StateCompleted, StateAborted:
		return true
	default:
		return false
	}
}

// ClaimLevel is the evidence ladder. A model cannot skip a level and cannot
// claim VERIFIED_FACT without a completed receipt and verification evidence.
type ClaimLevel string

const (
	// LevelObservation is an evidence-backed observation of the platform.
	LevelObservation ClaimLevel = "OBSERVATION"
	// LevelHypothesis is an interpretation that may have no evidence yet.
	LevelHypothesis ClaimLevel = "HYPOTHESIS"
	// LevelClaim is an evidence-backed assertion. It is invalid without evidence.
	LevelClaim ClaimLevel = "CLAIM"
	// LevelVerifiedFact additionally requires a COMPLETED execution receipt and
	// a VERIFIED verification with evidence.
	LevelVerifiedFact ClaimLevel = "VERIFIED_FACT"
)

func (l ClaimLevel) Valid() bool {
	switch l {
	case LevelObservation, LevelHypothesis, LevelClaim, LevelVerifiedFact:
		return true
	default:
		return false
	}
}

// Rank returns the ladder position. Lower is earlier. An invalid level returns
// -1 so callers can fail closed.
func (l ClaimLevel) Rank() int {
	switch l {
	case LevelObservation:
		return 0
	case LevelHypothesis:
		return 1
	case LevelClaim:
		return 2
	case LevelVerifiedFact:
		return 3
	default:
		return -1
	}
}

// Next returns the only level a valid promotion may target.
func (l ClaimLevel) Next() (ClaimLevel, bool) {
	switch l {
	case LevelObservation:
		return LevelHypothesis, true
	case LevelHypothesis:
		return LevelClaim, true
	case LevelClaim:
		return LevelVerifiedFact, true
	default:
		return "", false
	}
}

// Budget bounds a session. A zero maximum means "not bounded by this field".
// The loop consumes one step and one tool call per executed proposal.
type Budget struct {
	MaxSteps      int
	StepsUsed     int
	MaxToolCalls  int
	ToolCallsUsed int
	MaxTokens     int
	TokensUsed    int
}

func (b Budget) Validate() error {
	if b.MaxSteps < 0 || b.StepsUsed < 0 ||
		b.MaxToolCalls < 0 || b.ToolCallsUsed < 0 ||
		b.MaxTokens < 0 || b.TokensUsed < 0 {
		return fmt.Errorf("%w: negative value", ErrBudgetInvalid)
	}
	return nil
}

// Exhausted reports whether any configured limit has been reached.
func (b Budget) Exhausted() bool {
	if b.MaxSteps > 0 && b.StepsUsed >= b.MaxSteps {
		return true
	}
	if b.MaxToolCalls > 0 && b.ToolCallsUsed >= b.MaxToolCalls {
		return true
	}
	if b.MaxTokens > 0 && b.TokensUsed >= b.MaxTokens {
		return true
	}
	return false
}

// RemainingSteps returns the remaining steps, or -1 when unbounded.
func (b Budget) RemainingSteps() int {
	if b.MaxSteps <= 0 {
		return -1
	}
	if b.StepsUsed >= b.MaxSteps {
		return 0
	}
	return b.MaxSteps - b.StepsUsed
}

// RemainingToolCalls returns the remaining tool calls, or -1 when unbounded.
func (b Budget) RemainingToolCalls() int {
	if b.MaxToolCalls <= 0 {
		return -1
	}
	if b.ToolCallsUsed >= b.MaxToolCalls {
		return 0
	}
	return b.MaxToolCalls - b.ToolCallsUsed
}

// RemainingTokens returns the remaining tokens, or -1 when unbounded.
func (b Budget) RemainingTokens() int {
	if b.MaxTokens <= 0 {
		return -1
	}
	if b.TokensUsed >= b.MaxTokens {
		return 0
	}
	return b.MaxTokens - b.TokensUsed
}

// ConsumeStep reserves one step. It returns false when the step limit is
// already exhausted.
func (b *Budget) ConsumeStep() bool {
	if b == nil {
		return false
	}
	if b.MaxSteps > 0 && b.StepsUsed >= b.MaxSteps {
		return false
	}
	b.StepsUsed++
	return true
}

// ConsumeToolCall reserves one tool call. It returns false when the tool-call
// limit is already exhausted.
func (b *Budget) ConsumeToolCall() bool {
	if b == nil {
		return false
	}
	if b.MaxToolCalls > 0 && b.ToolCallsUsed >= b.MaxToolCalls {
		return false
	}
	b.ToolCallsUsed++
	return true
}

// AddTokens records model usage. Negative values are ignored.
func (b *Budget) AddTokens(n int) {
	if b == nil || n <= 0 {
		return
	}
	b.TokensUsed += n
}

// Observation is a summarized, evidence-linked result of one step. Content is
// expected to be a model-facing summary, not a raw page/JSON dump, and is
// scrubbed by NewObservation.
type Observation struct {
	ID           string
	ToolName     string
	Action       domain.Action
	ArgsHash     string
	ResponseHash string
	Content      string
	Evidence     []domain.EvidenceID
	FactIDs      []string
	Sequence     int
	ObservedAt   time.Time
}

func (o Observation) Validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return fmt.Errorf("%w: observation id is empty", ErrInvalidClaim)
	}
	if strings.TrimSpace(o.ToolName) == "" {
		return fmt.Errorf("%w: observation %s tool is empty", ErrInvalidClaim, o.ID)
	}
	if o.ObservedAt.IsZero() {
		return fmt.Errorf("%w: observation %s observed_at is zero", ErrInvalidClaim, o.ID)
	}
	if o.Sequence < 0 {
		return fmt.Errorf("%w: observation %s sequence is negative", ErrInvalidClaim, o.ID)
	}
	if err := validateEvidence(o.Evidence); err != nil {
		return err
	}
	if containsSecret(o.Content) {
		return fmt.Errorf("%w: observation %s content", ErrSecretDetected, o.ID)
	}
	return nil
}

// NewObservation builds a stable, sanitized observation. The ID is derived
// from the tool, arguments hash, response hash and timestamp, so replaying the
// same inputs produces the same ID.
func NewObservation(tool string, params []byte, response, content string, evidence []domain.EvidenceID, at time.Time) Observation {
	at = utc(at)
	argsHash := HashArgs(params)
	responseHash := HashString(response)
	content, _ = redact.Redact(content)
	return Observation{
		ID:           StableID("obs", tool, argsHash, responseHash, at.Format(time.RFC3339Nano)),
		ToolName:     tool,
		ArgsHash:     argsHash,
		ResponseHash: responseHash,
		Content:      content,
		Evidence:     dedupeEvidence(evidence),
		ObservedAt:   at,
	}
}

// Claim is an assertion at a specific ladder level.
type Claim struct {
	Level      ClaimLevel
	Statement  string
	Evidence   []domain.EvidenceID
	Confidence float64
}

// Validate enforces the evidence invariant: OBSERVATION, CLAIM and
// VERIFIED_FACT are invalid without evidence. HYPOTHESIS may be unsupported.
func (c Claim) Validate() error {
	if !c.Level.Valid() {
		return fmt.Errorf("%w: unknown level %q", ErrInvalidClaim, c.Level)
	}
	if strings.TrimSpace(c.Statement) == "" {
		return fmt.Errorf("%w: statement is empty", ErrInvalidClaim)
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("%w: confidence %v out of range", ErrInvalidClaim, c.Confidence)
	}
	if err := validateEvidence(c.Evidence); err != nil {
		return err
	}
	if containsSecret(c.Statement) {
		return fmt.Errorf("%w: claim statement", ErrSecretDetected)
	}
	switch c.Level {
	case LevelObservation, LevelClaim, LevelVerifiedFact:
		if !c.HasEvidence() {
			return fmt.Errorf("%w: %s requires evidence", ErrInvalidClaim, c.Level)
		}
	}
	return nil
}

// HasEvidence reports whether at least one non-empty evidence ID is attached.
func (c Claim) HasEvidence() bool {
	for _, id := range c.Evidence {
		if strings.TrimSpace(string(id)) != "" {
			return true
		}
	}
	return false
}

// ReceiptProof is the evidence-of-execution summary the reliability layer needs.
// The durable receipt is owned by the harness/store; this type only captures the
// fields required to decide whether a VERIFIED_FACT is justified.
type ReceiptProof struct {
	ID        string
	Status    domain.ReceiptStatus
	ProgramID domain.ProgramID
	FindingID domain.FindingID
	AssetID   *domain.AssetID
	Action    domain.Action
	Evidence  []domain.EvidenceID
}

func (r ReceiptProof) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("%w: receipt id is empty", ErrInvalidClaim)
	}
	if !r.Status.Valid() {
		return fmt.Errorf("%w: receipt %s has invalid status %q", ErrInvalidClaim, r.ID, r.Status)
	}
	return validateEvidence(r.Evidence)
}

// Completed reports whether this receipt proves the action actually ran.
func (r ReceiptProof) Completed() bool { return r.Status == domain.ReceiptCompleted }

// VerificationProof is the subset of domain.Verification needed to justify a
// VERIFIED_FACT. A model-supplied "I checked it" string is not accepted; the
// verdict must be VERIFIED and evidence must be attached.
type VerificationProof struct {
	ID         string
	Verdict    domain.VerificationVerdict
	ProgramID  domain.ProgramID
	FindingID  domain.FindingID
	AssetID    *domain.AssetID
	Evidence   []domain.EvidenceID
	VerifiedAt time.Time
}

func (v VerificationProof) Validate() error {
	if strings.TrimSpace(v.ID) == "" {
		return fmt.Errorf("%w: verification id is empty", ErrInvalidClaim)
	}
	if !v.Verdict.Valid() {
		return fmt.Errorf("%w: verification %s has invalid verdict %q", ErrInvalidClaim, v.ID, v.Verdict)
	}
	if err := validateEvidence(v.Evidence); err != nil {
		return err
	}
	if v.VerifiedAt.IsZero() {
		return fmt.Errorf("%w: verification %s verified_at is zero", ErrInvalidClaim, v.ID)
	}
	return nil
}

// FactRecord is a claim recorded in the session's known-facts ledger. IDs and
// timestamps are stable so a replayed run can be compared deterministically.
type FactRecord struct {
	ID                   string
	Claim                Claim
	ProgramID            domain.ProgramID
	FindingID            domain.FindingID
	AssetID              *domain.AssetID
	SourceObservationIDs []string
	Receipts             []ReceiptProof
	Verification         *VerificationProof
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewFactRecord creates a valid record at the supplied level. Evidence,
// receipts and verification are checked by FactRecord.Validate.
func NewFactRecord(id string, claim Claim, at time.Time) (FactRecord, error) {
	at = utc(at)
	if strings.TrimSpace(id) == "" {
		id = StableID("fact", string(claim.Level), claim.Statement, at.Format(time.RFC3339Nano))
	}
	f := FactRecord{ID: id, Claim: claim, CreatedAt: at, UpdatedAt: at}
	if err := f.Validate(); err != nil {
		return FactRecord{}, err
	}
	return f, nil
}

func (f FactRecord) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return fmt.Errorf("%w: fact id is empty", ErrInvalidClaim)
	}
	if err := f.Claim.Validate(); err != nil {
		return err
	}
	if f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: fact %s timestamps are zero", ErrInvalidClaim, f.ID)
	}
	if f.UpdatedAt.Before(f.CreatedAt) {
		return fmt.Errorf("%w: fact %s updated_at before created_at", ErrInvalidClaim, f.ID)
	}
	for _, id := range f.SourceObservationIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: fact %s has an empty source observation", ErrInvalidClaim, f.ID)
		}
	}
	for _, r := range f.Receipts {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if f.Verification != nil {
		if err := f.Verification.Validate(); err != nil {
			return err
		}
	}
	if f.Claim.Level == LevelVerifiedFact {
		return requireVerifiedFact(f)
	}
	return nil
}

// HypothesisStatus is the lifecycle of an open or failed hypothesis.
type HypothesisStatus string

const (
	HypothesisOpen      HypothesisStatus = "OPEN"
	HypothesisSupported HypothesisStatus = "SUPPORTED"
	HypothesisRefuted   HypothesisStatus = "REFUTED"
	HypothesisAbandoned HypothesisStatus = "ABANDONED"
)

func (s HypothesisStatus) Valid() bool {
	switch s {
	case HypothesisOpen, HypothesisSupported, HypothesisRefuted, HypothesisAbandoned:
		return true
	default:
		return false
	}
}

// Hypothesis is a proposed explanation. It may carry supporting evidence, but
// unlike a Claim it is not invalid without it.
type Hypothesis struct {
	ID         string
	Statement  string
	Evidence   []domain.EvidenceID
	Confidence float64
	Status     HypothesisStatus
	Refutation string
	Failure    *FailureSignature
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (h Hypothesis) Validate() error {
	if strings.TrimSpace(h.ID) == "" {
		return fmt.Errorf("%w: hypothesis id is empty", ErrInvalidClaim)
	}
	if strings.TrimSpace(h.Statement) == "" {
		return fmt.Errorf("%w: hypothesis %s statement is empty", ErrInvalidClaim, h.ID)
	}
	if !h.Status.Valid() {
		return fmt.Errorf("%w: hypothesis %s status %q", ErrInvalidClaim, h.ID, h.Status)
	}
	if h.Confidence < 0 || h.Confidence > 1 {
		return fmt.Errorf("%w: hypothesis %s confidence %v out of range", ErrInvalidClaim, h.ID, h.Confidence)
	}
	if h.CreatedAt.IsZero() || h.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: hypothesis %s timestamps are zero", ErrInvalidClaim, h.ID)
	}
	if h.UpdatedAt.Before(h.CreatedAt) {
		return fmt.Errorf("%w: hypothesis %s updated_at before created_at", ErrInvalidClaim, h.ID)
	}
	if err := validateEvidence(h.Evidence); err != nil {
		return err
	}
	if containsSecret(h.Statement) || containsSecret(h.Refutation) {
		return fmt.Errorf("%w: hypothesis %s text", ErrSecretDetected, h.ID)
	}
	if h.Failure != nil {
		if err := h.Failure.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Unknown is an explicit open question. Keeping unknowns explicit prevents a
// small model from silently inventing an answer.
type Unknown struct {
	ID        string
	Question  string
	Reason    string
	Blocking  bool
	CreatedAt time.Time
}

func (u Unknown) Validate() error {
	if strings.TrimSpace(u.ID) == "" {
		return fmt.Errorf("%w: unknown id is empty", ErrInvalidClaim)
	}
	if strings.TrimSpace(u.Question) == "" {
		return fmt.Errorf("%w: unknown %s question is empty", ErrInvalidClaim, u.ID)
	}
	if u.CreatedAt.IsZero() {
		return fmt.Errorf("%w: unknown %s created_at is zero", ErrInvalidClaim, u.ID)
	}
	if containsSecret(u.Question) || containsSecret(u.Reason) {
		return fmt.Errorf("%w: unknown %s text", ErrSecretDetected, u.ID)
	}
	return nil
}

// AttemptStatus is the lifecycle of one executed step. STARTED is written
// before the tool runs; if the process dies before it is finalized, the next
// run treats it as INTERRUPTED because the side effect may already have
// happened.
type AttemptStatus string

const (
	AttemptStarted     AttemptStatus = "STARTED"
	AttemptCompleted   AttemptStatus = "COMPLETED"
	AttemptFailed      AttemptStatus = "FAILED"
	AttemptInterrupted AttemptStatus = "INTERRUPTED"
)

func (s AttemptStatus) Valid() bool {
	switch s {
	case AttemptStarted, AttemptCompleted, AttemptFailed, AttemptInterrupted:
		return true
	default:
		return false
	}
}

// Terminal reports whether the attempt no longer represents in-flight work.
func (s AttemptStatus) Terminal() bool {
	return s == AttemptCompleted || s == AttemptFailed || s == AttemptInterrupted
}

// Attempt is one executed (or refused) proposal. The failure signature is kept
// instead of raw arguments so retries and reporting cannot echo secrets.
type Attempt struct {
	ID            string
	Step          int
	ProposalID    string
	ToolName      string
	Action        domain.Action
	ArgsHash      string
	ObservationID string
	Evidence      []domain.EvidenceID
	Failure       *FailureSignature
	// Status is empty for legacy records, which are treated as COMPLETED.
	Status     AttemptStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

func (a Attempt) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("%w: attempt id is empty", ErrInvalidClaim)
	}
	if a.Step < 1 {
		return fmt.Errorf("%w: attempt %s step must be positive", ErrInvalidClaim, a.ID)
	}
	if strings.TrimSpace(a.ToolName) == "" {
		return fmt.Errorf("%w: attempt %s tool is empty", ErrInvalidClaim, a.ID)
	}
	if a.Status != "" && !a.Status.Valid() {
		return fmt.Errorf("%w: attempt %s has invalid status %q", ErrInvalidClaim, a.ID, a.Status)
	}
	if a.StartedAt.IsZero() {
		return fmt.Errorf("%w: attempt %s started_at is zero", ErrInvalidClaim, a.ID)
	}
	if a.Status == AttemptStarted {
		// In-flight attempts may have no finish time yet.
		if !a.FinishedAt.IsZero() && a.FinishedAt.Before(a.StartedAt) {
			return fmt.Errorf("%w: attempt %s finished_at before started_at", ErrInvalidClaim, a.ID)
		}
	} else {
		if a.FinishedAt.IsZero() {
			return fmt.Errorf("%w: attempt %s finished_at is zero", ErrInvalidClaim, a.ID)
		}
		if a.FinishedAt.Before(a.StartedAt) {
			return fmt.Errorf("%w: attempt %s finished_at before started_at", ErrInvalidClaim, a.ID)
		}
	}
	if err := validateEvidence(a.Evidence); err != nil {
		return err
	}
	if a.Failure != nil {
		if err := a.Failure.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CandidateFinding is a model-proposed finding. It stays a candidate: it can be
// converted to domain.Finding, but only verify.RuleVerifier can produce
// VERIFIED.
type CandidateFinding struct {
	ID           string
	TaskID       domain.TaskID
	ProgramID    domain.ProgramID
	AssetID      *domain.AssetID
	Title        string
	VulnType     string
	Severity     string
	Summary      string
	Evidence     []domain.EvidenceID
	HypothesisID string
	Confidence   float64
	CreatedAt    time.Time
}

func (c CandidateFinding) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: candidate finding id is empty", ErrInvalidClaim)
	}
	if strings.TrimSpace(string(c.TaskID)) == "" || strings.TrimSpace(string(c.ProgramID)) == "" {
		return fmt.Errorf("%w: candidate finding %s task/program is empty", ErrInvalidClaim, c.ID)
	}
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.VulnType) == "" {
		return fmt.Errorf("%w: candidate finding %s title/vuln type is empty", ErrInvalidClaim, c.ID)
	}
	if c.CreatedAt.IsZero() {
		return fmt.Errorf("%w: candidate finding %s created_at is zero", ErrInvalidClaim, c.ID)
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("%w: candidate finding %s confidence out of range", ErrInvalidClaim, c.ID)
	}
	if err := validateEvidence(c.Evidence); err != nil {
		return err
	}
	if containsSecret(c.Title) || containsSecret(c.VulnType) ||
		containsSecret(c.Severity) || containsSecret(c.Summary) {
		return fmt.Errorf("%w: candidate finding %s text", ErrSecretDetected, c.ID)
	}
	return nil
}

// ToFinding maps a candidate to the domain object used by the verification and
// report pipeline. It never sets a VERIFIED status.
func (c CandidateFinding) ToFinding() domain.Finding {
	return domain.Finding{
		ID:        domain.FindingID(c.ID),
		TaskID:    c.TaskID,
		ProgramID: c.ProgramID,
		AssetID:   c.AssetID,
		Title:     c.Title,
		VulnType:  c.VulnType,
		Severity:  c.Severity,
		Evidence:  dedupeEvidence(c.Evidence),
		Status:    "CANDIDATE",
		CreatedAt: c.CreatedAt,
	}
}

// ResearchSession is the deterministic state a model may read (through the
// context compiler) but never writes directly. IDs are stable and all times are
// UTC.
type ResearchSession struct {
	ID                 string
	TaskID             domain.TaskID
	ProgramID          domain.ProgramID
	Objective          string
	KnownFacts         []FactRecord
	Unknowns           []Unknown
	Hypotheses         []Hypothesis
	Attempts           []Attempt
	FailedHypotheses   []Hypothesis
	CandidateFindings  []CandidateFinding
	RecentObservations []Observation
	Budget             Budget
	State              ResearchState
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewResearchSession creates a RUNNING session. An empty id is derived from the
// task ID and timestamp.
func NewResearchSession(id string, taskID domain.TaskID, at time.Time) *ResearchSession {
	at = utc(at)
	if strings.TrimSpace(id) == "" {
		id = StableID("sess", string(taskID), at.Format(time.RFC3339Nano))
	}
	return &ResearchSession{
		ID:        id,
		TaskID:    taskID,
		State:     StateRunning,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func (s *ResearchSession) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidClaim)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: session timestamps are zero", ErrInvalidClaim)
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: session updated_at before created_at", ErrInvalidClaim)
	}
	if !s.State.Valid() {
		return fmt.Errorf("%w: session state %q", ErrInvalidClaim, s.State)
	}
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if containsSecret(s.Objective) {
		return fmt.Errorf("%w: session objective", ErrSecretDetected)
	}
	for _, f := range s.KnownFacts {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	for _, u := range s.Unknowns {
		if err := u.Validate(); err != nil {
			return err
		}
	}
	for _, h := range s.Hypotheses {
		if err := h.Validate(); err != nil {
			return err
		}
	}
	for _, h := range s.FailedHypotheses {
		if err := h.Validate(); err != nil {
			return err
		}
	}
	for _, a := range s.Attempts {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	for _, c := range s.CandidateFindings {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	for _, o := range s.RecentObservations {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	if err := checkUniqueFactIDs(s.KnownFacts); err != nil {
		return err
	}
	if err := checkUniqueObservationIDs(s.RecentObservations); err != nil {
		return err
	}
	return nil
}

// AddObservation appends a validated observation and updates the session time.
func (s *ResearchSession) AddObservation(o Observation) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if strings.TrimSpace(o.ID) == "" {
		o.ID = StableID("obs", o.ToolName, o.ArgsHash, o.ResponseHash, utc(o.ObservedAt).Format(time.RFC3339Nano))
	}
	if o.ObservedAt.IsZero() {
		o.ObservedAt = s.UpdatedAt
	}
	if o.Sequence == 0 {
		o.Sequence = len(s.RecentObservations) + 1
	}
	if err := o.Validate(); err != nil {
		return err
	}
	s.RecentObservations = append(s.RecentObservations, o)
	s.touch(o.ObservedAt)
	return nil
}

// AddFact appends a validated fact record.
func (s *ResearchSession) AddFact(f FactRecord) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if strings.TrimSpace(f.ID) == "" {
		f.ID = StableID("fact", string(f.Claim.Level), f.Claim.Statement, utc(f.CreatedAt).Format(time.RFC3339Nano))
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = s.UpdatedAt
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = f.CreatedAt
	}
	if err := f.Validate(); err != nil {
		return err
	}
	s.KnownFacts = append(s.KnownFacts, f)
	s.touch(f.UpdatedAt)
	return nil
}

// AddHypothesis appends an open/supported hypothesis.
func (s *ResearchSession) AddHypothesis(h Hypothesis) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if strings.TrimSpace(h.ID) == "" {
		h.ID = StableID("hyp", h.Statement, utc(h.CreatedAt).Format(time.RFC3339Nano))
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = s.UpdatedAt
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = h.CreatedAt
	}
	if h.Status == "" {
		h.Status = HypothesisOpen
	}
	if err := h.Validate(); err != nil {
		return err
	}
	s.Hypotheses = append(s.Hypotheses, h)
	s.touch(h.UpdatedAt)
	return nil
}

// AddFailedHypothesis appends a REFUTED/ABANDONED hypothesis to the failure
// history. The status must be one of those two so an open hypothesis cannot be
// mistaken for a learned negative result.
func (s *ResearchSession) AddFailedHypothesis(h Hypothesis, at time.Time) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if h.Status != HypothesisRefuted && h.Status != HypothesisAbandoned {
		return fmt.Errorf("%w: failed hypothesis %s must be REFUTED or ABANDONED", ErrInvalidClaim, h.ID)
	}
	if strings.TrimSpace(h.ID) == "" {
		h.ID = StableID("hyp", h.Statement, utc(at).Format(time.RFC3339Nano))
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = utc(at)
	}
	h.UpdatedAt = utc(at)
	if err := h.Validate(); err != nil {
		return err
	}
	s.FailedHypotheses = append(s.FailedHypotheses, h)
	s.touch(h.UpdatedAt)
	return nil
}

// AddUnknown appends an explicit open question.
func (s *ResearchSession) AddUnknown(u Unknown) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = s.UpdatedAt
	}
	if strings.TrimSpace(u.ID) == "" {
		u.ID = StableID("unknown", u.Question, utc(u.CreatedAt).Format(time.RFC3339Nano))
	}
	if err := u.Validate(); err != nil {
		return err
	}
	s.Unknowns = append(s.Unknowns, u)
	s.touch(u.CreatedAt)
	return nil
}

// AddAttempt appends an executed/refused proposal record. A zero Status is a
// legacy record and is treated as COMPLETED; the loop writes STARTED
// explicitly before invoking a tool.
func (s *ResearchSession) AddAttempt(a Attempt) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if strings.TrimSpace(a.ID) == "" {
		a.ID = StableID("attempt", fmt.Sprint(a.Step), a.ToolName, a.ArgsHash, utc(a.StartedAt).Format(time.RFC3339Nano))
	}
	if a.Status == "" {
		a.Status = AttemptCompleted
	}
	if err := a.Validate(); err != nil {
		return err
	}
	s.Attempts = append(s.Attempts, a)
	if a.Status == AttemptStarted {
		s.touch(a.StartedAt)
	} else {
		s.touch(a.FinishedAt)
	}
	return nil
}

// ReplaceAttempt finalizes a previously appended STARTED attempt. The identity
// fields (ID, Step, ToolName, Action, ArgsHash, StartedAt) cannot be rewritten;
// only the outcome may be filled in.
func (s *ResearchSession) ReplaceAttempt(a Attempt) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if a.Status == "" {
		a.Status = AttemptCompleted
	}
	for i := range s.Attempts {
		cur := s.Attempts[i]
		if cur.ID != a.ID {
			continue
		}
		if cur.Step != a.Step || cur.ToolName != a.ToolName || cur.Action != a.Action || cur.ArgsHash != a.ArgsHash {
			return fmt.Errorf("%w: attempt %s identity cannot change", ErrInvalidClaim, a.ID)
		}
		if a.StartedAt.IsZero() {
			a.StartedAt = cur.StartedAt
		}
		if err := a.Validate(); err != nil {
			return err
		}
		s.Attempts[i] = a
		s.touch(a.FinishedAt)
		return nil
	}
	return fmt.Errorf("%w: attempt %s not found", ErrInvalidClaim, a.ID)
}

// HasInterruptedAttempt reports whether a previous run started the same
// tool+argument combination without recording its outcome. Replaying it could
// repeat a side effect, so the loop refuses to re-execute it automatically.
func (s *ResearchSession) HasInterruptedAttempt(tool, argsHash string) bool {
	if s == nil {
		return false
	}
	for _, a := range s.Attempts {
		if a.Status == AttemptInterrupted && a.ToolName == tool && a.ArgsHash == argsHash {
			return true
		}
	}
	return false
}

// interruptInFlight marks every STARTED attempt as INTERRUPTED. It returns true
// when something changed, so a caller can persist the transition.
func (s *ResearchSession) interruptInFlight(at time.Time) bool {
	if s == nil {
		return false
	}
	at = utc(at)
	changed := false
	for i := range s.Attempts {
		if s.Attempts[i].Status != AttemptStarted {
			continue
		}
		a := s.Attempts[i]
		a.Status = AttemptInterrupted
		if a.FinishedAt.IsZero() {
			if at.IsZero() {
				a.FinishedAt = a.StartedAt
			} else {
				a.FinishedAt = at
			}
		}
		s.Attempts[i] = a
		s.touch(a.FinishedAt)
		changed = true
	}
	return changed
}

// AddCandidateFinding appends a candidate. It remains a candidate until the
// external verifier decides otherwise.
func (s *ResearchSession) AddCandidateFinding(c CandidateFinding) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.UpdatedAt
	}
	if strings.TrimSpace(c.ID) == "" {
		c.ID = StableID("candidate", string(c.TaskID), c.Title, utc(c.CreatedAt).Format(time.RFC3339Nano))
	}
	if err := c.Validate(); err != nil {
		return err
	}
	s.CandidateFindings = append(s.CandidateFindings, c)
	s.touch(c.CreatedAt)
	return nil
}

// Resume reopens a STALLED or PARKED session for another loop run. Budget,
// attempts, observations and failed-hypothesis history are preserved, so the
// next run starts from what was already learned instead of repeating it. Only
// the deterministic layer may resume a session; a model proposal cannot.
func (s *ResearchSession) Resume(at time.Time) error {
	if s == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidClaim)
	}
	switch s.State {
	case StateStalled, StateParked:
	default:
		return fmt.Errorf("%w: state is %s", ErrInvalidResume, s.State)
	}
	// A new run cannot still have an execution in flight. Mark it interrupted
	// so the outcome is treated as unknown and the same call is not replayed.
	s.interruptInFlight(at)
	s.State = StateRunning
	s.touch(at)
	return s.Validate()
}

func (s *ResearchSession) touch(at time.Time) {
	at = utc(at)
	if at.IsZero() {
		return
	}
	if at.After(s.UpdatedAt) {
		s.UpdatedAt = at
	}
}

// StableID derives a deterministic identifier from its parts. It is safe to use
// for record IDs that must survive a replay.
func StableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "/" + hex.EncodeToString(h.Sum(nil))[:16]
}

// HashString returns a stable SHA-256 hash for a response/observation body.
func HashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashArgs normalizes JSON parameters (object key order and whitespace) and
// returns a stable hash. Invalid/empty JSON is hashed as raw bytes.
func HashArgs(args []byte) string {
	return "sha256:" + domain.CanonicalParametersHash(args)
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}

func containsSecret(text string) bool {
	return len(redact.Scan(text)) > 0
}

func validateEvidence(ids []domain.EvidenceID) error {
	for _, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			return fmt.Errorf("%w: empty evidence id", ErrInvalidClaim)
		}
	}
	return nil
}

func dedupeEvidence(ids []domain.EvidenceID) []domain.EvidenceID {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[domain.EvidenceID]struct{}, len(ids))
	out := make([]domain.EvidenceID, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func mergeEvidence(a, b []domain.EvidenceID) []domain.EvidenceID {
	out := make([]domain.EvidenceID, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return dedupeEvidence(out)
}

func checkUniqueFactIDs(facts []FactRecord) error {
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		if _, ok := seen[f.ID]; ok {
			return fmt.Errorf("%w: duplicate fact id %s", ErrInvalidClaim, f.ID)
		}
		seen[f.ID] = struct{}{}
	}
	return nil
}

func checkUniqueObservationIDs(observations []Observation) error {
	seen := make(map[string]struct{}, len(observations))
	for _, o := range observations {
		if _, ok := seen[o.ID]; ok {
			return fmt.Errorf("%w: duplicate observation id %s", ErrInvalidClaim, o.ID)
		}
		seen[o.ID] = struct{}{}
	}
	return nil
}
