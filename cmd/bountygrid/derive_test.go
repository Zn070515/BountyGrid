package main

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

type fakeDeriveStore struct {
	programs []domain.Program
	facts    map[domain.ProgramID][]domain.ObservedFact
	states   []domain.DerivedState
	listErr  error
	putErr   error
}

func (f *fakeDeriveStore) ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.programs, nil
}
func (f *fakeDeriveStore) ListFactsByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error) {
	return f.facts[id], nil
}
func (f *fakeDeriveStore) ListFactsByPlatform(ctx context.Context, platform domain.Platform) ([]domain.ObservedFact, error) {
	var out []domain.ObservedFact
	for _, fs := range f.facts {
		out = append(out, fs...)
	}
	return out, nil
}
func (f *fakeDeriveStore) PutStates(ctx context.Context, states []domain.DerivedState) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.states = append(f.states, states...)
	return nil
}

func TestExecuteDerive(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	pid := domain.ProgramID("vulbox/444")
	p := domain.Program{ID: pid, Platform: domain.PlatformVulbox}
	f := func(id string, pred string, value any) domain.ObservedFact {
		return domain.ObservedFact{
			ID: domain.FactID(id), Subject: domain.SubjectProgram, Platform: domain.PlatformVulbox,
			ProgramID: &pid, Predicate: pred, Value: value, ObservedAt: now,
			EvidenceRefs: []domain.EvidenceID{"e1"}, Confidence: 1, ExtractorVersion: "test",
		}
	}
	fsList := []domain.ObservedFact{
		f("f1", facts.PredProgramStatus, "RUNNING"),
		f("f2", facts.PredProgramStart, now.Add(-time.Hour)),
		f("f3", facts.PredProgramEnd, now.Add(time.Hour)),
		f("f4", facts.PredScopeVisibility, "GATED_BY_SIGNUP"),
		f("f5", facts.PredRegistrationStart, now.Add(-time.Hour)),
		f("f6", facts.PredRegistrationEnd, now.Add(time.Hour)),
	}
	store := &fakeDeriveStore{
		programs: []domain.Program{p},
		facts:    map[domain.ProgramID][]domain.ObservedFact{pid: fsList},
	}
	out, err := executeDerive(context.Background(), store, deriveOptions{Platform: "vulbox", TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.Programs != 1 || out.States != 3 || out.Skipped != 0 {
		t.Fatalf("output = %+v", out)
	}
	if out.BySubject[string(domain.StateProgramState)] != 1 ||
		out.BySubject[string(domain.StateScopeVisibility)] != 1 ||
		out.BySubject[string(domain.StateRegistration)] != 1 {
		t.Fatalf("subjects = %+v", out.BySubject)
	}
	seen := map[string]string{}
	for _, s := range store.states {
		if err := s.Validate(); err != nil {
			t.Fatalf("invalid state: %v", err)
		}
		if len(s.Inputs) == 0 {
			t.Fatalf("state without inputs: %+v", s)
		}
		seen[string(s.Subject)] = s.State
	}
	if seen[string(domain.StateProgramState)] != "RUNNING" ||
		seen[string(domain.StateScopeVisibility)] != "GATED_BY_SIGNUP" ||
		seen[string(domain.StateRegistration)] != "OPEN" {
		t.Fatalf("states = %+v", seen)
	}
}

func TestExecuteDeriveSkipsNoFacts(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	store := &fakeDeriveStore{programs: []domain.Program{{ID: "vulbox/444", Platform: domain.PlatformVulbox}}}
	out, err := executeDerive(context.Background(), store, deriveOptions{Platform: "vulbox", TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.States != 0 || out.Skipped != 1 {
		t.Fatalf("output = %+v", out)
	}
}

func TestExecuteDeriveFiltersProgram(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	store := &fakeDeriveStore{programs: []domain.Program{
		{ID: "vulbox/444", Platform: domain.PlatformVulbox},
		{ID: "vulbox/1841", Platform: domain.PlatformVulbox},
	}}
	out, err := executeDerive(context.Background(), store, deriveOptions{Platform: "vulbox", ProgramID: "vulbox/1841", TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.Programs != 1 {
		t.Fatalf("programs = %d, want 1", out.Programs)
	}
}

func TestExecuteDeriveWorkersDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	pid := domain.ProgramID("vulbox/444")
	p := domain.Program{ID: pid, Platform: domain.PlatformVulbox}
	fact := domain.ObservedFact{
		ID: "f1", Subject: domain.SubjectProgram, Platform: domain.PlatformVulbox,
		ProgramID: &pid, Predicate: facts.PredProgramStatus, Value: "RUNNING",
		ObservedAt: now, EvidenceRefs: []domain.EvidenceID{"e1"},
		Confidence: 1, ExtractorVersion: "test",
	}
	run := func(workers int) []domain.DerivedState {
		store := &fakeDeriveStore{
			programs: []domain.Program{p},
			facts:    map[domain.ProgramID][]domain.ObservedFact{pid: {fact}},
		}
		if _, err := executeDerive(context.Background(), store, deriveOptions{
			Platform: "vulbox", TTL: time.Hour, Workers: workers,
		}, now); err != nil {
			t.Fatal(err)
		}
		return store.states
	}
	one := run(1)
	many := run(4)
	if len(one) != len(many) {
		t.Fatalf("state counts differ: %d vs %d", len(one), len(many))
	}
	for i := range one {
		if one[i].ID != many[i].ID || one[i].State != many[i].State {
			t.Fatalf("state %d differs: %+v vs %+v", i, one[i], many[i])
		}
	}
}

func TestParseDeriveOptions(t *testing.T) {
	opts, err := parseDeriveOptions([]string{"-platform", "xianzhi", "-program", "xianzhi/324", "-ttl", "10m"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Platform != "xianzhi" || opts.ProgramID != "xianzhi/324" || opts.TTL != 10*time.Minute {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseDeriveOptions([]string{"-ttl", "0s"}); err == nil {
		t.Fatal("zero ttl should be rejected")
	}
}
