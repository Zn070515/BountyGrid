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
		CapturedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
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
