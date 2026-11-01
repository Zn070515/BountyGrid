package authz

import (
	"context"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

// TestPermissionRejectsDuplicateSelectors proves the quote check is real set
// equality, not "every supplied value appears somewhere in the quoted line".
// A fact that repeats one host must not verify against a line granting two
// different hosts.
func TestPermissionRejectsDuplicateSelectors(t *testing.T) {
	program := testProgram()
	line := "PERMISSION program=p1 action=ACTIVE_HTTP hosts=api.example.com,other.example.com methods=GET paths=/v1"
	content := line + "\n"
	hash := redact.Hash([]byte(content))
	store := fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{
		"ev-1": {
			ID: "ev-1", Platform: program.Platform, SourceType: domain.SourcePolicy,
			CapturedAt: testNow, ContentHash: hash,
		},
	}}
	contents := fakeContents{content: map[domain.EvidenceID]string{"ev-1": content}}

	permission := testPermission(program)
	permission.EvidenceContentHash = hash
	permission.EvidenceQuote = line
	permission.Hosts = []string{"api.example.com", "api.example.com"}

	err := VerifyPlatformEvidence(context.Background(), permission, program, store, contents, testNow)
	if !reasonIs(ReasonPermissionNotInEvidence)(err) {
		t.Fatalf("duplicate selector accepted: err = %v, want %s", err, ReasonPermissionNotInEvidence)
	}
}
