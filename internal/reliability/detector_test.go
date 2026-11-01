package reliability

import (
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func stepSignal(tool, args, response string, evidence []domain.EvidenceID, fact string) StepSignal {
	return StepSignal{
		ToolName:     tool,
		ArgsHash:     HashArgs([]byte(args)),
		ResponseHash: HashString(response),
		Evidence:     evidence,
		FactID:       fact,
		At:           testNow,
	}
}

func TestDetectorWarningAtThreeAndStalledAtFive(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	if got := d.Config(); got.WarnThreshold != 3 || got.StallThreshold != 5 {
		t.Fatalf("default thresholds = %+v, want 3/5", got)
	}
	sig := stepSignal("read_scope", `{"asset":"a"}`, "same response", nil, "")
	for i := 1; i <= 4; i++ {
		det := d.Observe(sig)
		if det.RepeatToolArgs != i {
			t.Fatalf("step %d repeat = %d, want %d", i, det.RepeatToolArgs, i)
		}
		want := DetectionProgress
		if i >= 3 {
			want = DetectionWarning
		}
		if det.Verdict != want {
			t.Fatalf("step %d verdict = %s, want %s", i, det.Verdict, want)
		}
		if want == DetectionWarning && det.Action != ActionSummarize {
			t.Fatalf("step %d warning action = %s", i, det.Action)
		}
	}
	det := d.Observe(sig)
	if det.Verdict != DetectionStalled {
		t.Fatalf("step 5 verdict = %s, want STALLED", det.Verdict)
	}
	if det.Action != ActionForceAlternative {
		t.Fatalf("step 5 action = %s, want FORCE_ALTERNATIVE", det.Action)
	}
	if det.RepeatToolArgs != 5 {
		t.Fatalf("repeat = %d, want 5", det.RepeatToolArgs)
	}
}

func TestDetectorDifferentArgsDoNotCountAsRepeats(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	steps := []StepSignal{
		stepSignal("read_scope", `{"asset":"a"}`, "response-a", nil, ""),
		stepSignal("read_scope", `{"asset":"b"}`, "response-b", nil, ""),
		stepSignal("read_scope", `{"asset":"a"}`, "response-c", nil, ""),
		stepSignal("read_scope", `{"asset":"b"}`, "response-d", nil, ""),
	}
	for i, sig := range steps {
		det := d.Observe(sig)
		if det.Verdict != DetectionProgress {
			t.Fatalf("step %d verdict = %s (%v), want PROGRESS", i+1, det.Verdict, det.Reasons)
		}
		if det.RepeatToolArgs > 1 {
			t.Fatalf("step %d repeated args = %d, want <=1", i+1, det.RepeatToolArgs)
		}
	}
}

func TestDetectorNoProgressStall(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	for i := 1; i <= 4; i++ {
		det := d.Observe(stepSignal("tool", `{"i":`+itoa(i)+`}`, "response-"+itoa(i), nil, ""))
		if det.Verdict == DetectionStalled {
			t.Fatalf("step %d stalled too early: %v", i, det.Reasons)
		}
	}
	det := d.Observe(stepSignal("tool", `{"i":5}`, "response-5", nil, ""))
	if det.Verdict != DetectionStalled {
		t.Fatalf("step 5 verdict = %s, want STALLED", det.Verdict)
	}
	if det.Action != ActionHandoff {
		t.Fatalf("action = %s, want HANDOFF", det.Action)
	}
	if det.NoProgress != 5 || det.NoNewEvidence != 5 || det.NoNewFact != 5 {
		t.Fatalf("no-progress counters = %d/%d/%d", det.NoProgress, det.NoNewEvidence, det.NoNewFact)
	}
}

func TestDetectorRecoversAfterNewEvidenceOrFact(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	sig := stepSignal("read_scope", `{"asset":"a"}`, "same response", nil, "")
	for i := 0; i < 3; i++ {
		if det := d.Observe(sig); det.Verdict != DetectionWarning && i == 2 {
			t.Fatalf("expected warning at 3, got %s", det.Verdict)
		}
	}

	withEvidence := sig
	withEvidence.Evidence = []domain.EvidenceID{"ev-new"}
	det := d.Observe(withEvidence)
	if det.Verdict != DetectionProgress {
		t.Fatalf("new evidence verdict = %s, want PROGRESS", det.Verdict)
	}
	if det.RepeatToolArgs != 0 {
		t.Fatalf("streaks were not reset: repeat=%d", det.RepeatToolArgs)
	}

	// The next identical call starts a fresh streak instead of continuing at 4.
	det = d.Observe(sig)
	if det.Verdict != DetectionProgress || det.RepeatToolArgs != 1 {
		t.Fatalf("after recovery: verdict=%s repeat=%d", det.Verdict, det.RepeatToolArgs)
	}
	if det := d.Observe(sig); det.Verdict != DetectionProgress {
		t.Fatalf("second recovered step = %s", det.Verdict)
	}
	if det := d.Observe(sig); det.Verdict != DetectionWarning {
		t.Fatalf("third recovered step = %s, want WARNING", det.Verdict)
	}

	// A new fact also counts as progress even without evidence.
	factSignal := stepSignal("tool", `{"asset":"b"}`, "new response", nil, "fact-new")
	if det := d.Observe(factSignal); det.Verdict != DetectionProgress {
		t.Fatalf("new fact verdict = %s", det.Verdict)
	}
}

func TestDetectorRepeatedErrorEscalates(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	failure := NewFailureSignature("read_scope", "TOOL_TIMEOUT", []byte(`{"asset":"a"}`))
	sig := stepSignal("read_scope", `{"asset":"a"}`, "error", nil, "")
	sig.ErrorSignature = &failure
	for i := 1; i <= 4; i++ {
		if det := d.Observe(sig); det.Verdict == DetectionStalled {
			t.Fatalf("stalled too early at %d", i)
		}
	}
	det := d.Observe(sig)
	if det.Verdict != DetectionStalled || det.Action != ActionEscalate {
		t.Fatalf("step 5 = %s/%s, want STALLED/ESCALATE", det.Verdict, det.Action)
	}
	if det.RepeatError != 5 {
		t.Fatalf("repeat error = %d", det.RepeatError)
	}
}

func TestDetectorSameHypothesisEscalates(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{})
	for i := 1; i <= 5; i++ {
		sig := stepSignal("tool", `{"i":`+itoa(i)+`}`, "response-"+itoa(i), nil, "")
		sig.HypothesisID = "hyp-1"
		det := d.Observe(sig)
		if i < 5 {
			continue
		}
		if det.Verdict != DetectionStalled || det.Action != ActionEscalate {
			t.Fatalf("step 5 = %s/%s, want STALLED/ESCALATE", det.Verdict, det.Action)
		}
		if det.SameHypothesis != 5 {
			t.Fatalf("same hypothesis = %d", det.SameHypothesis)
		}
	}
}

func TestDetectorTokenAndToolBudget(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{MaxToolCalls: 2})
	det := d.Observe(StepSignal{ToolName: "a", ArgsHash: HashArgs([]byte(`{"a":1}`)), ToolCallsUsed: 1})
	if det.Verdict != DetectionProgress {
		t.Fatalf("first step = %s", det.Verdict)
	}
	det = d.Observe(StepSignal{ToolName: "b", ArgsHash: HashArgs([]byte(`{"b":1}`)), ToolCallsUsed: 2})
	if det.Verdict != DetectionStalled || det.Action != ActionPark {
		t.Fatalf("tool budget = %s/%s", det.Verdict, det.Action)
	}

	d = NewLoopDetector(DetectorConfig{MaxTokens: 100})
	det = d.Observe(StepSignal{ToolName: "a", ArgsHash: HashArgs([]byte(`{"a":1}`)), TokensUsed: 60})
	if det.Verdict != DetectionProgress {
		t.Fatalf("token step 1 = %s", det.Verdict)
	}
	det = d.Observe(StepSignal{ToolName: "a", ArgsHash: HashArgs([]byte(`{"a":2}`)), TokensUsed: 120})
	if det.Verdict != DetectionStalled || det.Action != ActionPark {
		t.Fatalf("token budget = %s/%s", det.Verdict, det.Action)
	}
}

func TestDetectorThresholdsAreConfigurableAndResetWorks(t *testing.T) {
	d := NewLoopDetector(DetectorConfig{WarnThreshold: 2, StallThreshold: 4, NoProgressThreshold: 4})
	sig := stepSignal("tool", `{"a":1}`, "response", nil, "")
	if det := d.Observe(sig); det.Verdict != DetectionProgress {
		t.Fatalf("step 1 = %s", det.Verdict)
	}
	if det := d.Observe(sig); det.Verdict != DetectionWarning {
		t.Fatalf("step 2 = %s, want WARNING at configured threshold", det.Verdict)
	}
	if det := d.Observe(sig); det.Verdict != DetectionWarning {
		t.Fatalf("step 3 = %s", det.Verdict)
	}
	if det := d.Observe(sig); det.Verdict != DetectionStalled {
		t.Fatalf("step 4 = %s, want STALLED at configured threshold", det.Verdict)
	}

	d.Reset()
	if det := d.Observe(sig); det.Verdict != DetectionProgress || det.RepeatToolArgs != 1 {
		t.Fatalf("after reset = %s repeat=%d", det.Verdict, det.RepeatToolArgs)
	}
}

func TestFailureSignatureNormalization(t *testing.T) {
	a := NewFailureSignature(" Read_Scope ", "invalid argument", []byte(`{"a":1,"b":2}`))
	if a.ToolName != "read_scope" || a.ErrorClass != "INVALID_ARGUMENT" {
		t.Fatalf("normalized = %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	b := NewFailureSignature("read_scope", "INVALID_ARGUMENT", []byte(`{"b":2,"a":1}`))
	if !a.Equal(b) {
		t.Fatalf("canonical args should be equal:\n%+v\n%+v", a, b)
	}
	c := NewFailureSignature("read_scope", "INVALID_ARGUMENT", []byte(`{"a":9,"b":2}`))
	if a.Equal(c) {
		t.Fatal("different args produced the same signature")
	}
	if a.Key() == c.Key() {
		t.Fatal("different args produced the same key")
	}
	raw := NewFailureSignature("tool", "ERROR", []byte(`{"password":"hunter2"}`))
	if len(raw.Key()) == 0 {
		t.Fatal("empty key")
	}
	for _, part := range []string{"hunter2", "password"} {
		if containsSubstring(raw.Key(), part) {
			t.Fatalf("signature leaked raw args: %s", raw.Key())
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
