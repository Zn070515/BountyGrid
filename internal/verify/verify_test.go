package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func assetID(v string) *domain.AssetID { a := domain.AssetID(v); return &a }
func boolPtr(v bool) *bool             { return &v }

func testProgram() domain.Program {
	return domain.Program{
		ID: "p1", Platform: domain.PlatformButian, Name: "p1", Type: domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test"},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true,
			Include:    []domain.Asset{{ID: "a1", ProgramID: "p1", Value: "a1.example.com", InScope: true}},
			ObservedAt: testNow,
		},
		Reward:     domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		ObservedAt: testNow,
	}
}

func testFinding() domain.Finding {
	return domain.Finding{
		ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: assetID("a1"),
		Title: "SQL injection in search", VulnType: "SQLI", Severity: "HIGH",
		Evidence: []domain.EvidenceID{"ev-finding"}, Status: "CANDIDATE", CreatedAt: testNow,
	}
}

func allowDecision(action domain.Action, asset *domain.AssetID) *domain.ActionDecision {
	expires := testNow.Add(5 * time.Minute)
	return &domain.ActionDecision{
		ID: "d1", RequestID: "r1", ProgramID: "p1", Action: action,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow,
		ExpiresAt: &expires,
		Request: domain.ActionRequest{
			ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
			AccountID: "acc1", ProgramID: "p1", AssetID: asset, Action: action, RequestedAt: testNow,
		},
	}
}

func testEvidence() fakeEvidence {
	return fakeEvidence{exists: map[domain.EvidenceID]bool{
		"ev-finding": true, "ev-repro": true, "ev-impact": true, "ev-review": true,
	}}
}

func testReceipt() domain.ExecutionReceipt {
	return domain.ExecutionReceipt{
		ID: "rcpt/1", IdempotencyKey: "idem-1", DecisionID: "d1",
		ToolName: "active_http", Action: domain.ActionActiveHTTP,
		AccountID: "acc1", ProgramID: "p1", AssetID: assetID("a1"),
		Status: domain.ReceiptCompleted, Evidence: []domain.EvidenceID{"ev-repro"},
		StartedAt: testNow, UpdatedAt: testNow,
	}
}

func testHumanReview() *domain.HumanReview {
	return &domain.HumanReview{
		ID: "hr1", FindingID: "f1", Reviewer: "human",
		ReviewedAt: testNow, Verdict: domain.HumanReviewApproved,
		Evidence: []domain.EvidenceID{"ev-review"}, NotesHash: "notes-hash",
	}
}

func validInput() Input {
	return Input{
		Finding: testFinding(), Program: testProgram(),
		Decision:             allowDecision(domain.ActionActiveHTTP, assetID("a1")),
		Evidence:             testEvidence(),
		Receipts:             []domain.ExecutionReceipt{testReceipt()},
		Reproduced:           true,
		ReproductionEvidence: []domain.EvidenceID{"ev-repro"},
		ImpactVerified:       true,
		ImpactEvidence:       []domain.EvidenceID{"ev-impact"},
		DataMinimized:        boolPtr(true),
		DuplicateChecked:     true, DuplicateRisk: 0.2,
		Now: testNow,
	}
}

func verifyInput(t *testing.T, in Input) domain.Verification {
	t.Helper()
	v, err := NewRuleVerifier(Options{Now: func() time.Time { return testNow }}).Verify(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifiedPath(t *testing.T) {
	v := verifyInput(t, validInput())
	if v.Verdict != domain.VerdictVerified {
		t.Fatalf("verdict = %s %v", v.Verdict, v.Reasons)
	}
	if !v.InScope || !v.Reproduced || !v.ImpactVerified || !v.DataMinimized {
		t.Fatalf("flags = %+v", v)
	}
	if len(v.Evidence) != 3 {
		t.Fatalf("evidence = %v, want finding+repro+impact", v.Evidence)
	}
	if v.VerifiedBy != ruleVersion {
		t.Fatalf("verified_by = %q", v.VerifiedBy)
	}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOutOfScopeAndConditional(t *testing.T) {
	in := validInput()
	in.Finding.AssetID = assetID("a2")
	if v := verifyInput(t, in); v.Verdict != domain.VerdictOutOfScope || v.InScope {
		t.Fatalf("unknown asset = %s %+v", v.Verdict, v)
	}

	in = validInput()
	in.Program.Scope.Access = domain.ScopeAccessNotGranted
	in.Program.Scope.ContentsKnown = false
	in.Program.Scope.Include = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictOutOfScope {
		t.Fatalf("gated scope = %s", v.Verdict)
	}

	in = validInput()
	in.Program.Scope.Include = nil
	in.Program.Scope.Conditional = []domain.Asset{{ID: "a1", ProgramID: "p1", Value: "a1.example.com", InScope: true}}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "ASSET_CONDITIONAL" {
		t.Fatalf("conditional asset = %s %v", v.Verdict, v.Reasons)
	}
}

func TestAuthorizationGates(t *testing.T) {
	in := validInput()
	in.Decision = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "AUTHORIZATION_MISSING" {
		t.Fatalf("missing decision = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.Decision.Decision = domain.DecisionDeny
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "UNAUTHORIZED_ACTION" {
		t.Fatalf("deny decision = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.Decision.Request.AssetID = assetID("a2")
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "AUTHORIZATION_ASSET_MISMATCH" {
		t.Fatalf("asset mismatch = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.Decision = allowDecision(domain.ActionViewRules, nil)
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "INVALID_SOURCE_ACTION" {
		t.Fatalf("view-rules finding = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.Decision.ExpiresAt = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "AUTHORIZATION_INVALID" {
		t.Fatalf("ALLOW without expiry = %s %v", v.Verdict, v.Reasons)
	}
}

type fakeEvidence struct {
	exists map[domain.EvidenceID]bool
	err    error
}

func (f fakeEvidence) HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.exists[id], nil
}

func TestEvidenceExistenceIsChecked(t *testing.T) {
	in := validInput()
	in.Evidence = fakeEvidence{exists: map[domain.EvidenceID]bool{
		"ev-finding": true, "ev-repro": true, "ev-impact": true,
	}}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictVerified {
		t.Fatalf("present evidence = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.Evidence = fakeEvidence{exists: map[domain.EvidenceID]bool{
		"ev-finding": true, "ev-repro": true,
	}}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence ||
		v.Reasons[0] != "EVIDENCE_NOT_FOUND:ev-impact" {
		t.Fatalf("missing evidence = %s %v", v.Verdict, v.Reasons)
	}
}

func TestDataAndDuplicateGates(t *testing.T) {
	in := validInput()
	in.DataMinimized = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "DATA_MINIMIZATION_UNKNOWN" {
		t.Fatalf("unknown minimization = %s %v", v.Verdict, v.Reasons)
	}
	in.DataMinimized = boolPtr(false)
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "DATA_NOT_MINIMIZED" {
		t.Fatalf("not minimized = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.PubliclyKnown = true
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "PUBLICLY_KNOWN" {
		t.Fatalf("publicly known = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.DuplicateChecked = false
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "DUPLICATE_CHECK_MISSING" {
		t.Fatalf("duplicate unchecked = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.DuplicateRisk = 0.9
	if v := verifyInput(t, in); v.Verdict != domain.VerdictDuplicateRisk {
		t.Fatalf("high duplicate risk = %s", v.Verdict)
	}

	in = validInput()
	in.DuplicateRisk = 1.5
	if _, err := NewRuleVerifier(Options{}).Verify(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid risk err = %v", err)
	}
}

func TestAIGate(t *testing.T) {
	in := validInput()
	in.AIAssisted = true
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "AI_NOT_HUMAN_VERIFIED" {
		t.Fatalf("AI without human = %s %v", v.Verdict, v.Reasons)
	}

	in.HumanVerified = true
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "AI_HUMAN_REVIEW_MISSING" {
		t.Fatalf("AI without review = %s %v", v.Verdict, v.Reasons)
	}

	in.HumanReview = testHumanReview()
	in.HumanReviewID = in.HumanReview.ID
	in.HumanVerificationRef = "manually reproduced and screenshotted"
	v := verifyInput(t, in)
	if v.Verdict != domain.VerdictVerified {
		t.Fatalf("AI with human review = %s %v", v.Verdict, v.Reasons)
	}
	if !v.AIAssisted || !v.HumanVerified || v.HumanReviewID == "" || v.HumanVerificationRef == "" {
		t.Fatalf("AI trace not persisted: %+v", v)
	}
	if !v.DuplicateChecked {
		t.Fatal("duplicate check trace not persisted")
	}
}

func TestReproductionAndImpactGates(t *testing.T) {
	in := validInput()
	in.Reproduced = false
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "NOT_REPRODUCED" {
		t.Fatalf("not reproduced = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.ReproductionEvidence = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "REPRODUCTION_EVIDENCE_MISSING" {
		t.Fatalf("no repro evidence = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.ImpactVerified = false
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "IMPACT_NOT_VERIFIED" {
		t.Fatalf("impact not verified = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.ImpactEvidence = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "IMPACT_EVIDENCE_MISSING" {
		t.Fatalf("no impact evidence = %s %v", v.Verdict, v.Reasons)
	}
}

func TestIncompleteFinding(t *testing.T) {
	in := validInput()
	in.Finding.Evidence = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "FINDING_INCOMPLETE" {
		t.Fatalf("incomplete finding = %s %v", v.Verdict, v.Reasons)
	}
	in = validInput()
	in.Finding.ProgramID = "p2"
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "PROGRAM_MISMATCH" {
		t.Fatalf("program mismatch = %s %v", v.Verdict, v.Reasons)
	}
}

func TestVerificationValidateRejectsUnbackedVerified(t *testing.T) {
	v := domain.Verification{
		ID: "v1", FindingID: "f1", Verdict: domain.VerdictVerified,
		Reproduced: true, InScope: true, ImpactVerified: true, DataMinimized: false,
		DuplicateChecked: true, VerifiedBy: "rule", VerifiedAt: testNow,
		Evidence: []domain.EvidenceID{"ev1"}, ReceiptIDs: []domain.ReceiptID{"rcpt/1"},
	}
	if err := v.Validate(); err == nil {
		t.Fatal("VERIFIED without data_minimized must be rejected")
	}
	v.DataMinimized = true
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	v.AIAssisted = true
	if err := v.Validate(); err == nil {
		t.Fatal("AI-assisted VERIFIED without a human trace must be rejected")
	}
	v.HumanVerified = true
	v.HumanVerificationRef = "reviewed manually"
	if err := v.Validate(); err == nil {
		t.Fatal("AI-assisted VERIFIED without a durable human review must be rejected")
	}
	v.HumanReviewID = "hr1"
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceIsDeduplicated(t *testing.T) {
	in := validInput()
	in.ReproductionEvidence = []domain.EvidenceID{"ev-finding", "", "ev-repro"}
	v := verifyInput(t, in)
	if len(v.Evidence) != 3 {
		t.Fatalf("evidence = %v, want deduplicated non-empty IDs", v.Evidence)
	}
}

type fakeReceipts map[string]domain.ExecutionReceipt

func (f fakeReceipts) GetReceiptByID(ctx context.Context, id string) (domain.ExecutionReceipt, error) {
	r, ok := f[id]
	if !ok {
		return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
	}
	return r, nil
}

type fakeReviews map[string]domain.HumanReview

func (f fakeReviews) GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error) {
	h, ok := f[id]
	if !ok {
		return domain.HumanReview{}, domain.ErrNotFound
	}
	return h, nil
}

func TestExecutionReceiptIsRequiredForVerified(t *testing.T) {
	in := validInput()
	in.Receipts = nil
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "EXECUTION_RECEIPT_MISSING" {
		t.Fatalf("no receipt = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	rec := testReceipt()
	rec.Status = domain.ReceiptStarted
	in.Receipts = []domain.ExecutionReceipt{rec}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "EXECUTION_RECEIPT_NOT_PROVEN" {
		t.Fatalf("unsettled receipt = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	rec = testReceipt()
	rec.DecisionID = "d-other"
	in.Receipts = []domain.ExecutionReceipt{rec}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "EXECUTION_RECEIPT_NOT_PROVEN" {
		t.Fatalf("wrong decision receipt = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	rec = testReceipt()
	rec.Evidence = []domain.EvidenceID{"ev-unrelated"}
	in.Receipts = []domain.ExecutionReceipt{rec}
	in.Evidence = fakeEvidence{exists: map[domain.EvidenceID]bool{
		"ev-finding": true, "ev-repro": true, "ev-impact": true, "ev-unrelated": true,
	}}
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "EXECUTION_RECEIPT_NOT_PROVEN" {
		t.Fatalf("unrelated evidence receipt = %s %v", v.Verdict, v.Reasons)
	}
}

func TestExecutionReceiptResolvedFromFinding(t *testing.T) {
	in := validInput()
	in.Receipts = nil
	in.Finding.ExecutionReceipts = []domain.ReceiptID{"rcpt/1"}
	in.ReceiptChecker = fakeReceipts{"rcpt/1": testReceipt()}
	v := verifyInput(t, in)
	if v.Verdict != domain.VerdictVerified || len(v.ReceiptIDs) != 1 {
		t.Fatalf("resolved receipt = %s %v %v", v.Verdict, v.Reasons, v.ReceiptIDs)
	}
}

func TestHumanReviewResolvedFromStore(t *testing.T) {
	in := validInput()
	in.AIAssisted = true
	in.HumanVerified = true
	in.HumanReviewID = "hr1"
	in.HumanReviewLookup = fakeReviews{"hr1": *testHumanReview()}
	v := verifyInput(t, in)
	if v.Verdict != domain.VerdictVerified || v.HumanReviewID != "hr1" {
		t.Fatalf("resolved review = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.AIAssisted = true
	in.HumanVerified = true
	in.HumanReviewID = "missing"
	in.HumanReviewLookup = fakeReviews{}
	if v := verifyInput(t, in); v.Verdict == domain.VerdictVerified {
		t.Fatal("missing review record must not verify")
	}
}

func TestHumanReviewRejectionAndFindingMismatch(t *testing.T) {
	in := validInput()
	in.AIAssisted = true
	in.HumanVerified = true
	review := testHumanReview()
	review.Verdict = domain.HumanReviewRejected
	in.HumanReview = review
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "AI_HUMAN_REVIEW_REJECTED" {
		t.Fatalf("rejected review = %s %v", v.Verdict, v.Reasons)
	}

	in = validInput()
	in.AIAssisted = true
	in.HumanVerified = true
	review = testHumanReview()
	review.FindingID = "f-other"
	in.HumanReview = review
	if v := verifyInput(t, in); v.Verdict != domain.VerdictRejected || v.Reasons[0] != "AI_HUMAN_REVIEW_FINDING_MISMATCH" {
		t.Fatalf("mismatched review = %s %v", v.Verdict, v.Reasons)
	}
}
