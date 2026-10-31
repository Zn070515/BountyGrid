package facts

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func fact(id domain.FactID, pred string, at time.Time, v any) domain.ObservedFact {
	pid := domain.ProgramID("p1")
	return domain.ObservedFact{
		ID:               id,
		Subject:          domain.SubjectProgram,
		Platform:         domain.PlatformButian,
		ProgramID:        &pid,
		Predicate:        pred,
		Value:            v,
		ObservedAt:       at,
		EvidenceRefs:     []domain.EvidenceID{"e1"},
		Confidence:       0.9,
		ExtractorVersion: "test@1",
	}
}

func TestMemoryStoreBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()
	if err := s.Put(ctx, []domain.ObservedFact{fact("f1", PredProgramStatus, now, "RUNNING")}); err != nil {
		t.Fatal(err)
	}
	// f1 already exists; f2 must not be written.
	err := s.Put(ctx, []domain.ObservedFact{
		fact("f2", PredProgramStatus, now, "PAUSED"),
		fact("f1", PredProgramStatus, now, "RUNNING"),
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	got, err := s.ListByProgram(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("partial write detected: %+v", got)
	}
}

func TestFactSetUsesLatestObservedAt(t *testing.T) {
	old := fact("f-old", PredProgramStatus, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), "PAUSED")
	newer := fact("f-new", PredProgramStatus, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), "RUNNING")
	fs := NewFactSet([]domain.ObservedFact{newer, old})
	got, ok := fs.String(PredProgramStatus)
	if !ok || got != "RUNNING" {
		t.Fatalf("latest fact = %q (ok=%v), want RUNNING", got, ok)
	}
}

func TestSupersedeRejectsInvalidEdges(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()
	f1 := fact("f1", PredProgramStatus, now, "RUNNING")
	f2 := fact("f2", PredProgramStatus, now.Add(time.Second), "PAUSED")
	f3 := fact("f3", "other.predicate", now, "x")
	if err := s.Put(ctx, []domain.ObservedFact{f1, f2, f3}); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, "f1", "f1"); err == nil {
		t.Fatal("self-supersede must fail")
	}
	if err := s.Supersede(ctx, "f1", "f3"); err == nil {
		t.Fatal("different predicate must fail")
	}
	if err := s.Supersede(ctx, "f1", "f2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, "f2", "f1"); err == nil {
		t.Fatal("cycle must fail")
	}
}
