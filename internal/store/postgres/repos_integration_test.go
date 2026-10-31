package postgres

import (
	"context"
	"encoding/json"
	"errors"
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
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM consumed_approvals WHERE approval_id LIKE 'it-approval-%'`)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM audit_events WHERE entity_id=$1`, string(progID))
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM ledger_entries WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM submissions WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM report_drafts WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM findings WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM research_tasks WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM opportunities WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM action_decisions WHERE program_id=$1`, progID)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM derived_states WHERE program_id=$1`, progID)
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
		Reward:      domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation: domain.AutomationPolicy{
			AIAnalysis: domain.CapUnknown, AutonomousAgent: domain.CapUnknown,
			ActiveHTTP: domain.CapUnknown, BrowserAutomation: domain.CapUnknown,
			AutomatedScanning: domain.CapUnknown, DirectoryEnumeration: domain.CapUnknown,
			SubdomainDiscovery: domain.CapUnknown, HighConcurrency: domain.CapUnknown,
			BruteForce: domain.CapUnknown, CredentialStuffing: domain.CapUnknown,
			FileUpload: domain.CapUnknown, StateMutation: domain.CapUnknown,
			RCEVerification: domain.CapUnknown, SensitiveDataRead: domain.CapUnknown,
			InternalNetwork: domain.CapUnknown, LateralMovement: domain.CapUnknown,
			DoS: domain.CapUnknown,
		},
		ObservedAt: now,
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
	// Re-running the same extraction is idempotent; reusing an ID for
	// different content is rejected.
	if err := r.PutFacts(ctx, []domain.ObservedFact{f}); err != nil {
		t.Fatalf("idempotent re-put: %v", err)
	}
	conflict := f
	conflict.Value = "PAUSED"
	if err := r.PutFacts(ctx, []domain.ObservedFact{conflict}); err == nil {
		t.Fatal("conflicting fact content should be rejected")
	}
	if err := r.SupersedeFact(ctx, factID, factID); err == nil {
		t.Fatal("self-supersede should be rejected")
	}

	otherProgID := domain.ProgramID("it-other-prog-" + suffix)
	otherFact := f
	otherFact.ID = factID + "-other"
	otherFact.ProgramID = &otherProgID
	if err := r.PutFacts(ctx, []domain.ObservedFact{otherFact}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM observed_facts WHERE program_id=$1`, otherProgID)
	})
	if err := r.SupersedeFact(ctx, factID, otherFact.ID); err == nil {
		t.Fatal("cross-entity supersede should be rejected")
	}

	stateID := domain.StateID("it-state-" + suffix)
	st := domain.DerivedState{
		ID: stateID, Subject: domain.StateProgramState, Platform: domain.PlatformButian,
		ProgramID: &progID, State: "RUNNING", Inputs: []domain.FactID{factID},
		ObservedAt: now, DerivedAt: now, ValidUntil: now.Add(time.Hour),
		Confidence: 0.9, RuleVersion: "it-v1",
	}
	if err := r.PutState(ctx, st); err != nil {
		t.Fatal(err)
	}
	st.RuleVersion = "it-v2"
	st.Confidence = 1
	if err := r.PutState(ctx, st); err != nil {
		t.Fatal(err)
	}
	var ruleVersion string
	var confidence float64
	if err := r.Pool.QueryRow(ctx,
		`SELECT rule_version, confidence FROM derived_states WHERE id=$1`, stateID,
	).Scan(&ruleVersion, &confidence); err != nil {
		t.Fatal(err)
	}
	if ruleVersion != "it-v2" || confidence != 1 {
		t.Fatalf("state upsert did not refresh metadata: %s %v", ruleVersion, confidence)
	}

	states, err := r.ListStates(ctx, domain.PlatformButian, domain.StateProgramState)
	if err != nil {
		t.Fatal(err)
	}
	foundState := false
	for _, s := range states {
		if s.ID == stateID {
			foundState = true
			if s.State != "RUNNING" || len(s.Inputs) == 0 {
				t.Fatalf("state roundtrip = %+v", s)
			}
		}
	}
	if !foundState {
		t.Fatal("derived state not listed")
	}

	// Superseding an input fact invalidates the derived state.
	f2 := f
	f2.ID = factID + "-new"
	f2.ObservedAt = now.Add(time.Second)
	f2.Value = "PAUSED"
	if err := r.PutFacts(ctx, []domain.ObservedFact{f2}); err != nil {
		t.Fatal(err)
	}
	if err := r.SupersedeFact(ctx, factID, f2.ID); err != nil {
		t.Fatal(err)
	}
	var expired bool
	if err := r.Pool.QueryRow(ctx,
		`SELECT valid_until <= now() FROM derived_states WHERE id=$1`, stateID,
	).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if !expired {
		t.Fatal("derived state was not invalidated by fact supersede")
	}

	expires := now.Add(5 * time.Minute)
	d := domain.ActionDecision{
		ID: domain.DecisionID("it-dec-" + suffix), RequestID: "it-req-" + suffix, ProgramID: progID,
		Action: domain.ActionViewRules, Decision: domain.DecisionAllow,
		Reasons: []string{}, GatesEvaluated: []domain.EligibilityGate{},
		PolicyVersion: "it", DecidedAt: now, ExpiresAt: &expires,
	}
	if err := r.PutDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	gotDecision, err := r.GetDecision(ctx, d.ID)
	if err != nil || gotDecision.Decision != d.Decision || gotDecision.RequestID != d.RequestID {
		t.Fatalf("decision roundtrip: %+v %v", gotDecision, err)
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
	if _, err := auditStore.Append(ctx, audit.Event{
		Type: "it.bad", EntityType: "program", EntityID: string(progID), RecordedAt: now,
		Payload: json.RawMessage(`{"phone":"13800138000"}`),
	}); err == nil {
		t.Fatal("audit store must reject payloads containing sensitive values")
	}

	ledger := NewPostgresApprovalLedger(r.Pool)
	approvalID := "it-approval-" + suffix
	if err := ledger.Consume(ctx, approvalID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Consume(ctx, approvalID); !errors.Is(err, ErrApprovalConsumed) {
		t.Fatalf("second consume = %v, want ErrApprovalConsumed", err)
	}
}
