package submit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/report"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	draft        domain.ReportDraft
	program      domain.Program
	finding      domain.Finding
	verification domain.Verification
	submissions  []domain.Submission
	intents      map[string]string
}

func (s *fakeStore) GetReportDraft(ctx context.Context, id string) (domain.ReportDraft, error) {
	if s.draft.ID != id {
		return domain.ReportDraft{}, errors.New("not found")
	}
	return s.draft, nil
}
func (s *fakeStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	if s.program.ID != id {
		return domain.Program{}, errors.New("not found")
	}
	return s.program, nil
}
func (s *fakeStore) GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error) {
	if s.finding.ID != id {
		return domain.Finding{}, errors.New("not found")
	}
	return s.finding, nil
}
func (s *fakeStore) GetVerification(ctx context.Context, id string) (domain.Verification, error) {
	if s.verification.ID != id {
		return domain.Verification{}, errors.New("not found")
	}
	return s.verification, nil
}
func (s *fakeStore) GetSubmissionByReport(ctx context.Context, reportID string) (domain.Submission, bool, error) {
	for _, existing := range s.submissions {
		if existing.ReportID == reportID && existing.Status != "DRY_RUN" {
			return existing, true, nil
		}
	}
	return domain.Submission{}, false, nil
}

func (s *fakeStore) ReserveSubmissionIntent(ctx context.Context, reportID, approvalID string, now time.Time) (bool, error) {
	if s.intents == nil {
		s.intents = map[string]string{}
	}
	if _, ok := s.intents[reportID]; ok {
		return false, nil
	}
	s.intents[reportID] = approvalID
	return true, nil
}
func (s *fakeStore) ReleaseSubmissionIntent(ctx context.Context, reportID string) error {
	delete(s.intents, reportID)
	return nil
}

func (s *fakeStore) RecordSubmission(ctx context.Context, sub domain.Submission, d domain.ReportDraft) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if sub.Status != "DRY_RUN" {
		for _, existing := range s.submissions {
			if existing.ReportID == sub.ReportID && existing.Status != "DRY_RUN" {
				return errors.New("report already submitted")
			}
		}
	}
	s.submissions = append(s.submissions, sub)
	s.draft = d
	delete(s.intents, sub.ReportID)
	return nil
}

type fakeGate struct {
	decision   domain.ActionDecision
	evalErr    error
	authErr    error
	evaluated  int
	authorized int
	lastInput  gateway.EvaluateInput
}

func (g *fakeGate) Evaluate(ctx context.Context, in gateway.EvaluateInput) (domain.ActionDecision, error) {
	g.evaluated++
	g.lastInput = in
	if g.evalErr != nil {
		return domain.ActionDecision{}, g.evalErr
	}
	return g.decision, nil
}
func (g *fakeGate) AuthorizeWith(ctx context.Context, id domain.DecisionID, ai gateway.AuthorizeInput) error {
	g.authorized++
	return g.authErr
}

type fakeSubmitter struct {
	result Result
	err    error
	calls  int
}

func (s *fakeSubmitter) Submit(ctx context.Context, req Request) (Result, error) {
	s.calls++
	if s.err != nil {
		return Result{}, s.err
	}
	return s.result, nil
}

func programFixture() domain.Program {
	return domain.Program{
		ID: "p1", Platform: domain.PlatformVulbox, NativeID: "444", Name: "Acme",
		Type:   domain.TypeEnterpriseSRC,
		Status: domain.ProgramState{Status: domain.StatusRunning, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true,
			Include:    []domain.Asset{{ID: "a1", ProgramID: "p1", Value: "a1.example.com", InScope: true}},
			ObservedAt: testNow,
		},
		Reward:     domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		ObservedAt: testNow,
	}
}

func findingFixture() domain.Finding {
	a := domain.AssetID("a1")
	return domain.Finding{
		ID: "f1", TaskID: "t1", ProgramID: "p1", AssetID: &a,
		Title: "SQL injection", VulnType: "SQLI", Severity: "HIGH",
		Evidence: []domain.EvidenceID{"ev-finding"}, Status: "VERIFIED", CreatedAt: testNow,
	}
}

func verificationFixture() domain.Verification {
	return domain.Verification{
		ID: "ver/f1/1", FindingID: "f1", Verdict: domain.VerdictVerified,
		Reproduced: true, InScope: true, ImpactVerified: true, DataMinimized: true,
		DuplicateChecked: true, DuplicateRisk: 0.1,
		VerifiedBy: "rule", VerifiedAt: testNow,
		Evidence: []domain.EvidenceID{"ev-finding", "ev-repro"},
	}
}

func draftFixture(t *testing.T, complete bool) domain.ReportDraft {
	t.Helper()
	fields := report.Fields{
		AssetURL: "https://a1.example.com/search", Summary: "SQL injection",
		Steps: "1. request\n2. observe", PoC: "GET /search?q=1'",
		Impact: "read rows", Remediation: "parameterize",
	}
	if !complete {
		fields.PoC = ""
	}
	res, err := report.Build(report.Input{
		Finding: findingFixture(), Program: programFixture(), Verification: verificationFixture(),
		Fields: fields, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Draft
}

func approvalFixture(t *testing.T, draft domain.ReportDraft) *domain.HumanApproval {
	return approvalFixtureID(t, draft, "ap1")
}

func approvalFixtureID(t *testing.T, draft domain.ReportDraft, id string) *domain.HumanApproval {
	t.Helper()
	contentHash, err := report.ContentHash(draft)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(map[string]string{
		"report_id": draft.ID, "report_content_hash": contentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &domain.HumanApproval{
		ID: id, ApprovedBy: "human", ApprovedAt: testNow,
		RequestID: "submit/" + draft.ID + "/" + id, Action: domain.ActionReportSubmit,
		ProgramID: draft.ProgramID, AssetID: findingFixture().AssetID,
		ResourceID: draft.ID, ParametersHash: domain.CanonicalParametersHash(params),
		ExpiresAt: testNow.Add(time.Hour), SingleUse: true,
	}
}

func allowDecision() domain.ActionDecision {
	exp := testNow.Add(5 * time.Minute)
	return domain.ActionDecision{
		ID: "d1", RequestID: "submit", ProgramID: "p1", Action: domain.ActionReportSubmit,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow, ExpiresAt: &exp,
	}
}

func newService(t *testing.T, store Store, gate Gate, sub Submitter, aud Auditor) *Service {
	t.Helper()
	s, err := New(Options{Store: store, Gate: gate, Submitter: sub, Auditor: aud, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSubmitDryRun(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{
		draft: draft, program: programFixture(), finding: findingFixture(),
		verification: verificationFixture(),
	}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "dry-run/r", Status: "DRY_RUN", DryRun: true}}
	aud := audit.NewMemoryStore()
	s := newService(t, store, gate, sub, aud)

	submission, err := s.Submit(context.Background(), Input{
		ReportID: draft.ID, AccountID: "vulbox/account", Approval: approvalFixture(t, draft), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if submission.Status != "DRY_RUN" || submission.Receipt != "dry-run/r" {
		t.Fatalf("submission = %+v", submission)
	}
	if gate.evaluated != 1 || gate.authorized != 1 || sub.calls != 1 {
		t.Fatalf("gate/submitter calls = %d/%d/%d", gate.evaluated, gate.authorized, sub.calls)
	}
	if store.draft.Status != domain.ReportStatusDraft {
		t.Fatalf("dry run changed draft status = %s", store.draft.Status)
	}
	if len(store.submissions) != 1 || store.submissions[0].ApprovalID != "ap1" ||
		store.submissions[0].Status != "DRY_RUN" {
		t.Fatalf("submissions = %+v", store.submissions)
	}
	if _, ok := store.intents[draft.ID]; ok {
		t.Fatal("successful dry run must release the submission intent")
	}
	events, _ := aud.List(context.Background(), audit.Filter{Type: "submission.RECORDED"})
	if len(events) != 1 {
		t.Fatalf("audit = %+v", events)
	}
}

func TestSubmitRejectsApprovalBindingMismatch(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{
		draft: draft, program: programFixture(), finding: findingFixture(),
		verification: verificationFixture(),
	}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "DRY_RUN"}}
	s := newService(t, store, gate, sub, nil)
	appr := approvalFixture(t, draft)
	appr.RequestID = "wrong"

	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: appr, Now: testNow}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("mismatched approval reached the gate/submitter")
	}
}

func TestSubmitRejectsDraftChangedAfterApproval(t *testing.T) {
	draft := draftFixture(t, true)
	appr := approvalFixture(t, draft)
	draft.Fields["impact"] = "changed after approval"
	store := &fakeStore{
		draft: draft, program: programFixture(), finding: findingFixture(),
		verification: verificationFixture(),
	}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "DRY_RUN"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: appr, Now: testNow}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput for changed content", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("changed draft reached the gate/submitter")
	}
}

func TestSubmitRejectsNotReadyDraft(t *testing.T) {
	draft := draftFixture(t, false)
	store := &fakeStore{
		draft: draft, program: programFixture(), finding: findingFixture(),
		verification: verificationFixture(),
	}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "DRY_RUN"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{
		ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow,
	}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("not-ready draft reached the gate/submitter")
	}
}

func TestSubmitDenyAndAuthorizeFailureDoNotCallSubmitter(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	deny := allowDecision()
	deny.Decision = domain.DecisionDeny
	gate := &fakeGate{decision: deny}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "DRY_RUN"}}
	s := newService(t, store, gate, sub, nil)
	var derr DecisionError
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.As(err, &derr) {
		t.Fatalf("err = %v, want DecisionError", err)
	}
	if sub.calls != 0 {
		t.Fatal("submitter called on DENY")
	}

	gate = &fakeGate{decision: allowDecision(), authErr: errors.New("expired")}
	s = newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); err == nil {
		t.Fatal("authorize failure should surface")
	}
	if sub.calls != 0 {
		t.Fatal("submitter called after authorize failure")
	}
}

func TestSubmitPlatformFailureRecordsNoSubmission(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{err: errors.New("platform down")}
	aud := audit.NewMemoryStore()
	s := newService(t, store, gate, sub, aud)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.Is(err, ErrPlatformSubmit) {
		t.Fatalf("err = %v, want ErrPlatformSubmit", err)
	}
	if len(store.submissions) != 0 || store.draft.Status == domain.ReportStatusSubmitted {
		t.Fatal("failed submit recorded a submission/status")
	}
	events, _ := aud.List(context.Background(), audit.Filter{Type: "submission.SUBMIT_ERROR"})
	if len(events) != 1 {
		t.Fatalf("failure audit = %+v", events)
	}
}

func TestDryRunDoesNotBlockRealSubmission(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "dry-run/r", Status: "DRY_RUN", DryRun: true}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixtureID(t, draft, "ap1"), Now: testNow}); err != nil {
		t.Fatal(err)
	}
	if store.draft.Status != domain.ReportStatusDraft {
		t.Fatalf("dry run status = %s", store.draft.Status)
	}

	sub.result = Result{Receipt: "real-receipt", Status: "SUBMITTED"}
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixtureID(t, draft, "ap2"), Now: testNow}); err != nil {
		t.Fatalf("real submission after dry run: %v", err)
	}
	if store.draft.Status != domain.ReportStatusSubmitted {
		t.Fatalf("real status = %s", store.draft.Status)
	}
	if len(store.submissions) != 2 {
		t.Fatalf("submissions = %+v", store.submissions)
	}
}

func TestSubmitDryRunInferredFromStatus(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	sub := &fakeSubmitter{result: Result{Receipt: "dry", Status: "DRY_RUN"}}
	s := newService(t, store, &fakeGate{decision: allowDecision()}, sub, nil)
	out, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "DRY_RUN" || store.draft.Status != domain.ReportStatusDraft {
		t.Fatalf("dry-run inference failed: sub=%s draft=%s", out.Status, store.draft.Status)
	}
}

func TestSubmitDryRunFlagWinsOverStatus(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	sub := &fakeSubmitter{result: Result{Receipt: "dry", Status: "SUBMITTED", DryRun: true}}
	s := newService(t, store, &fakeGate{decision: allowDecision()}, sub, nil)
	out, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "DRY_RUN" || store.draft.Status != domain.ReportStatusDraft {
		t.Fatalf("dry-run flag not authoritative: sub=%s draft=%s", out.Status, store.draft.Status)
	}
}

func TestSubmitRejectsConcurrentIntent(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{
		draft: draft, program: programFixture(), finding: findingFixture(),
		verification: verificationFixture(),
		intents:      map[string]string{draft.ID: "other"},
	}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "SUBMITTED"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.Is(err, ErrSubmissionInProgress) {
		t.Fatalf("err = %v, want ErrSubmissionInProgress", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("concurrent intent reached the gate/submitter")
	}
}

func TestSubmitReleasesIntentOnPreSubmitFailure(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "SUBMITTED"}}
	s := newService(t, store, &fakeGate{decision: allowDecision(), authErr: errors.New("expired")}, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); err == nil {
		t.Fatal("authorize failure should surface")
	}
	if _, ok := store.intents[draft.ID]; ok {
		t.Fatal("pre-submit failure must release the intent")
	}

	store = &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	sub = &fakeSubmitter{err: errors.New("platform down")}
	s = newService(t, store, &fakeGate{decision: allowDecision()}, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); err == nil {
		t.Fatal("submitter failure should surface")
	}
	if _, ok := store.intents[draft.ID]; !ok {
		t.Fatal("submitter failure must keep the intent to prevent a double submit")
	}
}

func TestSubmitRechecksScope(t *testing.T) {
	draft := draftFixture(t, true)
	program := programFixture()
	program.Scope.Include = nil
	store := &fakeStore{draft: draft, program: program, finding: findingFixture(), verification: verificationFixture()}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "SUBMITTED"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("out-of-scope asset reached the gate/submitter")
	}
}

func TestSubmitRejectsIncompleteStoredDraft(t *testing.T) {
	draft := draftFixture(t, true)
	delete(draft.Fields, "title")
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "r", Status: "SUBMITTED"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if gate.evaluated != 0 || sub.calls != 0 {
		t.Fatal("incomplete stored draft reached the gate/submitter")
	}
}

func TestSubmitRejectsSecondRealSubmission(t *testing.T) {
	draft := draftFixture(t, true)
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	gate := &fakeGate{decision: allowDecision()}
	sub := &fakeSubmitter{result: Result{Receipt: "real", Status: "SUBMITTED"}}
	s := newService(t, store, gate, sub, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixtureID(t, draft, "ap1"), Now: testNow}); err != nil {
		t.Fatal(err)
	}
	// Simulate a lost status update; the report_id uniqueness must still reject
	// a second real submission.
	store.draft.Status = domain.ReportStatusDraft
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixtureID(t, draft, "ap2"), Now: testNow}); err == nil {
		t.Fatal("second real submission for the same report must be rejected")
	}
}

func TestSubmitRejectsAlreadySubmitted(t *testing.T) {
	draft := draftFixture(t, true)
	draft.Status = domain.ReportStatusSubmitted
	store := &fakeStore{draft: draft, program: programFixture(), finding: findingFixture(), verification: verificationFixture()}
	s := newService(t, store, &fakeGate{decision: allowDecision()}, &fakeSubmitter{}, nil)
	if _, err := s.Submit(context.Background(), Input{ReportID: draft.ID, AccountID: "acc", Approval: approvalFixture(t, draft), Now: testNow}); !errors.Is(err, ErrAlreadySubmitted) {
		t.Fatalf("err = %v, want ErrAlreadySubmitted", err)
	}
}
