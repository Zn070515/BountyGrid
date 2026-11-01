package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/report"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type fakeReportStore struct {
	program      domain.Program
	finding      domain.Finding
	verification domain.Verification
	drafts       []domain.ReportDraft
}

func (s *fakeReportStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	if s.program.ID != id {
		return domain.Program{}, postgres.ErrNotFound
	}
	return s.program, nil
}
func (s *fakeReportStore) GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error) {
	if s.finding.ID != id {
		return domain.Finding{}, postgres.ErrNotFound
	}
	return s.finding, nil
}
func (s *fakeReportStore) GetVerification(ctx context.Context, id string) (domain.Verification, error) {
	if s.verification.ID != id {
		return domain.Verification{}, postgres.ErrNotFound
	}
	return s.verification, nil
}
func (s *fakeReportStore) PutReportDraft(ctx context.Context, d domain.ReportDraft) error {
	s.drafts = append(s.drafts, d)
	return nil
}

func reportProgramFixture() domain.Program {
	return domain.Program{
		ID: "p1", Platform: domain.PlatformVulbox, NativeID: "444", Name: "Acme",
		Type:       domain.TypeEnterpriseSRC,
		Status:     domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Scope:      domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: testNow},
		Reward:     domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		ObservedAt: testNow,
	}
}

func reportFindingFixture() domain.Finding {
	a := domain.AssetID("a1")
	return domain.Finding{
		ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: &a,
		Title: "SQL injection", VulnType: "SQLI", Severity: "HIGH",
		Evidence: []domain.EvidenceID{"ev-finding"}, Status: "VERIFIED", CreatedAt: testNow,
	}
}

func reportVerificationFixture() domain.Verification {
	return domain.Verification{
		ID: "ver/f1/1", FindingID: "f1", Verdict: domain.VerdictVerified,
		Reproduced: true, InScope: true, ImpactVerified: true, DataMinimized: true,
		DuplicateChecked: true, DuplicateRisk: 0.1,
		VerifiedBy: "rule/verify@0.1.0", VerifiedAt: testNow,
		Evidence: []domain.EvidenceID{"ev-finding", "ev-repro"},
	}
}

func validReportFile() reportFile {
	return reportFile{
		FindingID: "f1", ProgramID: "p1", VerificationID: "ver/f1/1",
		Fields: report.Fields{
			AssetURL: "https://a1.example.com/search", Summary: "SQL injection",
			Steps: "1. request\n2. observe", PoC: "GET /search?q=1'",
			Impact: "read rows", Remediation: "parameterize",
		},
	}
}

func TestExecuteReportReadyAndPersisted(t *testing.T) {
	store := &fakeReportStore{
		program: reportProgramFixture(), finding: reportFindingFixture(),
		verification: reportVerificationFixture(),
	}
	out, err := executeReport(context.Background(), store, validReportFile(), true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Ready || out.Missing != nil || !out.Persisted {
		t.Fatalf("out = %+v", out)
	}
	if len(store.drafts) != 1 || store.drafts[0].Status != "DRAFT" {
		t.Fatalf("drafts = %+v", store.drafts)
	}
}

func TestExecuteReportMissingFields(t *testing.T) {
	store := &fakeReportStore{
		program: reportProgramFixture(), finding: reportFindingFixture(),
		verification: reportVerificationFixture(),
	}
	in := validReportFile()
	in.Fields.PoC = ""
	out, err := executeReport(context.Background(), store, in, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Ready || len(out.Missing) != 1 || out.Missing[0] != "poc" {
		t.Fatalf("out = %+v", out)
	}
}

func TestExecuteReportRequiresVerification(t *testing.T) {
	store := &fakeReportStore{program: reportProgramFixture(), finding: reportFindingFixture()}
	if _, err := executeReport(context.Background(), store, validReportFile(), false, testNow); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestParseReportOptions(t *testing.T) {
	opts, err := parseReportOptions([]string{"-file", "r.json", "-persist"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.File != "r.json" || !opts.Persist {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseReportOptions(nil); err == nil {
		t.Fatal("missing -file should fail")
	}
}
