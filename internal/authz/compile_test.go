package authz

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

func TestCompilePermissionsBuildsEvidenceBoundFacts(t *testing.T) {
	program := testProgram()
	ev := testEvidence("ev-1", domain.SourcePolicy)

	compiled, err := CompilePermissions(program, ev, testPolicyContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Permissions) != 1 || len(compiled.Restricted) != 0 {
		t.Fatalf("compiled = %+v", compiled)
	}
	got := compiled.Permissions[0]
	if got.EvidenceID != ev.ID || got.EvidenceContentHash != ev.ContentHash ||
		got.EvidenceQuote != testPermissionLine {
		t.Fatalf("provenance = %+v", got)
	}
	if got.Platform != program.Platform || got.ProgramID != program.ID ||
		got.Action != domain.ActionActiveHTTP {
		t.Fatalf("binding = %+v", got)
	}
	if len(got.Hosts) != 1 || got.Hosts[0] != "api.example.com" ||
		len(got.Methods) != 1 || got.Methods[0] != "GET" ||
		len(got.PathPrefixes) != 1 || got.PathPrefixes[0] != "/v1" {
		t.Fatalf("selectors = %+v", got)
	}
	if !got.ObservedAt.Equal(ev.CapturedAt) {
		t.Fatalf("observed_at = %s, want %s", got.ObservedAt, ev.CapturedAt)
	}
	if want := ev.CapturedAt.Add(domain.PolicyMaxAge); !got.ValidUntil.Equal(want) {
		t.Fatalf("valid_until = %s, want %s", got.ValidUntil, want)
	}
	if got.PolicyRevision != program.PolicyRevision() {
		t.Fatalf("policy revision = %q", got.PolicyRevision)
	}
	if !strings.Contains(got.Justification, string(ev.ID)) {
		t.Fatalf("justification = %q", got.Justification)
	}
}

func TestCompilePermissionsIgnoresOtherPrograms(t *testing.T) {
	program := testProgram()
	ev := testEvidence("ev-1", domain.SourcePolicy)
	content := "PERMISSION program=p2 action=ACTIVE_HTTP hosts=api.example.com methods=GET\n"

	compiled, err := CompilePermissions(program, ev, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Permissions) != 0 || len(compiled.Restricted) != 0 {
		t.Fatalf("foreign program compiled = %+v", compiled)
	}
}

func TestCompilePermissionsSeparatesRestrictedLines(t *testing.T) {
	program := testProgram()
	ev := testEvidence("ev-1", domain.SourcePolicy)
	restricted := "PERMISSION program=p1 action=ACTIVE_HTTP hosts=api.example.com methods=GET restrictions=human_review"
	content := testPermissionLine + "\n" + restricted + "\n"

	compiled, err := CompilePermissions(program, ev, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Permissions) != 1 || compiled.Permissions[0].EvidenceQuote != testPermissionLine {
		t.Fatalf("plain permissions = %+v", compiled.Permissions)
	}
	if len(compiled.Restricted) != 1 || compiled.Restricted[0].Quote != restricted {
		t.Fatalf("restricted = %+v", compiled.Restricted)
	}
}

func TestCompilePermissionsRejectsMalformedContent(t *testing.T) {
	program := testProgram()
	ev := testEvidence("ev-1", domain.SourcePolicy)
	for _, content := range []string{
		"PERMISSION program=p1 action=ACTIVE_HTTP hosts=* methods=GET",
		"PERMISSION program=p1 action=BOGUS hosts=api.example.com methods=GET",
		"PERMISSION program=p1 action=ACTIVE_HTTP hosts=api.example.com methods=GET unknown=x",
	} {
		if _, err := CompilePermissions(program, ev, content); err == nil {
			t.Fatalf("malformed content accepted: %q", content)
		}
	}
}

func TestCompilePermissionsRequiresCaptureTime(t *testing.T) {
	program := testProgram()
	ev := testEvidence("ev-1", domain.SourcePolicy)
	ev.CapturedAt = time.Time{}
	if _, err := CompilePermissions(program, ev, testPolicyContent); err == nil {
		t.Fatal("evidence without captured_at compiled")
	}
}

// TestCompiledPermissionsVerifyThroughFileContentReader is the wiring test:
// the file-backed reader supplies content, the compiler turns explicit
// PERMISSION lines into facts, and BuildPlatformPolicy verifies them and grants
// CanRelax. No caller-supplied selectors or hashes are trusted.
func TestCompiledPermissionsVerifyThroughFileContentReader(t *testing.T) {
	dir := t.TempDir()
	content := "平台政策原文\n" + testPermissionLine + "\n"
	writeEvidenceFile(t, dir, "ev-1.json", content)
	ev := domain.Evidence{
		ID: "ev-1", Platform: domain.PlatformButian, SourceType: domain.SourcePolicy,
		CapturedAt: testNow, ContentHash: redact.Hash([]byte(content)), RawRef: "ev-1.json",
	}
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	program := testProgram()
	store := fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{ev.ID: ev}}

	compiled, err := CompilePermissions(program, ev, content)
	if err != nil {
		t.Fatal(err)
	}
	pp, err := BuildPlatformPolicy(context.Background(), program, compiled.Permissions, store, reader, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if pp.Len() != 1 {
		t.Fatalf("verified permissions = %d, want 1", pp.Len())
	}
	res, err := pp.Evaluate(context.Background(), program, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanRelax || res.Decision != rules.DecisionAllow {
		t.Fatalf("evaluation = %+v", res)
	}
}

func TestCompilePermissionsHashMismatchStillFailsVerification(t *testing.T) {
	// The compiler is pure parsing; authority comes only from BuildPlatformPolicy.
	// If the file content no longer matches the stored hash, verification must
	// reject the compiled fact.
	dir := t.TempDir()
	writeEvidenceFile(t, dir, "ev-1.json", testPolicyContent)
	ev := domain.Evidence{
		ID: "ev-1", Platform: domain.PlatformButian, SourceType: domain.SourcePolicy,
		CapturedAt: testNow, ContentHash: redact.Hash([]byte("different")), RawRef: "ev-1.json",
	}
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	program := testProgram()
	store := fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{ev.ID: ev}}
	compiled, err := CompilePermissions(program, ev, testPolicyContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlatformPolicy(context.Background(), program, compiled.Permissions, store, reader, testNow); err == nil {
		t.Fatal("hash-mismatched content granted platform authority")
	}
}
