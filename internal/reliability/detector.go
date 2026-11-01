package reliability

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// FailureSignature is the normalized identity of a failed call: tool name,
// error class and argument hash. It is deliberately safe to render: it contains
// no raw argument values.
type FailureSignature struct {
	ToolName   string
	ErrorClass string
	ArgsHash   string
}

// NewFailureSignature normalizes tool/error names and hashes the raw arguments.
// The raw bytes are never retained.
func NewFailureSignature(tool, errorClass string, args []byte) FailureSignature {
	return FailureSignature{
		ToolName:   normalizeIdent(tool, false),
		ErrorClass: normalizeIdent(errorClass, true),
		ArgsHash:   HashArgs(args),
	}
}

// NewFailureSignatureFromHash is for callers that already hold an argument hash
// (for example an Observation.ArgsHash).
func NewFailureSignatureFromHash(tool, errorClass, argsHash string) FailureSignature {
	return FailureSignature{
		ToolName:   normalizeIdent(tool, false),
		ErrorClass: normalizeIdent(errorClass, true),
		ArgsHash:   normalizeArgsHash(argsHash),
	}
}

// NormalizeFailureSignature returns the canonical form of a signature.
func NormalizeFailureSignature(f FailureSignature) FailureSignature {
	return FailureSignature{
		ToolName:   normalizeIdent(f.ToolName, false),
		ErrorClass: normalizeIdent(f.ErrorClass, true),
		ArgsHash:   normalizeArgsHash(f.ArgsHash),
	}
}

// Validate rejects a signature that cannot identify a failure.
func (f FailureSignature) Validate() error {
	n := NormalizeFailureSignature(f)
	if n.ToolName == "" {
		return fmt.Errorf("%w: failure signature tool is empty", ErrInvalidClaim)
	}
	if n.ErrorClass == "" {
		return fmt.Errorf("%w: failure signature error class is empty", ErrInvalidClaim)
	}
	if n.ArgsHash == "" {
		return fmt.Errorf("%w: failure signature args hash is empty", ErrInvalidClaim)
	}
	return nil
}

// Key is the stable comparison key used by the detector and compiler.
func (f FailureSignature) Key() string {
	n := NormalizeFailureSignature(f)
	return n.ToolName + "|" + n.ErrorClass + "|" + n.ArgsHash
}

// Equal compares normalized signatures.
func (f FailureSignature) Equal(other FailureSignature) bool {
	return f.Key() == other.Key()
}

// DetectorVerdict is the detector's judgement for one step.
type DetectorVerdict string

const (
	DetectionProgress DetectorVerdict = "PROGRESS"
	DetectionWarning  DetectorVerdict = "WARNING"
	DetectionStalled  DetectorVerdict = "STALLED"
)

func (v DetectorVerdict) Valid() bool {
	switch v {
	case DetectionProgress, DetectionWarning, DetectionStalled:
		return true
	default:
		return false
	}
}

// DetectorAction is the deterministic recovery action attached to a verdict.
type DetectorAction string

const (
	ActionNone             DetectorAction = ""
	ActionSummarize        DetectorAction = "SUMMARIZE"
	ActionForceAlternative DetectorAction = "FORCE_ALTERNATIVE"
	ActionHandoff          DetectorAction = "HANDOFF"
	ActionEscalate         DetectorAction = "ESCALATE"
	ActionPark             DetectorAction = "PARK"
)

func (a DetectorAction) Valid() bool {
	switch a {
	case ActionNone, ActionSummarize, ActionForceAlternative, ActionHandoff, ActionEscalate, ActionPark:
		return true
	default:
		return false
	}
}

// DetectorConfig controls thresholds. WarnThreshold defaults to 3 and
// StallThreshold to 5. NoProgressThreshold defaults to StallThreshold.
type DetectorConfig struct {
	WarnThreshold       int
	StallThreshold      int
	NoProgressThreshold int
	// MaxTokens and MaxToolCalls are optional absolute budget ceilings. Reaching
	// either parks the run even if the call pattern is not otherwise repeated.
	MaxTokens    int
	MaxToolCalls int
}

func (c DetectorConfig) normalized() DetectorConfig {
	if c.WarnThreshold <= 0 {
		c.WarnThreshold = 3
	}
	if c.StallThreshold <= c.WarnThreshold {
		c.StallThreshold = c.WarnThreshold + 2
	}
	if c.NoProgressThreshold <= 0 {
		c.NoProgressThreshold = c.StallThreshold
	}
	if c.MaxTokens < 0 {
		c.MaxTokens = 0
	}
	if c.MaxToolCalls < 0 {
		c.MaxToolCalls = 0
	}
	return c
}

// StepSignal is the deterministic summary of one executed step. Raw arguments
// are represented only by ArgsHash.
type StepSignal struct {
	ToolName       string
	ArgsHash       string
	ResponseHash   string
	ErrorSignature *FailureSignature
	HypothesisID   string
	Evidence       []domain.EvidenceID
	FactID         string
	TokensUsed     int
	ToolCallsUsed  int
	At             time.Time
}

// Detection is the detector's output for one step.
type Detection struct {
	Verdict DetectorVerdict
	Action  DetectorAction
	// Reasons are stable, machine-readable labels with counts.
	Reasons []string

	RepeatToolArgs int
	RepeatResponse int
	RepeatError    int
	SameHypothesis int

	NoNewEvidence int
	NoNewFact     int
	NoProgress    int

	TokensUsed             int
	ToolCallsUsed          int
	TokensSinceProgress    int
	ToolCallsSinceProgress int

	CurrentFailure *FailureSignature
	At             time.Time
}

// LoopDetector tracks local repetition and lack of progress. A detector is
// stateful and intended for one loop; call Reset to reuse it.
type LoopDetector struct {
	cfg DetectorConfig

	seenEvidence   map[domain.EvidenceID]struct{}
	seenFacts      map[string]struct{}
	seenHypotheses map[string]struct{}

	lastActionKey  string
	lastResponse   string
	lastErrorKey   string
	lastHypothesis string

	actionStreak     int
	responseStreak   int
	errorStreak      int
	hypothesisStreak int

	noEvidenceStreak int
	noFactStreak     int
	noProgressStreak int

	lastTokens             int
	lastToolCalls          int
	tokensSinceProgress    int
	toolCallsSinceProgress int
	steps                  int
}

// StallDetector is an alias for LoopDetector.
type StallDetector = LoopDetector

// NewLoopDetector creates a detector with normalized thresholds.
func NewLoopDetector(cfg DetectorConfig) *LoopDetector {
	d := &LoopDetector{cfg: cfg.normalized()}
	d.Reset()
	return d
}

// NewStallDetector is an alias constructor.
func NewStallDetector(cfg DetectorConfig) *LoopDetector {
	return NewLoopDetector(cfg)
}

// Config returns the normalized configuration.
func (d *LoopDetector) Config() DetectorConfig {
	if d == nil {
		return DetectorConfig{}.normalized()
	}
	return d.cfg
}

// Steps returns the number of observed steps.
func (d *LoopDetector) Steps() int {
	if d == nil {
		return 0
	}
	return d.steps
}

// Reset clears all history while keeping the configuration.
func (d *LoopDetector) Reset() {
	if d == nil {
		return
	}
	d.seenEvidence = make(map[domain.EvidenceID]struct{})
	d.seenFacts = make(map[string]struct{})
	d.seenHypotheses = make(map[string]struct{})
	d.lastActionKey = ""
	d.lastResponse = ""
	d.lastErrorKey = ""
	d.lastHypothesis = ""
	d.actionStreak = 0
	d.responseStreak = 0
	d.errorStreak = 0
	d.hypothesisStreak = 0
	d.noEvidenceStreak = 0
	d.noFactStreak = 0
	d.noProgressStreak = 0
	d.lastTokens = 0
	d.lastToolCalls = 0
	d.tokensSinceProgress = 0
	d.toolCallsSinceProgress = 0
	d.steps = 0
}

// Observe records one step and returns a verdict. Progress (new evidence or a
// new fact) resets every streak, so a run recovers immediately after it learns
// something.
func (d *LoopDetector) Observe(sig StepSignal) Detection {
	if d == nil {
		return Detection{Verdict: DetectionProgress, Action: ActionNone, At: sig.At}
	}
	d.steps++
	det := Detection{
		Verdict:       DetectionProgress,
		Action:        ActionNone,
		Reasons:       []string{},
		At:            sig.At,
		TokensUsed:    sig.TokensUsed,
		ToolCallsUsed: sig.ToolCallsUsed,
	}
	if d.seenEvidence == nil {
		d.Reset()
	}

	if sig.TokensUsed > d.lastTokens {
		d.tokensSinceProgress += sig.TokensUsed - d.lastTokens
	}
	if sig.ToolCallsUsed > d.lastToolCalls {
		d.toolCallsSinceProgress += sig.ToolCallsUsed - d.lastToolCalls
	}
	d.lastTokens = sig.TokensUsed
	d.lastToolCalls = sig.ToolCallsUsed
	if sig.ErrorSignature != nil {
		f := NormalizeFailureSignature(*sig.ErrorSignature)
		det.CurrentFailure = &f
	}

	newEvidence := false
	for _, id := range sig.Evidence {
		if strings.TrimSpace(string(id)) == "" {
			continue
		}
		if _, ok := d.seenEvidence[id]; !ok {
			d.seenEvidence[id] = struct{}{}
			newEvidence = true
		}
	}
	newFact := false
	if id := strings.TrimSpace(sig.FactID); id != "" {
		if _, ok := d.seenFacts[id]; !ok {
			d.seenFacts[id] = struct{}{}
			newFact = true
		}
	}
	newHypothesis := false
	if id := strings.TrimSpace(sig.HypothesisID); id != "" {
		if _, ok := d.seenHypotheses[id]; !ok {
			d.seenHypotheses[id] = struct{}{}
			newHypothesis = true
		}
	}

	if newEvidence || newFact {
		d.actionStreak = 0
		d.responseStreak = 0
		d.errorStreak = 0
		d.hypothesisStreak = 0
		d.noEvidenceStreak = 0
		d.noFactStreak = 0
		d.noProgressStreak = 0
		d.lastActionKey = ""
		d.lastResponse = ""
		d.lastErrorKey = ""
		d.lastHypothesis = ""
		d.tokensSinceProgress = 0
		d.toolCallsSinceProgress = 0
		if newEvidence {
			det.Reasons = append(det.Reasons, "NEW_EVIDENCE")
		}
		if newFact {
			det.Reasons = append(det.Reasons, "NEW_FACT")
		}
		if newHypothesis {
			det.Reasons = append(det.Reasons, "NEW_HYPOTHESIS")
		}
		return det
	}

	// Repetition streaks. A different argument set starts a new streak, which is
	// why two alternating calls never count as five repeats.
	actionKey := strings.TrimSpace(sig.ToolName) + "|" + strings.TrimSpace(sig.ArgsHash)
	if strings.TrimSpace(sig.ArgsHash) == "" {
		d.actionStreak = 0
		d.lastActionKey = ""
	} else if actionKey == d.lastActionKey {
		d.actionStreak++
	} else {
		d.actionStreak = 1
		d.lastActionKey = actionKey
	}

	if strings.TrimSpace(sig.ResponseHash) == "" {
		d.responseStreak = 0
		d.lastResponse = ""
	} else if sig.ResponseHash == d.lastResponse {
		d.responseStreak++
	} else {
		d.responseStreak = 1
		d.lastResponse = sig.ResponseHash
	}

	if sig.ErrorSignature == nil {
		d.errorStreak = 0
		d.lastErrorKey = ""
	} else {
		key := NormalizeFailureSignature(*sig.ErrorSignature).Key()
		if key == d.lastErrorKey {
			d.errorStreak++
		} else {
			d.errorStreak = 1
			d.lastErrorKey = key
		}
	}

	if strings.TrimSpace(sig.HypothesisID) == "" {
		d.hypothesisStreak = 0
		d.lastHypothesis = ""
	} else if sig.HypothesisID == d.lastHypothesis {
		d.hypothesisStreak++
	} else {
		d.hypothesisStreak = 1
		d.lastHypothesis = sig.HypothesisID
	}

	d.noEvidenceStreak++
	d.noFactStreak++
	d.noProgressStreak++

	det.RepeatToolArgs = d.actionStreak
	det.RepeatResponse = d.responseStreak
	det.RepeatError = d.errorStreak
	det.SameHypothesis = d.hypothesisStreak
	det.NoNewEvidence = d.noEvidenceStreak
	det.NoNewFact = d.noFactStreak
	det.NoProgress = d.noProgressStreak
	det.TokensSinceProgress = d.tokensSinceProgress
	det.ToolCallsSinceProgress = d.toolCallsSinceProgress

	maxRepeat := d.actionStreak
	if d.responseStreak > maxRepeat {
		maxRepeat = d.responseStreak
	}
	if d.errorStreak > maxRepeat {
		maxRepeat = d.errorStreak
	}
	if d.hypothesisStreak > maxRepeat {
		maxRepeat = d.hypothesisStreak
	}

	switch {
	case d.cfg.MaxTokens > 0 && sig.TokensUsed >= d.cfg.MaxTokens:
		det.Verdict = DetectionStalled
		det.Action = ActionPark
		det.Reasons = append(det.Reasons, fmt.Sprintf("TOKEN_BUDGET_REACHED:%d/%d", sig.TokensUsed, d.cfg.MaxTokens))
	case d.cfg.MaxToolCalls > 0 && sig.ToolCallsUsed >= d.cfg.MaxToolCalls:
		det.Verdict = DetectionStalled
		det.Action = ActionPark
		det.Reasons = append(det.Reasons, fmt.Sprintf("TOOL_BUDGET_REACHED:%d/%d", sig.ToolCallsUsed, d.cfg.MaxToolCalls))
	case d.errorStreak >= d.cfg.StallThreshold:
		det.Verdict = DetectionStalled
		det.Action = ActionEscalate
		det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_ERROR:%d", d.errorStreak))
	case d.actionStreak >= d.cfg.StallThreshold:
		det.Verdict = DetectionStalled
		det.Action = ActionForceAlternative
		det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_TOOL_ARGS:%d", d.actionStreak))
	case d.responseStreak >= d.cfg.StallThreshold:
		det.Verdict = DetectionStalled
		det.Action = ActionHandoff
		det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_RESPONSE:%d", d.responseStreak))
	case d.hypothesisStreak >= d.cfg.StallThreshold:
		det.Verdict = DetectionStalled
		det.Action = ActionEscalate
		det.Reasons = append(det.Reasons, fmt.Sprintf("SAME_HYPOTHESIS:%d", d.hypothesisStreak))
	case d.noProgressStreak >= d.cfg.NoProgressThreshold:
		det.Verdict = DetectionStalled
		det.Action = ActionHandoff
		det.Reasons = append(det.Reasons,
			fmt.Sprintf("NO_PROGRESS:%d", d.noProgressStreak),
			fmt.Sprintf("NO_NEW_EVIDENCE:%d", d.noEvidenceStreak),
			fmt.Sprintf("NO_NEW_FACT:%d", d.noFactStreak))
	case maxRepeat >= d.cfg.WarnThreshold:
		det.Verdict = DetectionWarning
		det.Action = ActionSummarize
		switch {
		case d.errorStreak >= d.cfg.WarnThreshold:
			det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_ERROR:%d", d.errorStreak))
		case d.actionStreak >= d.cfg.WarnThreshold:
			det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_TOOL_ARGS:%d", d.actionStreak))
		case d.responseStreak >= d.cfg.WarnThreshold:
			det.Reasons = append(det.Reasons, fmt.Sprintf("REPEATED_RESPONSE:%d", d.responseStreak))
		default:
			det.Reasons = append(det.Reasons, fmt.Sprintf("SAME_HYPOTHESIS:%d", d.hypothesisStreak))
		}
	default:
		det.Reasons = append(det.Reasons, "NO_PROGRESS")
	}
	return det
}

func normalizeIdent(s string, upper bool) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	lastSeparator := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			if upper {
				r = unicode.ToUpper(r)
			} else {
				r = unicode.ToLower(r)
			}
			b.WriteRune(r)
			lastSeparator = false
		default:
			if !lastSeparator {
				b.WriteByte('_')
				lastSeparator = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizeArgsHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return HashArgs(nil)
	}
	if strings.HasPrefix(hash, "sha256:") {
		return "sha256:" + strings.ToLower(strings.TrimPrefix(hash, "sha256:"))
	}
	if len(hash) == 64 && isHex(hash) {
		return "sha256:" + strings.ToLower(hash)
	}
	// The caller supplied something that is not a hash. Hash it rather than
	// retaining raw arguments in the signature.
	return HashArgs([]byte(hash))
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
