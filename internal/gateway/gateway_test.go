package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

var testNow = time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)

type fakeStore struct {
	accounts  map[domain.AccountID]domain.Account
	programs  map[domain.ProgramID]domain.Program
	decisions map[domain.DecisionID]domain.ActionDecision
	getErr    error
	putErr    error
}

func (f *fakeStore) GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error) {
	if f.getErr != nil {
		return domain.Account{}, f.getErr
	}
	a, ok := f.accounts[id]
	if !ok {
		return domain.Account{}, postgres.ErrNotFound
	}
	return a, nil
}
func (f *fakeStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	if f.getErr != nil {
		return domain.Program{}, f.getErr
	}
	p, ok := f.programs[id]
	if !ok {
		return domain.Program{}, postgres.ErrNotFound
	}
	return p, nil
}
func (f *fakeStore) GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error) {
	d, ok := f.decisions[id]
	if !ok {
		return domain.ActionDecision{}, postgres.ErrNotFound
	}
	return d, nil
}
func (f *fakeStore) PutDecision(ctx context.Context, d domain.ActionDecision) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.decisions[d.ID] = d
	return nil
}

type fakeAuditor struct {
	events []audit.Event
	err    error
}

func (f *fakeAuditor) Append(ctx context.Context, e audit.Event) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.events = append(f.events, e)
	return int64(len(f.events)), nil
}

type fakeLedger struct {
	consumed map[string]bool
}

func (f *fakeLedger) Consume(ctx context.Context, id string) error {
	if f.consumed == nil {
		f.consumed = map[string]bool{}
	}
	if f.consumed[id] {
		return domain.ErrApprovalConsumed
	}
	f.consumed[id] = true
	return nil
}

func testAccount(canSubmit bool) domain.Account {
	identity, compliance := true, true
	return domain.Account{
		ID: "acc1", Platform: domain.PlatformButian, ObservedAt: testNow,
		IdentityVerified: &identity, ComplianceExamCompleted: &compliance,
		Capability: domain.AccountCapability{CanViewPrograms: true, CanViewScope: true, CanJoinPrograms: true, CanSubmit: canSubmit, EvaluatedAt: testNow},
	}
}

func testProgram(status domain.ProgramStatus) domain.Program {
	automation := domain.AutomationPolicy{
		AIAnalysis: domain.CapUnknown, AutonomousAgent: domain.CapUnknown, ActiveHTTP: domain.CapUnknown,
		BrowserAutomation: domain.CapUnknown, AutomatedScanning: domain.CapUnknown,
		DirectoryEnumeration: domain.CapUnknown, SubdomainDiscovery: domain.CapUnknown,
		HighConcurrency: domain.CapUnknown, BruteForce: domain.CapUnknown, CredentialStuffing: domain.CapUnknown,
		FileUpload: domain.CapUnknown, StateMutation: domain.CapUnknown, RCEVerification: domain.CapUnknown,
		SensitiveDataRead: domain.CapUnknown, InternalNetwork: domain.CapUnknown,
		LateralMovement: domain.CapUnknown, DoS: domain.CapUnknown,
	}
	return domain.Program{
		ID: "p1", Platform: domain.PlatformButian, Name: "test", Type: domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: status, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: testNow, PolicyVersion: "test"},
		Scope:       domain.Scope{Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true, ObservedAt: testNow},
		Reward:      domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation:  automation,
		ObservedAt:  testNow,
	}
}

func testRequest(action domain.Action) domain.ActionRequest {
	return domain.ActionRequest{
		ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
		AccountID: "acc1", ProgramID: "p1", Action: action, RequestedAt: testNow,
	}
}

func testApproval(action domain.Action, resource string) *domain.HumanApproval {
	h := sha256.Sum256(nil)
	return &domain.HumanApproval{
		ID: "h1", ApprovedBy: "human", ApprovedAt: testNow,
		RequestID: "r1", Action: action, ProgramID: "p1",
		ResourceID: resource, ParametersHash: hex.EncodeToString(h[:]),
		ExpiresAt: testNow.Add(time.Hour), SingleUse: true,
	}
}

func newGateway(store *fakeStore, auditor Auditor, ledger ApprovalLedger) *Gateway {
	g := New(store, policy.NewEngine(), auditor, ledger)
	g.SetClock(func() time.Time { return testNow })
	return g
}

func TestEvaluateAllowsViewRulesAndRecordsDecision(t *testing.T) {
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	aud := &fakeAuditor{}
	g := newGateway(store, aud, nil)
	d, err := g.Evaluate(context.Background(), EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
	if len(store.decisions) != 1 || len(aud.events) != 1 || aud.events[0].Type != "action.decision" {
		t.Fatalf("decision/audit = %d/%d", len(store.decisions), len(aud.events))
	}
}

func TestEvaluateDeniesSubmitWithoutCapability(t *testing.T) {
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(false)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)
	d, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: testRequest(domain.ActionReportSubmit), ResourceID: "report1", Now: testNow,
		HumanApproval: testApproval(domain.ActionReportSubmit, "report1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ACCOUNT_CANNOT_SUBMIT" {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
}

func TestEvaluateMissingContextPersistsDeny(t *testing.T) {
	store := &fakeStore{
		accounts: map[domain.AccountID]domain.Account{}, programs: map[domain.ProgramID]domain.Program{},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)
	d, err := g.Evaluate(context.Background(), EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "CONTEXT_NOT_FOUND" {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
	if len(store.decisions) != 1 {
		t.Fatal("context-not-found decision was not persisted")
	}
}

func TestEvaluateResolvesAssetFromStoredScope(t *testing.T) {
	p := testProgram(domain.StatusRunning)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "a1.example.com", InScope: true}, {ID: "a2", Value: "a2.example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": p},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)

	aid := domain.AssetID("a2")
	req := testRequest(domain.ActionActiveHTTP)
	req.AssetID = &aid
	d, err := g.Evaluate(context.Background(), EvaluateInput{Request: req, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s %v, want ALLOW for the stored a2 asset", d.Decision, d.Reasons)
	}

	// An ID that is not in the program's scope must never resolve, even if the
	// caller also supplies a plausible value elsewhere.
	outID := domain.AssetID("a3")
	req.AssetID = &outID
	d, err = g.Evaluate(context.Background(), EvaluateInput{Request: req, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_UNKNOWN" {
		t.Fatalf("unknown asset = %s %v, want DENY ASSET_UNKNOWN", d.Decision, d.Reasons)
	}
}

func TestApprovalConsumedAtAuthorizeOnly(t *testing.T) {
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	ledger := &fakeLedger{}
	g := newGateway(store, &fakeAuditor{}, ledger)
	in := EvaluateInput{
		Request: testRequest(domain.ActionReportSubmit), ResourceID: "report1", Now: testNow,
		HumanApproval: testApproval(domain.ActionReportSubmit, "report1"),
	}
	d, err := g.Evaluate(context.Background(), in)
	if err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("evaluate = %v %v", d.Decision, err)
	}
	if len(ledger.consumed) != 0 {
		t.Fatal("a decision must not consume the approval")
	}
	if err := g.Authorize(context.Background(), d.ID); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	if !ledger.consumed["h1"] {
		t.Fatal("authorize did not consume the single-use approval")
	}
	if err := g.Authorize(context.Background(), d.ID); err == nil {
		t.Fatal("a consumed approval must not authorize a second execution")
	}
}

func TestAuthorize(t *testing.T) {
	expires := testNow.Add(time.Hour)
	decision := domain.ActionDecision{
		ID: "d1", RequestID: "r1", ProgramID: "p1", Action: domain.ActionActiveHTTP,
		Decision: domain.DecisionAllow, PolicyVersion: "test", DecidedAt: testNow, ExpiresAt: &expires,
	}
	store := &fakeStore{
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{"d1": decision},
	}
	g := newGateway(store, nil, nil)
	if err := g.Authorize(context.Background(), "d1"); err != nil {
		t.Fatal(err)
	}

	expired := decision
	expired.ExpiresAt = ptrTime(testNow.Add(-time.Minute))
	store.decisions["d1"] = expired
	if err := g.Authorize(context.Background(), "d1"); err == nil {
		t.Fatal("expired decision must not authorize")
	}

	denied := decision
	denied.Decision = domain.DecisionDeny
	store.decisions["d1"] = denied
	if err := g.Authorize(context.Background(), "d1"); err == nil {
		t.Fatal("DENY must not authorize")
	}

	store.decisions["d1"] = decision
	paused := testProgram(domain.StatusPaused)
	paused.Status.ValidUntil = testNow.Add(time.Hour)
	store.programs["p1"] = paused
	if err := g.Authorize(context.Background(), "d1"); err == nil {
		t.Fatal("paused program must not authorize")
	}
}

func TestEvaluateInfrastructureErrorSurfaces(t *testing.T) {
	store := &fakeStore{getErr: errors.New("db down"), decisions: map[domain.DecisionID]domain.ActionDecision{}}
	g := newGateway(store, nil, nil)
	if _, err := g.Evaluate(context.Background(), EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("infrastructure error should surface")
	}
}

func TestEvaluateAuditFailureSurfaces(t *testing.T) {
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{err: errors.New("audit down")}, nil)
	if _, err := g.Evaluate(context.Background(), EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("audit failure should surface")
	}
}

type recordingStore struct {
	*fakeStore
	recorded int
	events   []audit.Event
	err      error
}

func (s *recordingStore) RecordDecisionWithAudit(ctx context.Context, d domain.ActionDecision, e audit.Event) error {
	if s.err != nil {
		return s.err
	}
	s.recorded++
	s.events = append(s.events, e)
	s.decisions[d.ID] = d
	return nil
}

func TestEvaluateUsesAtomicDecisionAuditRecorder(t *testing.T) {
	base := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	store := &recordingStore{fakeStore: base}
	g := newGateway(base, &fakeAuditor{}, nil)
	g.store = store
	if _, err := g.Evaluate(context.Background(), EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err != nil {
		t.Fatal(err)
	}
	if store.recorded != 1 || len(store.events) != 1 || store.events[0].Type != "action.decision" {
		t.Fatalf("atomic recorder not used: recorded=%d events=%+v", store.recorded, store.events)
	}
	if len(base.decisions) != 1 {
		t.Fatal("decision was not persisted by the recorder")
	}

	store.err = errors.New("audit down")
	if _, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: domain.ActionRequest{
			ID: "r2", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
			AccountID: "acc1", ProgramID: "p1", Action: domain.ActionViewRules, RequestedAt: testNow,
		},
		Now: testNow,
	}); err == nil {
		t.Fatal("transaction failure must surface")
	}
	if len(base.decisions) != 1 {
		t.Fatal("a failed transaction must not leave a decision behind")
	}
}

func TestAuthorizeRestoresRawParametersForApproval(t *testing.T) {
	pretty := []byte("{\n  \"b\": 2,\n  \"a\": 1\n}")
	compact := []byte(`{"a":1,"b":2}`)
	sum := sha256.Sum256(pretty)
	appr := &domain.HumanApproval{
		ID: "h1", ApprovedBy: "human", ApprovedAt: testNow,
		RequestID: "r1", Action: domain.ActionReportSubmit, ProgramID: "p1",
		ResourceID: "report1", ParametersHash: hex.EncodeToString(sum[:]),
		ExpiresAt: testNow.Add(time.Hour), SingleUse: true,
	}
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, &fakeLedger{})
	req := testRequest(domain.ActionReportSubmit)
	req.Parameters = pretty
	d, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: req, ResourceID: "report1", HumanApproval: appr, Now: testNow,
	})
	if err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("evaluate = %v %v", d.Decision, err)
	}

	// Simulate a JSONB round-trip: the nested request parameters are compacted,
	// but RequestParameters preserves the caller's exact bytes.
	stored := d
	stored.Request.Parameters = compact
	store.decisions[d.ID] = stored
	if err := g.Authorize(context.Background(), d.ID); err != nil {
		t.Fatalf("authorize after JSONB normalization: %v", err)
	}
}

func TestAuthorizeRequiresFreshUsage(t *testing.T) {
	p := testProgram(domain.StatusRunning)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "a1.example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed
	p.Constraints.PerInterfaceDailyLimits = []domain.DailyLimit{{Interface: "item_api", Amount: 10}}
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": p},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)
	aid := domain.AssetID("a1")
	req := testRequest(domain.ActionActiveHTTP)
	req.AssetID = &aid
	d, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: req, UsageToday: map[string]int64{"item_api": 1}, Now: testNow,
	})
	if err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("evaluate = %v %v", d.Decision, err)
	}
	if err := g.Authorize(context.Background(), d.ID); err == nil {
		t.Fatal("authorize without a fresh usage observation must fail closed")
	}
	if err := g.AuthorizeWith(context.Background(), d.ID, AuthorizeInput{
		UsageToday: map[string]int64{"item_api": 2},
	}); err != nil {
		t.Fatalf("authorize with fresh usage: %v", err)
	}
}

func TestAuthorizeStaleEnvironmentFailsClosed(t *testing.T) {
	p := testProgram(domain.StatusRunning)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "a1.example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed
	p.Constraints.VPNRequired = domain.TriYes
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": p},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)
	aid := domain.AssetID("a1")
	req := testRequest(domain.ActionActiveHTTP)
	req.AssetID = &aid
	observed := testNow.Add(-4 * time.Minute)
	d, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: req, Now: testNow,
		Environment: &domain.ExecutionEnvironment{ObservedAt: observed, VPNConnected: domain.TriYes},
	})
	if err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("evaluate = %v %v", d.Decision, err)
	}

	later := testNow.Add(2 * time.Minute)
	g.SetClock(func() time.Time { return later })
	if err := g.Authorize(context.Background(), d.ID); err == nil {
		t.Fatal("a stored environment older than its max age must not authorize")
	}
	if err := g.AuthorizeWith(context.Background(), d.ID, AuthorizeInput{
		Environment: &domain.ExecutionEnvironment{ObservedAt: later, VPNConnected: domain.TriYes},
	}); err != nil {
		t.Fatalf("authorize with fresh environment: %v", err)
	}
}

func TestAuthorizeRejectsChangedScope(t *testing.T) {
	p := testProgram(domain.StatusRunning)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "a1.example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": p},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, nil)
	aid := domain.AssetID("a1")
	req := testRequest(domain.ActionActiveHTTP)
	req.AssetID = &aid
	d, err := g.Evaluate(context.Background(), EvaluateInput{Request: req, Now: testNow})
	if err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("evaluate = %v %v", d.Decision, err)
	}

	// The platform removes the asset between decide and execute. The stored
	// ALLOW must no longer authorize the action.
	updated := p
	updated.Scope.Include = nil
	store.programs["p1"] = updated
	if err := g.Authorize(context.Background(), d.ID); err == nil {
		t.Fatal("authorize must re-evaluate and reject a scope that changed after the decision")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
