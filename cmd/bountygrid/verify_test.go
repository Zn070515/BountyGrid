package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type fakeVerifyStore struct {
	program     domain.Program
	decision    domain.ActionDecision
	verifs      []domain.Verification
	programErr  error
	decisionErr error
}

func (s *fakeVerifyStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	if s.programErr != nil {
		return domain.Program{}, s.programErr
	}
	if s.program.ID != id {
		return domain.Program{}, postgres.ErrNotFound
	}
	return s.program, nil
}
func (s *fakeVerifyStore) GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error) {
	if s.decisionErr != nil {
		return domain.ActionDecision{}, s.decisionErr
	}
	if s.decision.ID != id {
		return domain.ActionDecision{}, postgres.ErrNotFound
	}
	return s.decision, nil
}
func (s *fakeVerifyStore) PutVerification(ctx context.Context, v domain.Verification) error {
	s.verifs = append(s.verifs, v)
	return nil
}

func verifyProgram() domain.Program {
	a := domain.AssetID("a1")
	return domain.Program{
		ID: "p1", Platform: domain.PlatformButian, Name: "p1", Type: domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test"},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true,
			Include:    []domain.Asset{{ID: a, ProgramID: "p1", Value: "a1.example.com", InScope: true}},
			ObservedAt: testNow,
		},
		Reward:     domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		ObservedAt: testNow,
	}
}

func verifyDecision() domain.ActionDecision {
	a := domain.AssetID("a1")
	return domain.ActionDecision{
		ID: "d1", RequestID: "r1", ProgramID: "p1", Action: domain.ActionActiveHTTP,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow,
		Request: domain.ActionRequest{
			ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
			AccountID: "acc1", ProgramID: "p1", AssetID: &a, Action: domain.ActionActiveHTTP, RequestedAt: testNow,
		},
	}
}

func validVerifyFile() verifyFile {
	a := domain.AssetID("a1")
	yes := true
	now := testNow
	return verifyFile{
		Finding: domain.Finding{
			ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: &a,
			Title: "SQL injection", VulnType: "SQLI", Severity: "HIGH",
			Evidence: []domain.EvidenceID{"ev-finding"}, Status: "CANDIDATE", CreatedAt: testNow,
		},
		ProgramID: "p1", DecisionID: "d1",
		Reproduced: true, ReproductionEvidence: []domain.EvidenceID{"ev-repro"},
		ImpactVerified: true, ImpactEvidence: []domain.EvidenceID{"ev-impact"},
		DataMinimized: &yes, DuplicateChecked: true, DuplicateRisk: 0.1,
		Now: &now,
	}
}

func TestExecuteVerifyVerifiedAndPersisted(t *testing.T) {
	store := &fakeVerifyStore{program: verifyProgram(), decision: verifyDecision()}
	out, err := executeVerify(context.Background(), store, validVerifyFile(), true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != domain.VerdictVerified || !out.Persisted {
		t.Fatalf("out = %+v", out)
	}
	if len(store.verifs) != 1 || store.verifs[0].Verdict != domain.VerdictVerified {
		t.Fatalf("persisted = %+v", store.verifs)
	}
}

func TestExecuteVerifyMissingDecisionNeedsEvidence(t *testing.T) {
	store := &fakeVerifyStore{program: verifyProgram()}
	in := validVerifyFile()
	in.DecisionID = ""
	out, err := executeVerify(context.Background(), store, in, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != domain.VerdictNeedsEvidence {
		t.Fatalf("verdict = %s %v", out.Verdict, out.Reasons)
	}
}

func TestExecuteVerifyMissingProgram(t *testing.T) {
	store := &fakeVerifyStore{}
	if _, err := executeVerify(context.Background(), store, validVerifyFile(), false, testNow); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestParseVerifyOptions(t *testing.T) {
	opts, err := parseVerifyOptions([]string{"-file", "v.json", "-persist"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.File != "v.json" || !opts.Persist {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseVerifyOptions(nil); err == nil {
		t.Fatal("missing -file should fail")
	}
}
