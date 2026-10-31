package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

func integrationRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewRepo(pool), ctx
}

func TestRepoRoundTrip(t *testing.T) {
	r, ctx := integrationRepo(t)
	now := time.Now().UTC().Truncate(time.Second)
	suffix := now.Format("150405.000000000")
	accID := domain.AccountID("it-acc-" + suffix)
	progID := domain.ProgramID("it-prog-" + suffix)
	evID := domain.EvidenceID("it-ev-" + suffix)
	factID := domain.FactID("it-fact-" + suffix)

	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM audit_events WHERE entity_id=$1`, string(progID))
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM ledger_entries WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM submissions WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM report_drafts WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM findings WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM research_tasks WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM opportunities WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM action_decisions WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM observed_facts WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM evidence WHERE id=$1`, evID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM programs WHERE id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, accID)
	})

	ident := true
	acc := domain.Account{
		ID: accID, Platform: domain.PlatformButian, Registered: true,
		IdentityVerified: &ident, Level: "实习白帽", ObservedAt: now,
		Capability: domain.AccountCapability{CanSubmit: true, EvaluatedAt: now},
	}
	if err := r.UpsertAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	gotAcc, err := r.GetAccount(ctx, accID)
	if err != nil || gotAcc.Level != acc.Level {
		t.Fatalf("account roundtrip: %+v %v", gotAcc, err)
	}

	prog := domain.Program{
		ID: progID, Platform: domain.PlatformButian, Name: "it", Type: domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: domain.StatusRunning, ObservedAt: now, ValidUntil: now.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: now, PolicyVersion: "it"},
		Scope:       domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: now},
		ObservedAt:  now,
	}
	if err := r.UpsertProgram(ctx, prog); err != nil {
		t.Fatal(err)
	}
	gotProg, err := r.GetProgram(ctx, progID)
	if err != nil || gotProg.Name != prog.Name {
		t.Fatalf("program roundtrip: %+v %v", gotProg, err)
	}

	ev := domain.Evidence{
		ID: evID, Platform: domain.PlatformButian, SourceType: domain.SourceProgram,
		Transport: domain.TransportBrowserPage, URL: "https://example.test", CapturedAt: now,
		ContentHash: "sha256:it", RawRef: "local://it",
	}
	if err := r.PutEvidence(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetEvidence(ctx, evID); err != nil {
		t.Fatal(err)
	}

	f := domain.ObservedFact{
		ID: factID, Subject: domain.SubjectProgram, Platform: domain.PlatformButian,
		ProgramID: &progID, Predicate: "program.status", Value: "RUNNING",
		ObservedAt: now, EvidenceRefs: []domain.EvidenceID{evID},
		Confidence: 0.9, ExtractorVersion: "it",
	}
	if err := r.PutFacts(ctx, []domain.ObservedFact{f}); err != nil {
		t.Fatal(err)
	}
	gotFacts, err := r.ListFactsByProgram(ctx, progID)
	if err != nil || len(gotFacts) != 1 {
		t.Fatalf("facts: %+v %v", gotFacts, err)
	}

	d := domain.ActionDecision{
		ID: domain.DecisionID("it-dec-" + suffix), RequestID: "it-req-" + suffix, ProgramID: progID,
		Action: domain.ActionViewRules, Decision: domain.DecisionAllow,
		Reasons: []string{}, GatesEvaluated: []domain.EligibilityGate{},
		PolicyVersion: "it", DecidedAt: now,
	}
	if err := r.PutDecision(ctx, d); err != nil {
		t.Fatal(err)
	}

	oppID := domain.OpportunityID("it-opp-" + suffix)
	taskID := domain.TaskID("it-task-" + suffix)
	findingID := domain.FindingID("it-finding-" + suffix)
	reportID := "it-report-" + suffix
	subID := domain.SubmissionID("it-sub-" + suffix)

	opp := domain.Opportunity{ID: oppID, ProgramID: progID, Confidence: 0.5, EvaluatedAt: now}
	if err := r.PutOpportunity(ctx, opp); err != nil {
		t.Fatal(err)
	}
	task := domain.ResearchTask{ID: taskID, OpportunityID: oppID, ProgramID: progID, Status: "CREATED", CreatedAt: now}
	if err := r.PutResearchTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	finding := domain.Finding{ID: findingID, TaskID: taskID, ProgramID: progID, Title: "it", Status: "CANDIDATE", CreatedAt: now}
	if err := r.PutFinding(ctx, finding); err != nil {
		t.Fatal(err)
	}
	report := domain.ReportDraft{ID: reportID, FindingID: findingID, ProgramID: progID, Status: "DRAFT", CreatedAt: now}
	if err := r.PutReportDraft(ctx, report); err != nil {
		t.Fatal(err)
	}
	sub := domain.Submission{ID: subID, ReportID: reportID, ProgramID: progID, SubmittedAt: now, Receipt: "it", Status: "SUBMITTED", ApprovalID: "it-approval"}
	if err := r.PutSubmission(ctx, sub); err != nil {
		t.Fatal(err)
	}
	entry := domain.LedgerEntry{
		ID: "it-ledger-" + suffix, Platform: domain.PlatformButian, ProgramID: progID,
		Kind: domain.LedgerPaid, Amount: domain.Money{Amount: 100, Currency: "CNY"},
		Status: "PAID", OccurredAt: now,
	}
	if err := r.PutLedgerEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}

	auditStore := NewAuditStore(r.Pool)
	if _, err := auditStore.Append(ctx, audit.Event{
		Type: "program.observed", EntityType: "program", EntityID: string(progID),
		ActorKind: "ADAPTER", ActorID: "it", RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := auditStore.List(ctx, audit.Filter{EntityID: string(progID)})
	if err != nil || len(events) == 0 {
		t.Fatalf("audit list: %+v %v", events, err)
	}
}
