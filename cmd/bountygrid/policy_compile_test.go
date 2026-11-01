package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/authz"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

const compileTestLine = "PERMISSION program=p1 action=ACTIVE_HTTP hosts=api.example.com methods=GET paths=/v1"

func compileTestProgram() domain.Program {
	return domain.Program{
		ID: "p1", Platform: domain.PlatformButian,
		PolicyObservedAt: testNow,
	}
}

type stubEvidenceStore struct {
	items map[domain.EvidenceID]domain.Evidence
	err   error
}

func (s stubEvidenceStore) GetEvidence(_ context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	if s.err != nil {
		return domain.Evidence{}, s.err
	}
	ev, ok := s.items[id]
	if !ok {
		return domain.Evidence{}, domain.ErrNotFound
	}
	return ev, nil
}

type stubContentReader map[domain.EvidenceID]string

func (s stubContentReader) ReadContent(_ context.Context, ev domain.Evidence) (string, error) {
	content, ok := s[ev.ID]
	if !ok {
		return "", domain.ErrNotFound
	}
	return content, nil
}

func compileTestEvidence(id domain.EvidenceID, sourceType domain.SourceType, content string) domain.Evidence {
	return domain.Evidence{
		ID: id, Platform: domain.PlatformButian, SourceType: sourceType,
		CapturedAt: testNow, ContentHash: redact.Hash([]byte(content)),
	}
}

func TestParsePolicyCompileOptions(t *testing.T) {
	opts, err := parsePolicyCompileOptions([]string{
		"-program", "vulbox/444", "-evidence", "ev-1, ev-2", "-content-root", ".evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Program != "vulbox/444" || len(opts.Evidence) != 2 ||
		opts.Evidence[0] != "ev-1" || opts.Evidence[1] != "ev-2" ||
		opts.ContentRoot != ".evidence" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parsePolicyCompileOptions(nil); err == nil {
		t.Fatal("missing -program should fail")
	}
	if _, err := parsePolicyCompileOptions([]string{"-program", "p1", "-content-root", " "}); err == nil {
		t.Fatal("blank content root should fail")
	}
}

func TestCompilePlatformAuthorityFromEvidence(t *testing.T) {
	content := "平台规则原文\n" + compileTestLine + "\n"
	ev := compileTestEvidence("ev-1", domain.SourcePolicy, content)
	store := stubEvidenceStore{items: map[domain.EvidenceID]domain.Evidence{"ev-1": ev}}
	reader := stubContentReader{"ev-1": content}
	program := compileTestProgram()

	out, pp, err := compilePlatformAuthority(
		context.Background(), store, reader, program, []domain.EvidenceID{"ev-1"}, testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || len(out.Permissions) != 1 {
		t.Fatalf("output = %+v", out)
	}
	got := out.Permissions[0]
	if got.EvidenceID != "ev-1" || got.Action != "ACTIVE_HTTP" ||
		len(got.Hosts) != 1 || got.Hosts[0] != "api.example.com" ||
		len(got.Methods) != 1 || got.Methods[0] != "GET" {
		t.Fatalf("permission = %+v", got)
	}
	if pp == nil || pp.Len() != 1 || out.Fingerprint == "" {
		t.Fatalf("policy = %+v fingerprint=%q", pp, out.Fingerprint)
	}
}

func TestCompilePlatformAuthoritySkipsNonAuthorizingEvidence(t *testing.T) {
	content := compileTestLine + "\n"
	ev := compileTestEvidence("ev-program", domain.SourceProgram, content)
	store := stubEvidenceStore{items: map[domain.EvidenceID]domain.Evidence{"ev-program": ev}}
	reader := stubContentReader{"ev-program": content}

	out, pp, err := compilePlatformAuthority(
		context.Background(), store, reader, compileTestProgram(),
		[]domain.EvidenceID{"ev-program"}, testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 0 || len(out.Skipped) != 1 || pp.Len() != 0 {
		t.Fatalf("output = %+v policy=%+v", out, pp)
	}
	if !strings.Contains(out.Skipped[0].Reason, "POLICY") && !strings.Contains(out.Skipped[0].Reason, "ANNOUNCEMENT") {
		t.Fatalf("skip reason = %q", out.Skipped[0].Reason)
	}
}

func TestCompilePlatformAuthoritySeparatesRestrictedPermission(t *testing.T) {
	content := "PERMISSION program=p1 action=ACTIVE_HTTP hosts=api.example.com methods=GET restrictions=human_review\n"
	ev := compileTestEvidence("ev-1", domain.SourcePolicy, content)
	store := stubEvidenceStore{items: map[domain.EvidenceID]domain.Evidence{"ev-1": ev}}
	reader := stubContentReader{"ev-1": content}

	out, pp, err := compilePlatformAuthority(
		context.Background(), store, reader, compileTestProgram(), []domain.EvidenceID{"ev-1"}, testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 0 || len(out.Restricted) != 1 || pp.Len() != 0 {
		t.Fatalf("output = %+v policy=%+v", out, pp)
	}
}

func TestCompilePlatformAuthorityFailsClosedOnUnreadableEvidence(t *testing.T) {
	content := compileTestLine + "\n"
	ev := compileTestEvidence("ev-1", domain.SourcePolicy, content)
	store := stubEvidenceStore{items: map[domain.EvidenceID]domain.Evidence{"ev-1": ev}}
	// No content registered: reading must fail, not silently drop authority.
	_, _, err := compilePlatformAuthority(
		context.Background(), store, stubContentReader{}, compileTestProgram(),
		[]domain.EvidenceID{"ev-1"}, testNow,
	)
	if err == nil {
		t.Fatal("unreadable policy evidence did not fail")
	}
}

func TestCompilePlatformAuthorityRejectsMissingEvidence(t *testing.T) {
	_, _, err := compilePlatformAuthority(
		context.Background(), stubEvidenceStore{}, stubContentReader{}, compileTestProgram(),
		[]domain.EvidenceID{"ev-missing"}, testNow,
	)
	if err == nil {
		t.Fatal("missing evidence did not fail")
	}
}

func TestCompilePlatformAuthorityRejectsExpiredPermission(t *testing.T) {
	content := compileTestLine + "\n"
	ev := compileTestEvidence("ev-1", domain.SourcePolicy, content)
	ev.CapturedAt = testNow.Add(-2 * domain.PolicyMaxAge)
	store := stubEvidenceStore{items: map[domain.EvidenceID]domain.Evidence{"ev-1": ev}}
	reader := stubContentReader{"ev-1": content}
	// Capture the evidence long before the decision time so the permission
	// window is already over: verification must reject rather than keep
	// authority.
	_, _, err := compilePlatformAuthority(
		context.Background(), store, reader, compileTestProgram(),
		[]domain.EvidenceID{"ev-1"}, testNow,
	)
	if err == nil {
		t.Fatal("expired permission produced platform authority")
	}
}

var _ authz.EvidenceContentReader = stubContentReader(nil)
