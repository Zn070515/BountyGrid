package state

import (
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func TestTrackerInvalidatesByFact(t *testing.T) {
	tr := NewTracker()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	d := Derivation{
		StateID:    "s1",
		Inputs:     []domain.FactID{"f1", "f2"},
		ValidUntil: now.Add(time.Hour),
	}
	if err := tr.Record(d); err != nil {
		t.Fatal(err)
	}
	if !tr.Valid("s1", now) {
		t.Fatal("state should be valid")
	}
	got := tr.InvalidateFact("f1")
	if len(got) != 1 || got[0] != "s1" {
		t.Fatalf("invalidated = %v, want [s1]", got)
	}
	if tr.Valid("s1", now) {
		t.Fatal("state should be invalid after supersede")
	}
	// The other input must no longer reference the removed state.
	if got := tr.InvalidateFact("f2"); len(got) != 0 {
		t.Fatalf("f2 invalidated = %v, want empty", got)
	}
}

func TestTrackerRerecordMovesDependencies(t *testing.T) {
	tr := NewTracker()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := tr.Record(Derivation{StateID: "s1", Inputs: []domain.FactID{"f1"}, ValidUntil: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Record(Derivation{StateID: "s1", Inputs: []domain.FactID{"f2"}, ValidUntil: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := tr.InvalidateFact("f1"); len(got) != 0 {
		t.Fatalf("stale input invalidated state: %v", got)
	}
	if !tr.Valid("s1", now) {
		t.Fatal("state should still be valid")
	}
	if got := tr.InvalidateFact("f2"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("current input invalidated = %v, want [s1]", got)
	}
}

func TestTrackerExpiry(t *testing.T) {
	tr := NewTracker()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := tr.Record(Derivation{StateID: "s1", Inputs: []domain.FactID{"f1"}, ValidUntil: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if tr.Valid("s1", now) {
		t.Fatal("expired state must not be valid")
	}
}
