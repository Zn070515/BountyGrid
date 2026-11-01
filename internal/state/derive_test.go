package state

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	butianfacts "github.com/Zn070515/BountyGrid/internal/facts/butian"
)

func growFacts(t *testing.T) facts.FactSet {
	t.Helper()
	pid := domain.ProgramID("butian:grow")
	in := facts.Input{
		Platform:   domain.PlatformButian,
		ProgramID:  &pid,
		Text:       "AI 辅助漏洞挖掘规则\n报名时间：2026.8.25~2026.11.30\n活动时间：2026.9.1~2026.11.30\n2026GROW计划漏洞挖掘实战-第二期 2026/10/01 - 2026/10/31",
		CapturedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		EvidenceID: "ev-grow",
	}
	fs, err := (butianfacts.GrowExtractor{}).Extract(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return facts.NewFactSet(fs)
}

func TestGrowPhaseDoesNotEndProgram(t *testing.T) {
	in, err := ProgramInputFromFacts(growFacts(t))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	st, err := DeriveProgramState(in, at, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusRunning {
		t.Fatalf("status = %s, want RUNNING (phase end 10/31 must not end GROW)", st.Status)
	}
	open, err := DeriveRegistrationOpen(in, at)
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Fatal("registration should be open on 2026-11-15")
	}
}

func TestGrowEndsAfterActivityEnd(t *testing.T) {
	in, err := ProgramInputFromFacts(growFacts(t))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	st, err := DeriveProgramState(in, at, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusEnded {
		t.Fatalf("status = %s, want ENDED", st.Status)
	}
}

func TestExplicitRunningCannotOverridePassedEnd(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	running := domain.StatusRunning
	end := now.Add(-time.Hour)
	st, err := DeriveProgramState(ProgramInput{Status: &running, End: &end}, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusEnded {
		t.Fatalf("status = %s, want ENDED (passed end wins over stale RUNNING)", st.Status)
	}
}

func TestExplicitPausedStillWinsOverEnd(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	paused := domain.StatusPaused
	end := now.Add(-time.Hour)
	st, err := DeriveProgramState(ProgramInput{Status: &paused, End: &end}, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusPaused {
		t.Fatalf("status = %s, want PAUSED", st.Status)
	}
}

func TestDeriveProgramStatePausedAndUpcoming(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	st, err := DeriveProgramState(ProgramInput{Paused: true, Start: &start, End: &end}, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusPaused {
		t.Fatalf("paused status = %s", st.Status)
	}

	st, err = DeriveProgramState(ProgramInput{Start: &start, End: &end}, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StatusUpcoming {
		t.Fatalf("upcoming status = %s", st.Status)
	}
}

func TestDeriveAccountCapabilityButianCompliance(t *testing.T) {
	identity := true
	compliance := false
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	gates := []domain.EligibilityGate{
		{Kind: domain.GateComplianceExam, Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true},
	}
	cap := DeriveAccountCapability(gates, &identity, &compliance, nil, now)
	if cap.CanSubmit {
		t.Fatal("CanSubmit should be false when compliance exam is incomplete")
	}
	if len(cap.BlockingReasons) != 1 {
		t.Fatalf("blocking reasons = %d, want 1", len(cap.BlockingReasons))
	}
}

func TestPracticalCertificationDoesNotBlockGeneralSubmit(t *testing.T) {
	identity := true
	compliance := true
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	pid := domain.ProgramID("p1")
	gates := []domain.EligibilityGate{{
		Kind: domain.GatePracticalCertification, ProgramID: &pid,
		Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true,
	}}
	cap := DeriveAccountCapability(gates, &identity, &compliance, nil, now)
	if !cap.CanSubmit {
		t.Fatal("program-scoped practical certification must not block general submit")
	}
}

func TestStaleFactsCannotProduceFreshState(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	running := domain.StatusRunning
	in := ProgramInput{
		Status:           &running,
		OldestObservedAt: now.Add(-DefaultFactTTL - time.Hour),
	}
	if _, err := DeriveProgramState(in, now, 30*time.Minute); err == nil {
		t.Fatal("facts older than the fact TTL must not produce a state")
	}
}

func TestStateValidityDoesNotOutliveInputFacts(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	running := domain.StatusRunning
	// The fact expires in 10 minutes, which must cap the state before its own
	// 30-minute TTL.
	observed := now.Add(-DefaultFactTTL + 10*time.Minute)
	in := ProgramInput{Status: &running, OldestObservedAt: observed}
	st, err := DeriveProgramState(in, now, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ValidUntil.Equal(observed.Add(DefaultFactTTL)) {
		t.Fatalf("valid_until = %v, want input fact expiry %v", st.ValidUntil, observed.Add(DefaultFactTTL))
	}
	if !st.ObservedAt.Equal(observed) {
		t.Fatalf("observed_at = %v, want input observation %v", st.ObservedAt, observed)
	}
}

func TestProgramStateKeepsProvenance(t *testing.T) {
	in, err := ProgramInputFromFacts(growFacts(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := DeriveProgramState(in, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Inputs) == 0 || len(st.Evidence) == 0 {
		t.Fatalf("provenance missing: inputs=%v evidence=%v", st.Inputs, st.Evidence)
	}
}

func TestProgramStatePropagatesFactConfidence(t *testing.T) {
	in, err := ProgramInputFromFacts(growFacts(t))
	if err != nil {
		t.Fatal(err)
	}
	if in.Confidence != 0.9 {
		t.Fatalf("input confidence = %v, want 0.9", in.Confidence)
	}
	st, err := DeriveProgramState(in, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Confidence != 0.9 {
		t.Fatalf("state confidence = %v, want 0.9", st.Confidence)
	}
}

func TestProgramStateUsesLowestInputConfidence(t *testing.T) {
	fs := growFacts(t)
	for i := range fs {
		if fs[i].Predicate == facts.PredRegistrationEnd {
			fs[i].Confidence = 0.5
		}
	}
	in, err := ProgramInputFromFacts(fs)
	if err != nil {
		t.Fatal(err)
	}
	if in.Confidence != 0.5 {
		t.Fatalf("input confidence = %v, want 0.5", in.Confidence)
	}
	st, err := DeriveProgramState(in, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Confidence != 0.5 {
		t.Fatalf("state confidence = %v, want 0.5", st.Confidence)
	}
}

func TestProgramInputRejectsInvalidFactConfidence(t *testing.T) {
	fs := growFacts(t)
	fs[0].Confidence = 7.5
	if _, err := ProgramInputFromFacts(fs); err == nil {
		t.Fatal("out-of-range fact confidence should be rejected")
	}
}

func TestGrowInclusiveEnd(t *testing.T) {
	in, err := ProgramInputFromFacts(growFacts(t))
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-30 20:00 Asia/Shanghai is still inside the inclusive end date.
	at := time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)
	open, err := DeriveRegistrationOpen(in, at)
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Fatal("registration should be open on 2026-11-30 local time")
	}
	// 2026-12-01 00:00 Asia/Shanghai has passed the inclusive end.
	after := time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC)
	open, err = DeriveRegistrationOpen(in, after)
	if err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("registration should be closed after local 2026-11-30")
	}
}
