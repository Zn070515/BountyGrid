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
	return &domain.ActionDecision{
		ID: "d1", RequestID: "r1", ProgramID: "p1", Action: action,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow,
		Request: domain.ActionRequest{
			ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
			AccountID: "acc1", ProgramID: "p1", AssetID: asset, Action: action, RequestedAt: testNow,
		},
	}
}

func validInput() Input {
	return Input{
		Finding: testFinding(), Program: testProgram(),
		Decision:             allowDecision(domain.ActionActiveHTTP, assetID("a1")),
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
	if v := verifyInput(t, in); v.Verdict != domain.VerdictNeedsEvidence || v.Reasons[0] != "AI_HUMAN_VERIFICATION_REF_MISSING" {
		t.Fatalf("AI without ref = %s %v", v.Verdict, v.Reasons)
	}

	in.HumanVerificationRef = "manually reproduced and screenshotted"
	if v := verifyInput(t, in); v.Verdict != domain.VerdictVerified {
		t.Fatalf("AI with human ref = %s %v", v.Verdict, v.Reasons)
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
		VerifiedBy: "rule", VerifiedAt: testNow, Evidence: []domain.EvidenceID{"ev1"},
	}
	if err := v.Validate(); err == nil {
		t.Fatal("VERIFIED without data_minimized must be rejected")
	}
	v.DataMinimized = true
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
