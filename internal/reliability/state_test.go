package reliability

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

func mustFact(t *testing.T, id string, level ClaimLevel, evidence []domain.EvidenceID) FactRecord {
	t.Helper()
	f, err := NewFactRecord(id, Claim{
		Level:      level,
		Statement:  "observed fact",
		Evidence:   evidence,
		Confidence: 0.8,
	}, testNow)
	if err != nil {
		t.Fatalf("NewFactRecord(%s): %v", level, err)
	}
	return f
}

func TestClaimEvidenceInvariants(t *testing.T) {
	cases := []struct {
		name    string
		claim   Claim
		wantErr bool
	}{
		{name: "observation without evidence", claim: Claim{Level: LevelObservation, Statement: "x"}, wantErr: true},
		{name: "claim without evidence", claim: Claim{Level: LevelClaim, Statement: "x"}, wantErr: true},
		{name: "verified fact without evidence", claim: Claim{Level: LevelVerifiedFact, Statement: "x"}, wantErr: true},
		{name: "hypothesis without evidence", claim: Claim{Level: LevelHypothesis, Statement: "x"}, wantErr: false},
		{name: "empty statement", claim: Claim{Level: LevelHypothesis}, wantErr: true},
		{name: "invalid confidence", claim: Claim{Level: LevelHypothesis, Statement: "x", Confidence: 1.5}, wantErr: true},
		{name: "unknown level", claim: Claim{Level: "MAYBE", Statement: "x"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.claim.Validate()
			if tc.wantErr && !errors.Is(err, ErrInvalidClaim) {
				t.Fatalf("want ErrInvalidClaim, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPromotionCannotSkipLevels(t *testing.T) {
	base := mustFact(t, "fact-1", LevelObservation, []domain.EvidenceID{"ev-1"})

	if _, err := base.Promote(PromotionRequest{To: LevelClaim}); !errors.Is(err, ErrLevelSkipped) {
		t.Fatalf("observation -> claim: want ErrLevelSkipped, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{To: LevelVerifiedFact}); !errors.Is(err, ErrLevelSkipped) {
		t.Fatalf("observation -> verified fact: want ErrLevelSkipped, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{To: LevelObservation}); !errors.Is(err, ErrInvalidPromotion) {
		t.Fatalf("same level: want ErrInvalidPromotion, got %v", err)
	}

	hyp, err := base.Promote(PromotionRequest{To: LevelHypothesis, Now: testNow.Add(time.Minute)})
	if err != nil {
		t.Fatalf("observation -> hypothesis: %v", err)
	}
	if hyp.Claim.Level != LevelHypothesis {
		t.Fatalf("level = %s, want HYPOTHESIS", hyp.Claim.Level)
	}
	if len(hyp.Claim.Evidence) != 1 || hyp.Claim.Evidence[0] != "ev-1" {
		t.Fatalf("promotion dropped evidence: %v", hyp.Claim.Evidence)
	}
}

func TestHypothesisWithoutEvidenceCannotBecomeClaim(t *testing.T) {
	base := mustFact(t, "fact-h", LevelHypothesis, nil)

	if _, err := base.Promote(PromotionRequest{To: LevelClaim}); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("want ErrEvidenceRequired, got %v", err)
	}

	claim, err := base.Promote(PromotionRequest{
		To:       LevelClaim,
		Evidence: []domain.EvidenceID{"ev-9"},
		Now:      testNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("promote with evidence: %v", err)
	}
	if claim.Claim.Level != LevelClaim || len(claim.Claim.Evidence) != 1 {
		t.Fatalf("unexpected promoted claim: %+v", claim.Claim)
	}
}

type fakeProofResolver struct {
	receipts      map[string]ReceiptProof
	verifications map[string]VerificationProof
}

func (f fakeProofResolver) ResolveReceipt(_ context.Context, id string) (ReceiptProof, error) {
	r, ok := f.receipts[id]
	if !ok {
		return ReceiptProof{}, ErrNotFound
	}
	return r, nil
}

func (f fakeProofResolver) ResolveVerification(_ context.Context, id string) (VerificationProof, error) {
	v, ok := f.verifications[id]
	if !ok {
		return VerificationProof{}, ErrNotFound
	}
	return v, nil
}

func verifiedFactBase(t *testing.T) FactRecord {
	t.Helper()
	asset := domain.AssetID("a1")
	base := mustFact(t, "fact-c", LevelClaim, []domain.EvidenceID{"ev-1"})
	base.ProgramID = "p1"
	base.FindingID = "f1"
	base.AssetID = &asset
	return base
}

func TestVerifiedFactRequiresAuthoritativeProofs(t *testing.T) {
	base := verifiedFactBase(t)
	asset := domain.AssetID("a1")
	completed := ReceiptProof{
		ID: "rcpt-2", Status: domain.ReceiptCompleted, ProgramID: "p1", FindingID: "f1",
		AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"},
	}
	failed := ReceiptProof{
		ID: "rcpt-1", Status: domain.ReceiptFailed, ProgramID: "p1", FindingID: "f1",
		AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"},
	}
	notVerified := VerificationProof{
		ID: "ver-1", Verdict: domain.VerdictNeedsEvidence, ProgramID: "p1", FindingID: "f1",
		AssetID: &asset, Evidence: []domain.EvidenceID{"ev-1"}, VerifiedAt: testNow,
	}
	verified := VerificationProof{
		ID: "ver-2", Verdict: domain.VerdictVerified, ProgramID: "p1", FindingID: "f1",
		AssetID: &asset, Evidence: []domain.EvidenceID{"ev-1"}, VerifiedAt: testNow,
	}
	resolver := fakeProofResolver{
		receipts:      map[string]ReceiptProof{"rcpt-2": completed, "rcpt-1": failed},
		verifications: map[string]VerificationProof{"ver-1": notVerified, "ver-2": verified},
	}

	// A VERIFIED_FACT cannot be produced without an authoritative resolver.
	if _, err := base.Promote(PromotionRequest{To: LevelVerifiedFact}); !errors.Is(err, ErrProofResolverRequired) {
		t.Fatalf("no resolver: want ErrProofResolverRequired, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Resolver: resolver,
	}); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("no receipt: want ErrReceiptRequired, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{failed}, Resolver: resolver,
	}); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("failed receipt: want ErrReceiptRequired, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{completed}, Resolver: resolver,
	}); !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("no verification: want ErrVerificationRequired, got %v", err)
	}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{completed},
		Verification: &notVerified, Resolver: resolver,
	}); !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("non-VERIFIED verdict: want ErrVerificationRequired, got %v", err)
	}
	out, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{completed},
		Verification: &verified, Resolver: resolver, Now: testNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("valid verified promotion: %v", err)
	}
	if out.Claim.Level != LevelVerifiedFact {
		t.Fatalf("level = %s, want VERIFIED_FACT", out.Claim.Level)
	}
	if len(out.Receipts) != 1 || out.Receipts[0].Status != domain.ReceiptCompleted {
		t.Fatalf("receipts = %+v", out.Receipts)
	}
	if out.Verification == nil || out.Verification.ID != "ver-2" {
		t.Fatalf("verification = %+v", out.Verification)
	}
}

func TestFactRecordCannotBeConstructedAtVerifiedFactWithoutProof(t *testing.T) {
	asset := domain.AssetID("a1")
	f := FactRecord{
		ID: "fact-bypass", ProgramID: "p1", FindingID: "f1", AssetID: &asset,
		Claim: Claim{
			Level: LevelVerifiedFact, Statement: "verified without proof",
			Evidence: []domain.EvidenceID{"ev-1"}, Confidence: 1,
		},
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := f.Validate(); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("want ErrReceiptRequired, got %v", err)
	}
}

func TestResearchStateRejectsRawSecrets(t *testing.T) {
	_, err := NewFactRecord("fact-secret", Claim{
		Level:      LevelClaim,
		Statement:  "the response contained password=hunter2",
		Evidence:   []domain.EvidenceID{"ev-1"},
		Confidence: 0.5,
	}, testNow)
	if !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("claim secret: want ErrSecretDetected, got %v", err)
	}

	s := NewResearchSession("sess-secret", "task-secret", testNow)
	s.Objective = "use cookie: session=abcdef123456"
	if err := s.Validate(); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("session objective secret: want ErrSecretDetected, got %v", err)
	}

	raw := Observation{
		ID:         "obs-manual",
		ToolName:   "read_scope",
		Content:    "authorization: bearer abc.def.ghi",
		ObservedAt: testNow,
		Sequence:   1,
	}
	if err := raw.Validate(); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("manual observation secret: want ErrSecretDetected, got %v", err)
	}
}

func TestNewObservationScrubsSecrets(t *testing.T) {
	obs := NewObservation("read_scope", []byte(`{"asset":"a"}`), "token=abcdefgh12345678",
		"token=abcdefgh12345678", []domain.EvidenceID{"ev-1"}, testNow)
	if err := obs.Validate(); err != nil {
		t.Fatalf("scrubbed observation should be valid: %v", err)
	}
	if strings.Contains(obs.Content, "abcdefgh12345678") {
		t.Fatalf("observation kept a raw secret: %q", obs.Content)
	}
	if !strings.Contains(obs.Content, "[REDACTED:") {
		t.Fatalf("expected redaction marker, got %q", obs.Content)
	}
}

func TestStableIDsAndHashes(t *testing.T) {
	if StableID("fact", "a", "b") != StableID("fact", "a", "b") {
		t.Fatal("StableID is not deterministic")
	}
	if StableID("fact", "a", "b") == StableID("fact", "b", "a") {
		t.Fatal("StableID ignored part order")
	}

	a := NewObservation("tool", []byte(`{"a":1,"b":2}`), "same", "same", nil, testNow)
	b := NewObservation("tool", []byte(`{"b":2,"a":1}`), "same", "same", nil, testNow)
	if a.ID != b.ID {
		t.Fatalf("canonical args should produce the same observation id: %s vs %s", a.ID, b.ID)
	}
	if HashArgs([]byte(`{"a":1,"b":2}`)) != HashArgs([]byte(`{"b":2,"a":1}`)) {
		t.Fatal("HashArgs is not canonical")
	}
	c := NewObservation("tool", []byte(`{"a":1,"b":2}`), "same", "same", nil, testNow.Add(time.Second))
	if a.ID == c.ID {
		t.Fatal("different observation time should produce a different id")
	}
}

func TestResearchSessionAppendAndValidate(t *testing.T) {
	s := NewResearchSession("sess-1", "task-1", testNow)
	s.ProgramID = "prog-1"
	s.Budget = Budget{MaxSteps: 10, MaxToolCalls: 10, MaxTokens: 1000}

	obs := NewObservation("read_scope", []byte(`{"asset":"a"}`), "scope read", "scope read",
		[]domain.EvidenceID{"ev-1"}, testNow)
	if err := s.AddObservation(obs); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	f := mustFact(t, "fact-1", LevelObservation, []domain.EvidenceID{"ev-1"})
	if err := s.AddFact(f); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	h := Hypothesis{
		ID: "hyp-1", Statement: "IDOR in export", Evidence: []domain.EvidenceID{"ev-1"},
		Confidence: 0.6, Status: HypothesisOpen, CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := s.AddHypothesis(h); err != nil {
		t.Fatalf("AddHypothesis: %v", err)
	}

	failed := h
	failed.ID = "hyp-failed"
	failed.Status = HypothesisRefuted
	failed.Refutation = "export is scoped to the owner"
	if err := s.AddFailedHypothesis(failed, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("AddFailedHypothesis: %v", err)
	}

	if err := s.AddUnknown(Unknown{
		ID: "unknown-1", Question: "is the export endpoint authenticated?",
		CreatedAt: testNow,
	}); err != nil {
		t.Fatalf("AddUnknown: %v", err)
	}

	attempt := Attempt{
		ID: "attempt-1", Step: 1, ProposalID: "p-1", ToolName: "read_scope",
		Action: domain.ActionReadScope, ArgsHash: HashArgs([]byte(`{"asset":"a"}`)),
		ObservationID: obs.ID, StartedAt: testNow, FinishedAt: testNow,
	}
	if err := s.AddAttempt(attempt); err != nil {
		t.Fatalf("AddAttempt: %v", err)
	}

	if err := s.AddCandidateFinding(CandidateFinding{
		ID: "cand-1", TaskID: "task-1", ProgramID: "prog-1",
		AssetID: ptrAsset("asset-1"), Title: "IDOR", VulnType: "IDOR",
		Summary: "user B can read user A's export", Evidence: []domain.EvidenceID{"ev-1"},
		Confidence: 0.7, CreatedAt: testNow,
	}); err != nil {
		t.Fatalf("AddCandidateFinding: %v", err)
	}

	if err := s.Validate(); err != nil {
		t.Fatalf("session should validate: %v", err)
	}
	if got := s.CandidateFindings[0].ToFinding(); got.Status != "CANDIDATE" {
		t.Fatalf("candidate converted with status %q", got.Status)
	}
}

func TestDuplicateFactIDIsRejected(t *testing.T) {
	s := NewResearchSession("sess-dup", "task-1", testNow)
	f := mustFact(t, "fact-dup", LevelObservation, []domain.EvidenceID{"ev-1"})
	if err := s.AddFact(f); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFact(f); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("want duplicate id error, got %v", err)
	}
}

func ptrAsset(id domain.AssetID) *domain.AssetID { return &id }

func TestVerifiedFactIgnoresForgedProofSummaries(t *testing.T) {
	base := verifiedFactBase(t)
	asset := domain.AssetID("a1")
	resolver := fakeProofResolver{
		receipts: map[string]ReceiptProof{
			"rcpt-1": {ID: "rcpt-1", Status: domain.ReceiptFailed, ProgramID: "p1", FindingID: "f1",
				AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"}},
			"rcpt-2": {ID: "rcpt-2", Status: domain.ReceiptCompleted, ProgramID: "p1", FindingID: "f1",
				AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"}},
		},
		verifications: map[string]VerificationProof{
			"ver-1": {ID: "ver-1", Verdict: domain.VerdictNeedsEvidence, ProgramID: "p1", FindingID: "f1",
				AssetID: &asset, Evidence: []domain.EvidenceID{"ev-1"}, VerifiedAt: testNow},
		},
	}
	// The caller claims COMPLETED/VERIFIED; the authoritative store disagrees.
	forgedReceipt := ReceiptProof{ID: "rcpt-1", Status: domain.ReceiptCompleted}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{forgedReceipt}, Resolver: resolver,
	}); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("forged completed receipt: want ErrReceiptRequired, got %v", err)
	}
	forgedVerification := VerificationProof{ID: "ver-1", Verdict: domain.VerdictVerified,
		Evidence: []domain.EvidenceID{"ev-1"}, VerifiedAt: testNow}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{{ID: "rcpt-2", Status: domain.ReceiptCompleted}},
		Verification: &forgedVerification, Resolver: resolver,
	}); !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("forged verification: want ErrVerificationRequired, got %v", err)
	}
}

func TestVerifiedFactRequiresBoundProofs(t *testing.T) {
	base := verifiedFactBase(t)
	asset := domain.AssetID("a1")
	completed := ReceiptProof{ID: "rcpt-1", Status: domain.ReceiptCompleted, ProgramID: "p1",
		FindingID: "f1", AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"}}
	verified := VerificationProof{ID: "ver-1", Verdict: domain.VerdictVerified, ProgramID: "p1",
		FindingID: "f1", AssetID: &asset, Evidence: []domain.EvidenceID{"ev-1"}, VerifiedAt: testNow}

	otherProgram := completed
	otherProgram.ProgramID = "p2"
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{otherProgram},
		Verification: &verified, Resolver: fakeProofResolver{
			receipts:      map[string]ReceiptProof{"rcpt-1": otherProgram},
			verifications: map[string]VerificationProof{"ver-1": verified},
		},
	}); !errors.Is(err, ErrReceiptRequired) {
		t.Fatalf("receipt for another program: want ErrReceiptRequired, got %v", err)
	}

	otherAsset := verified
	otherAsset.AssetID = ptrAsset("a2")
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{completed},
		Verification: &otherAsset, Resolver: fakeProofResolver{
			receipts:      map[string]ReceiptProof{"rcpt-1": completed},
			verifications: map[string]VerificationProof{"ver-1": otherAsset},
		},
	}); !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("verification for another asset: want ErrVerificationRequired, got %v", err)
	}

	uncovered := verified
	uncovered.Evidence = []domain.EvidenceID{"ev-2"}
	if _, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{completed},
		Verification: &uncovered, Resolver: fakeProofResolver{
			receipts:      map[string]ReceiptProof{"rcpt-1": completed},
			verifications: map[string]VerificationProof{"ver-1": uncovered},
		},
	}); !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("verification evidence disjoint from claim: want ErrVerificationRequired, got %v", err)
	}
}

func TestVerifiedFactPropagatesResolverNotFound(t *testing.T) {
	base := verifiedFactBase(t)
	resolver := fakeProofResolver{receipts: map[string]ReceiptProof{}}
	_, err := base.Promote(PromotionRequest{
		To: LevelVerifiedFact, Receipts: []ReceiptProof{{ID: "missing", Status: domain.ReceiptCompleted}},
		Resolver: resolver,
	})
	if !errors.Is(err, ErrReceiptRequired) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("want both ErrReceiptRequired and ErrNotFound, got %v", err)
	}
}

func TestCloneFactRecordPreservesProofBindings(t *testing.T) {
	asset := domain.AssetID("a1")
	in := FactRecord{
		ID: "fact-clone", ProgramID: "p1", FindingID: "f1", AssetID: &asset,
		Claim: Claim{Level: LevelClaim, Statement: "x", Evidence: []domain.EvidenceID{"ev-1"}, Confidence: 0.8},
		Receipts: []ReceiptProof{{
			ID: "rcpt-1", Status: domain.ReceiptCompleted, ProgramID: "p1", FindingID: "f1",
			AssetID: &asset, Action: domain.ActionActiveHTTP, Evidence: []domain.EvidenceID{"ev-1"},
		}},
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	out := cloneFactRecord(in)
	if !reflect.DeepEqual(in.Receipts, out.Receipts) {
		t.Fatalf("receipts lost fields during clone: %+v", out.Receipts)
	}
}
