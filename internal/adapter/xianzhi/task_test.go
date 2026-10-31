package xianzhi

import (
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

var testNow = time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)

func sampleTask() taskCard {
	return taskCard{
		ID: 324, Name: "2026年10月众测活动", Status: 4, Type: 1,
		RegistrationBegin: 1792137353000, RegistrationEnd: 1792569357000,
		TaskBegin: 1792342532000, TaskEnd: 1793462145000,
		RegistrationLimit: 30, Bounty: "20 - 3375",
		Buttons: []string{"我要报名", "查看详情"},
		Title:   "2026年10月众测活动",
	}
}

func TestMapTaskStatus(t *testing.T) {
	cases := map[int]domain.ProgramStatus{
		4: domain.StatusRunning,
		5: domain.StatusEnded,
		6: domain.StatusPaused,
		9: domain.StatusUnknown,
	}
	for in, want := range cases {
		if got := mapTaskStatus(in); got != want {
			t.Fatalf("status %d = %s, want %s", in, got, want)
		}
	}
}

func TestMapTaskType(t *testing.T) {
	if got, err := mapTaskType(1); err != nil || got != domain.TypeBrandSRC {
		t.Fatalf("type 1 = %s, %v", got, err)
	}
	if got, err := mapTaskType(2); err != nil || got != domain.TypeCrowdTest {
		t.Fatalf("type 2 = %s, %v", got, err)
	}
	if _, err := mapTaskType(9); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("type 9 err = %v", err)
	}
}

func TestParseBounty(t *testing.T) {
	cases := []struct {
		in       string
		min, max int64
	}{
		{"20 - 3375", 20, 3375},
		{"¥100-¥2000", 100, 2000},
		{"-", 0, 0},
		{"", 0, 0},
	}
	for _, c := range cases {
		min, max := parseBounty(c.in)
		if min != c.min || max != c.max {
			t.Fatalf("parseBounty(%q) = %d,%d, want %d,%d", c.in, min, max, c.min, c.max)
		}
	}
}

func TestTaskProgramRunningGated(t *testing.T) {
	prog, fs, err := taskProgram(sampleTask(), "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.ID != "xianzhi/324" || prog.Type != domain.TypeBrandSRC || prog.Status.Status != domain.StatusRunning {
		t.Fatalf("program = %+v", prog)
	}
	if prog.Eligibility.Decision != domain.HumanActionNeeded {
		t.Fatalf("eligibility = %s, want HUMAN_ACTION_REQUIRED", prog.Eligibility.Decision)
	}
	if prog.Scope.Visibility != domain.ScopeGatedBySignup || prog.Scope.Access != domain.ScopeAccessUnknown || prog.Scope.ContentsKnown {
		t.Fatalf("scope = %+v", prog.Scope)
	}
	if prog.Reward.Min == nil || prog.Reward.Min.Amount != 20 || prog.Reward.Max == nil || prog.Reward.Max.Amount != 3375 {
		t.Fatalf("reward = %+v", prog.Reward)
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program invalid: %v", err)
	}
	if len(prog.Status.Inputs) == 0 || len(prog.Evidence) == 0 {
		t.Fatal("program lost provenance")
	}

	wantStart := time.UnixMilli(sampleTask().TaskBegin).UTC()
	wantRegStart := time.UnixMilli(sampleTask().RegistrationBegin).UTC()
	var seenStatus, seenStart, seenRegStart, seenCanonicalRegStart bool
	for _, f := range fs {
		if len(f.EvidenceRefs) == 0 {
			t.Fatalf("fact without evidence: %+v", f)
		}
		switch f.Predicate {
		case facts.PredProgramStatus:
			seenStatus = f.Value == "RUNNING"
		case facts.PredProgramStart:
			seenStart = f.Value == wantStart
		case "xianzhi.task.registration_start":
			seenRegStart = f.Value == wantRegStart
		case facts.PredRegistrationStart:
			seenCanonicalRegStart = f.Value == wantRegStart
		}
	}
	if !seenStatus || !seenStart || !seenRegStart || !seenCanonicalRegStart {
		t.Fatalf("missing facts: status=%v start=%v regStart=%v canonicalRegStart=%v", seenStatus, seenStart, seenRegStart, seenCanonicalRegStart)
	}
}

func TestTaskProgramEndedUnknownEligibility(t *testing.T) {
	c := sampleTask()
	c.Status = 5
	c.Buttons = nil
	prog, _, err := taskProgram(c, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Status.Status != domain.StatusEnded {
		t.Fatalf("status = %s", prog.Status.Status)
	}
	if prog.Eligibility.Decision != domain.EligibilityUnknown {
		t.Fatalf("eligibility = %s, want UNKNOWN", prog.Eligibility.Decision)
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program invalid: %v", err)
	}
}

func TestTaskProgramPaused(t *testing.T) {
	c := sampleTask()
	c.Status = 6
	prog, fs, err := taskProgram(c, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Status.Status != domain.StatusPaused {
		t.Fatalf("status = %s", prog.Status.Status)
	}
	found := false
	for _, f := range fs {
		if f.Predicate == facts.PredProgramPaused && f.Value == true {
			found = true
		}
	}
	if !found {
		t.Fatal("missing paused fact")
	}
}

func TestTaskProgramUnknownType(t *testing.T) {
	c := sampleTask()
	c.Type = 9
	if _, _, err := taskProgram(c, "ev1", testNow); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
