package report

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func reportProgram(platform domain.Platform, typ domain.ProgramType) domain.Program {
	return domain.Program{
		ID: "p1", Platform: platform, NativeID: "native-444", Name: "Acme", Type: typ,
		Status: domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Scope:  domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: testNow},
		Reward: domain.RewardPolicy{TaxBasis: domain.TaxUnknown}, ObservedAt: testNow,
	}
}

func reportFinding() domain.Finding {
	a := domain.AssetID("a1")
	return domain.Finding{
		ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: &a,
		Title: "SQL injection", VulnType: "SQLI", Severity: "HIGH",
		Evidence: []domain.EvidenceID{"ev-finding"}, Status: "VERIFIED", CreatedAt: testNow,
	}
}

func reportVerification() domain.Verification {
	return domain.Verification{
		ID: "ver/f1/1", FindingID: "f1", Verdict: domain.VerdictVerified,
		Reproduced: true, InScope: true, ImpactVerified: true, DataMinimized: true,
		DuplicateChecked: true, DuplicateRisk: 0.1,
		VerifiedBy: "rule/verify@0.1.0", VerifiedAt: testNow,
		Evidence:   []domain.EvidenceID{"ev-finding", "ev-repro", "ev-impact"},
		ReceiptIDs: []domain.ReceiptID{"rcpt/1"},
	}
}

func completeFields() Fields {
	return Fields{
		AssetURL:    "https://a1.example.com/search?q=1",
		Summary:     "The search endpoint concatenates input into SQL.",
		Steps:       "1. Open the URL\n2. Send the payload",
		PoC:         "GET /search?q=1'",
		Impact:      "Read arbitrary rows from the search database.",
		Remediation: "Use parameterised queries.",
		AccountName: "account",
		Category:    "Web",
	}
}

func validInput() Input {
	return Input{
		Finding: reportFinding(), Program: reportProgram(domain.PlatformVulbox, domain.TypeEnterpriseSRC),
		Verification: reportVerification(), Fields: completeFields(), Now: testNow,
	}
}

func TestBuildReadyVulboxDraft(t *testing.T) {
	res, err := Build(validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready || len(res.Missing) != 0 {
		t.Fatalf("ready=%v missing=%v", res.Ready, res.Missing)
	}
	if res.Draft.Status != "DRAFT" || res.Draft.Fields["title"] != "SQL injection" ||
		res.Draft.Fields["vuln_type"] != "SQLI" || res.Draft.Fields["severity"] != "HIGH" {
		t.Fatalf("draft = %+v", res.Draft)
	}
	if err := res.Draft.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFieldsAreReported(t *testing.T) {
	in := validInput()
	in.Fields.PoC = ""
	in.Fields.Remediation = ""
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready {
		t.Fatal("draft without poc/remediation must not be ready")
	}
	if !contains(res.Missing, "poc") || !contains(res.Missing, "remediation") {
		t.Fatalf("missing = %v", res.Missing)
	}
}

func TestXianzhiRequiresAccountAndDefaultsTask(t *testing.T) {
	in := validInput()
	in.Program = reportProgram(domain.PlatformXianzhi, domain.TypeBrandSRC)
	in.Fields.AccountName = ""
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Missing, "account_name") {
		t.Fatalf("missing = %v", res.Missing)
	}
	if res.Draft.Fields["task_id"] != "native-444" {
		t.Fatalf("task_id = %q, want program native id", res.Draft.Fields["task_id"])
	}
}

func TestButianRequiresCategoryAndDefaultsVendor(t *testing.T) {
	in := validInput()
	in.Program = reportProgram(domain.PlatformButian, domain.TypeEnterpriseSRC)
	in.Fields.Category = ""
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Missing, "category") {
		t.Fatalf("missing = %v", res.Missing)
	}
	if res.Draft.Fields["vendor"] != "Acme" {
		t.Fatalf("vendor = %q", res.Draft.Fields["vendor"])
	}
}

func TestButianGrowRequiresAIModelAndTool(t *testing.T) {
	in := validInput()
	in.Program = reportProgram(domain.PlatformButian, domain.TypeCampusProgram)
	in.Fields.Category = "Web"
	in.Fields.AssetScope = "域名"
	in.Fields.Weight = "1"
	in.Fields.Activity = "GROW"
	in.Fields.Industry = "汽车"
	in.Fields.Region = "北京"
	in.Fields.Contact = "platform account"
	in.AI = AIUse{Assisted: true, HumanVerification: "manually reproduced"}
	in.Verification.AIAssisted = true
	in.Verification.HumanVerified = true
	in.Verification.HumanVerificationRef = "manually reproduced"
	in.Verification.HumanReviewID = "hr1"
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ai_model_name", "ai_model_version", "ai_tool_name"} {
		if !contains(res.Missing, key) {
			t.Fatalf("missing = %v, want %s", res.Missing, key)
		}
	}
	in.AI.ModelName, in.AI.ModelVersion, in.AI.ToolName = "model", "1.0", "tool"
	res, err = Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready {
		t.Fatalf("AI disclosure still missing: %v", res.Missing)
	}
	if res.Draft.AIDisclosure["model_name"] != "model" ||
		res.Draft.AIDisclosure["human_verification"] != "manually reproduced" {
		t.Fatalf("disclosure = %v", res.Draft.AIDisclosure)
	}
}

func TestBuildRejectsUnverified(t *testing.T) {
	in := validInput()
	in.Verification.Verdict = domain.VerdictNeedsEvidence
	if _, err := Build(in); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("err = %v, want ErrNotVerified", err)
	}

	in = validInput()
	in.Verification.FindingID = "other"
	if _, err := Build(in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestAttachmentRules(t *testing.T) {
	in := validInput()
	in.Attachments = []string{"evidence.exe"}
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("vulbox attachment err = %v", err)
	}
	in.Attachments = []string{"evidence.zip"}
	if _, err := Build(in); err != nil {
		t.Fatal(err)
	}

	in = validInput()
	in.Program = reportProgram(domain.PlatformButian, domain.TypeEnterpriseSRC)
	in.Fields.Category = "Web"
	in.Attachments = []string{"evidence.pdf"}
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("butian attachment err = %v", err)
	}
}

func TestManualFindingWithAIAssistedReportIsAllowed(t *testing.T) {
	in := validInput()
	in.AI = AIUse{Assisted: true, HumanVerification: "human reproduced the PoC"}
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready {
		t.Fatalf("manual finding + AI-written report should be ready: %v", res.Missing)
	}
	if res.Draft.AIDisclosure["human_verification"] == "" {
		t.Fatal("AI disclosure missing human verification")
	}
}

func TestAIAssistedVerificationRequiresDisclosure(t *testing.T) {
	in := validInput()
	in.Verification.AIAssisted = true
	in.Verification.HumanVerified = true
	in.Verification.HumanVerificationRef = "human reviewed"
	in.Verification.HumanReviewID = "hr1"
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || !contains(res.Missing, "ai_disclosure") {
		t.Fatalf("AI-assisted verification without disclosure = %v", res.Missing)
	}
}

func TestSensitiveReportTextIsRejected(t *testing.T) {
	in := validInput()
	in.Fields.PoC = "GET /search\nCookie: session=abcdef123456"
	if _, err := Build(in); !errors.Is(err, ErrSensitive) {
		t.Fatalf("err = %v, want ErrSensitive", err)
	}
}

func TestSensitiveReportAllowsLongTimestamp(t *testing.T) {
	in := validInput()
	in.Fields.PoC = `{"timestamp": 1700000000000000000}`
	if _, err := Build(in); err != nil {
		t.Fatalf("long timestamp should not be mistaken for a bank card: %v", err)
	}
}

func TestSensitiveReportRejectsCardContext(t *testing.T) {
	in := validInput()
	in.Fields.PoC = "bank card 6222020202020202"
	if _, err := Build(in); !errors.Is(err, ErrSensitive) {
		t.Fatalf("err = %v, want ErrSensitive", err)
	}
}

func TestRecheckRejectsCrossLink(t *testing.T) {
	built, err := Build(validInput())
	if err != nil {
		t.Fatal(err)
	}
	verification := reportVerification()
	verification.FindingID = "other-finding"
	if _, err := Recheck(built.Draft, reportFinding(), reportProgram(domain.PlatformVulbox, domain.TypeEnterpriseSRC), verification); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestRecheckDoesNotFillDefaults(t *testing.T) {
	built, err := Build(validInput())
	if err != nil {
		t.Fatal(err)
	}
	draft := built.Draft
	delete(draft.Fields, "title")
	res, err := Recheck(draft, reportFinding(), reportProgram(domain.PlatformVulbox, domain.TypeEnterpriseSRC), reportVerification())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || !contains(res.Missing, "title") {
		t.Fatalf("stored draft missing title = %v", res.Missing)
	}
}

func TestRecheckRejectsEntityMismatch(t *testing.T) {
	built, err := Build(validInput())
	if err != nil {
		t.Fatal(err)
	}
	draft := built.Draft
	draft.VerificationID = "other"
	if _, err := Recheck(draft, reportFinding(), reportProgram(domain.PlatformVulbox, domain.TypeEnterpriseSRC), reportVerification()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func strictAttachmentInput(t *testing.T) (string, Input) {
	t.Helper()
	dir := t.TempDir()
	in := validInput()
	in.Attachments = []string{"evidence.zip"}
	in.AttachmentsRoot = dir
	in.AttachmentEvidence = map[string][]domain.EvidenceID{
		"evidence.zip": {"ev-finding"},
	}
	return dir, in
}

func TestStrictAttachmentValidationAcceptsMatchingMagic(t *testing.T) {
	dir, in := strictAttachmentInput(t)
	if err := os.WriteFile(filepath.Join(dir, "evidence.zip"), []byte("PK\x03\x04hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.AttachmentHashes["evidence.zip"] == "" {
		t.Fatal("attachment hash missing")
	}
	if err := VerifyAttachmentHashes(res.Draft, dir, 0); err != nil {
		t.Fatal(err)
	}
	// Changing the file after approval invalidates the hash.
	if err := os.WriteFile(filepath.Join(dir, "evidence.zip"), []byte("PK\x03\x04changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAttachmentHashes(res.Draft, dir, 0); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("changed attachment err = %v", err)
	}
}

func TestStrictAttachmentValidationRejectsWrongMagic(t *testing.T) {
	dir, in := strictAttachmentInput(t)
	if err := os.WriteFile(filepath.Join(dir, "evidence.zip"), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("err = %v, want ErrInvalidAttachment", err)
	}
}

func TestStrictAttachmentValidationRejectsTraversalAndMissingProvenance(t *testing.T) {
	dir, in := strictAttachmentInput(t)
	in.Attachments = []string{"../outside.zip"}
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("traversal err = %v", err)
	}

	dir, in = strictAttachmentInput(t)
	if err := os.WriteFile(filepath.Join(dir, "evidence.zip"), []byte("PK\x03\x04hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.AttachmentEvidence = nil
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("missing provenance err = %v", err)
	}
}

func TestStrictAttachmentValidationScansText(t *testing.T) {
	dir, in := strictAttachmentInput(t)
	in.Attachments = []string{"note.pdf"}
	in.AttachmentEvidence = map[string][]domain.EvidenceID{"note.pdf": {"ev-finding"}}
	if err := os.WriteFile(filepath.Join(dir, "note.pdf"), []byte("%PDF-1.4 contact a@b.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(in); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("sensitive attachment err = %v", err)
	}
}
