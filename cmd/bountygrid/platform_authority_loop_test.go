package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/authz"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

// evidenceLookup adapts the fake writer's persisted evidence to
// authz.EvidenceStore for the end-to-end compile test.
type evidenceLookup struct{ writer *fakeWriter }

func (e evidenceLookup) GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	return e.writer.GetEvidence(ctx, id)
}

func (w *fakeWriter) GetEvidence(_ context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	for _, ev := range w.evidence {
		if ev.ID == id {
			return ev, nil
		}
	}
	return domain.Evidence{}, domain.ErrNotFound
}

func platformLoopProgram() domain.Program {
	unknown := domain.CapUnknown
	return domain.Program{
		ID: "vulbox/444", Platform: domain.PlatformVulbox, NativeID: "444",
		Name: "联想", Type: domain.TypeEnterpriseSRC,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, ObservedAt: testNow,
			ValidUntil: testNow.Add(time.Hour), Confidence: 1,
		},
		Eligibility: domain.EligibilityState{
			Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test",
		},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted,
			ContentsKnown: true, Include: []domain.Asset{{ID: "a1", Value: "api.lenovo.com"}},
			ObservedAt: testNow,
		},
		Reward: domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation: domain.AutomationPolicy{
			AIAnalysis: unknown, AutonomousAgent: unknown, ActiveHTTP: domain.CapConditional,
			BrowserAutomation: unknown, AutomatedScanning: unknown, DirectoryEnumeration: unknown,
			SubdomainDiscovery: unknown, HighConcurrency: unknown, BruteForce: unknown,
			CredentialStuffing: unknown, FileUpload: unknown, StateMutation: unknown,
			RCEVerification: unknown, SensitiveDataRead: unknown, InternalNetwork: unknown,
			LateralMovement: unknown, DoS: unknown,
		},
		ObservedAt: testNow,
	}
}

// TestSyncedProgramClosesPlatformAuthorityLoop proves the end-to-end path:
// `sync -program` associates the policy Evidence with Program.Evidence, and
// `policy compile` auto-discovers it, reads the redacted file, compiles the
// explicit PERMISSION line, and verifies it into a PlatformPolicy.
func TestSyncedProgramClosesPlatformAuthorityLoop(t *testing.T) {
	dir := t.TempDir()
	line := "PERMISSION program=vulbox/444 action=ACTIVE_HTTP hosts=api.lenovo.com methods=GET paths=/v1"
	content := line + "\n"
	if err := os.WriteFile(filepath.Join(dir, "policy.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	policyEvidence := domain.Evidence{
		ID: "ev-policy", Platform: domain.PlatformVulbox, SourceType: domain.SourcePolicy,
		Transport: domain.TransportBrowserPage, URL: "https://lsrc.vulbox.com/policy",
		CapturedAt: testNow, ContentHash: redact.Hash([]byte(content)), RawRef: "policy.txt",
	}
	scopeEvidence := domain.Evidence{
		ID: "ev-scope", Platform: domain.PlatformVulbox, SourceType: domain.SourceProgram,
		Transport: domain.TransportBrowserPage, URL: "https://lsrc.vulbox.com/home",
		CapturedAt: testNow, ContentHash: redact.Hash([]byte("scope")), RawRef: "scope.txt",
	}
	program := platformLoopProgram()
	reader := &fakeReader{
		program: adapter.ProgramSnapshot{Program: program, Evidence: []domain.Evidence{scopeEvidence}},
		policy: adapter.PolicySnapshot{
			Automation: program.Automation, Constraints: program.Constraints,
			ObservedAt: testNow, Evidence: []domain.Evidence{policyEvidence},
		},
	}
	writer := &fakeWriter{}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), reader, syncOptions{ProgramID: "444"}, &buf, writer); err != nil {
		t.Fatal(err)
	}
	if len(writer.programs) != 1 {
		t.Fatalf("persisted programs = %d, want 1", len(writer.programs))
	}
	stored := writer.programs[0]
	if !slices.Contains(stored.Evidence, domain.EvidenceID("ev-policy")) {
		t.Fatalf("Program.Evidence = %v, want it to include ev-policy", stored.Evidence)
	}
	if slices.Contains(stored.Evidence, domain.EvidenceID("ev-scope")) == false {
		t.Fatalf("Program.Evidence = %v, want it to include ev-scope", stored.Evidence)
	}

	ids, err := resolveCompileEvidence(stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := authz.NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, pp, err := compilePlatformAuthority(
		context.Background(), evidenceLookup{writer: writer}, contents, stored, ids, testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || pp.Len() != 1 || out.Fingerprint == "" {
		t.Fatalf("compiled = %+v policy=%+v", out, pp)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].EvidenceID != "ev-scope" {
		t.Fatalf("skipped = %+v, want ev-scope only", out.Skipped)
	}
}
