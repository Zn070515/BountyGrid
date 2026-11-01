package authz

import (
	"context"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

type countingStore struct {
	inner fakeStore
	gets  int
}

func (c *countingStore) GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	c.gets++
	return c.inner.GetEvidence(ctx, id)
}

type countingContents struct {
	inner fakeContents
	reads int
}

func (c *countingContents) ReadContent(ctx context.Context, ev domain.Evidence) (string, error) {
	c.reads++
	return c.inner.ReadContent(ctx, ev)
}

// Several permissions commonly cite the same policy page. Verification must
// load and parse that page once per Build/Evaluate call, not once per
// permission: the pre-execution Authorize path pays for every repeated read.
func TestPlatformPolicyVerifiesEachEvidenceOnce(t *testing.T) {
	program := testProgram()
	secondLine := "PERMISSION program=p1 action=VIEW_RULES hosts=api.example.com methods=GET paths=/v1"
	content := testPermissionLine + "\n" + secondLine + "\n"
	hash := redact.Hash([]byte(content))
	ev := domain.Evidence{
		ID: "ev-1", Platform: program.Platform, SourceType: domain.SourcePolicy,
		CapturedAt: testNow, ContentHash: hash,
	}
	store := &countingStore{inner: fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{"ev-1": ev}}}
	contents := &countingContents{inner: fakeContents{content: map[domain.EvidenceID]string{"ev-1": content}}}

	first := testPermission(program)
	first.EvidenceContentHash = hash
	first.EvidenceQuote = testPermissionLine
	second := first
	second.Action = domain.ActionViewRules
	second.EvidenceQuote = secondLine

	pp, err := BuildPlatformPolicy(context.Background(), program, []PermissionFact{first, second}, store, contents, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if pp.Len() != 2 {
		t.Fatalf("verified permissions = %d, want 2", pp.Len())
	}
	if store.gets != 1 || contents.reads != 1 {
		t.Fatalf("build loads = %d evidence / %d content, want 1/1", store.gets, contents.reads)
	}

	store.gets, contents.reads = 0, 0
	if _, err := pp.Evaluate(context.Background(), program, testRequest()); err != nil {
		t.Fatal(err)
	}
	if store.gets != 1 || contents.reads != 1 {
		t.Fatalf("evaluate loads = %d evidence / %d content, want 1/1", store.gets, contents.reads)
	}
}
