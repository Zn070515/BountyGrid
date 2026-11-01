package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type fakeVerifyStore struct {
	program      domain.Program
	decision     domain.ActionDecision
	finding      domain.Finding
	evidence     map[domain.EvidenceID]domain.Evidence
	verifs       []domain.Verification
	receipts     map[string]domain.ExecutionReceipt
	humanReviews map[string]domain.HumanReview
	programErr   error
	decisionErr  error
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
func (s *fakeVerifyStore) GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error) {
	if s.finding.ID != id {
		return domain.Finding{}, postgres.ErrNotFound
	}
	return s.finding, nil
}
func (s *fakeVerifyStore) GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	e, ok := s.evidence[id]
	if !ok {
		return domain.Evidence{}, postgres.ErrNotFound
	}
	return e, nil
}
func (s *fakeVerifyStore) PutVerification(ctx context.Context, v domain.Verification) error {
	s.verifs = append(s.verifs, v)
	return nil
}
func (s *fakeVerifyStore) GetReceiptByID(ctx context.Context, id string) (domain.ExecutionReceipt, error) {
	if r, ok := s.receipts[id]; ok {
		return r, nil
	}
	if id == "rcpt/1" {
		return verifyReceipt(), nil
	}
	return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
}
func (s *fakeVerifyStore) GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error) {
	if h, ok := s.humanReviews[id]; ok {
		return h, nil
	}
	return domain.HumanReview{}, postgres.ErrNotFound
}
func (s *fakeVerifyStore) PutHumanReview(ctx context.Context, h domain.HumanReview) error {
	if s.humanReviews == nil {
		s.humanReviews = map[string]domain.HumanReview{}
	}
	s.humanReviews[h.ID] = h
	return nil
}

func verifyReceipt() domain.ExecutionReceipt {
	a := domain.AssetID("a1")
	return domain.ExecutionReceipt{
		ID: "rcpt/1", IdempotencyKey: "idem-1", DecisionID: "d1",
		Action: domain.ActionActiveHTTP, ProgramID: "p1", AssetID: &a,
		Status: domain.ReceiptCompleted, Evidence: []domain.EvidenceID{"ev-repro"},
		StartedAt: testNow, UpdatedAt: testNow,
	}
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
	expires := testNow.Add(5 * time.Minute)
	return domain.ActionDecision{
		ID: "d1", RequestID: "r1", ProgramID: "p1", Action: domain.ActionActiveHTTP,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow,
		ExpiresAt: &expires,
		Request: domain.ActionRequest{
			ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
			AccountID: "acc1", ProgramID: "p1", AssetID: &a, Action: domain.ActionActiveHTTP, RequestedAt: testNow,
		},
	}
}

func validVerifyFinding() domain.Finding {
	a := domain.AssetID("a1")
	return domain.Finding{
		ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: &a,
		Title: "SQL injection", VulnType: "SQLI", Severity: "HIGH",
		Evidence: []domain.EvidenceID{"ev-finding"}, Status: "CANDIDATE", CreatedAt: testNow,
	}
}

func validVerifyFile() verifyFile {
	a := domain.AssetID("a1")
	yes := true
	f := validVerifyFinding()
	return verifyFile{
		Finding: verifyFinding{
			ID: f.ID, TaskID: f.TaskID, ProgramID: f.ProgramID, AssetID: &a,
			Title: f.Title, VulnType: f.VulnType, Severity: f.Severity,
			Evidence: f.Evidence, Status: f.Status, CreatedAt: f.CreatedAt,
		},
		ProgramID: "p1", DecisionID: "d1",
		ExecutionReceipts: []string{"rcpt/1"},
		Reproduced:        true, ReproductionEvidence: []domain.EvidenceID{"ev-repro"},
		ImpactVerified: true, ImpactEvidence: []domain.EvidenceID{"ev-impact"},
		DataMinimized: &yes, DuplicateChecked: true, DuplicateRisk: 0.1,
	}
}

func verifyEvidence() map[domain.EvidenceID]domain.Evidence {
	return map[domain.EvidenceID]domain.Evidence{
		"ev-finding": {ID: "ev-finding"},
		"ev-repro":   {ID: "ev-repro"},
		"ev-impact":  {ID: "ev-impact"},
	}
}

func TestExecuteVerifyVerifiedAndPersisted(t *testing.T) {
	store := &fakeVerifyStore{
		program: verifyProgram(), decision: verifyDecision(),
		finding: validVerifyFinding(), evidence: verifyEvidence(),
	}
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

func TestExecuteVerifyMissingEvidence(t *testing.T) {
	store := &fakeVerifyStore{
		program: verifyProgram(), decision: verifyDecision(),
		finding: validVerifyFinding(),
		evidence: map[domain.EvidenceID]domain.Evidence{
			"ev-finding": {ID: "ev-finding"}, "ev-repro": {ID: "ev-repro"},
		},
	}
	out, err := executeVerify(context.Background(), store, validVerifyFile(), false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != domain.VerdictNeedsEvidence ||
		out.Reasons[0] != "EVIDENCE_NOT_FOUND:ev-impact" {
		t.Fatalf("missing evidence = %s %v", out.Verdict, out.Reasons)
	}
}

func TestExecuteVerifyPersistRequiresStoredFinding(t *testing.T) {
	store := &fakeVerifyStore{
		program: verifyProgram(), decision: verifyDecision(),
		evidence: verifyEvidence(),
	}
	if _, err := executeVerify(context.Background(), store, validVerifyFile(), true, testNow); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for an unstored finding", err)
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

func TestLoadVerifyFileSnakeCaseFinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.json")
	body := `{
		"program_id": "p1",
		"finding": {
			"id": "f1", "task_id": "t1", "program_id": "p1", "asset_id": "a1",
			"title": "SQL injection", "vuln_type": "SQLI", "severity": "HIGH",
			"evidence": ["ev1"], "status": "CANDIDATE",
			"created_at": "2026-11-01T12:00:00Z"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := loadVerifyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if in.Finding.ID != "f1" || in.Finding.TaskID != "t1" || in.Finding.ProgramID != "p1" {
		t.Fatalf("finding decoded incorrectly: %+v", in.Finding)
	}
	if in.Finding.AssetID == nil || *in.Finding.AssetID != "a1" || in.Finding.VulnType != "SQLI" {
		t.Fatalf("finding decoded incorrectly: %+v", in.Finding)
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
