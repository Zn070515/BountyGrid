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

func newGateway(store *fakeStore, auditor Auditor, ledger policy.ApprovalLedger) *Gateway {
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

func TestEvaluateAssetMismatch(t *testing.T) {
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
	d, err := g.Evaluate(context.Background(), EvaluateInput{
		Request: req, Asset: &domain.Asset{ID: "a1", ProgramID: "p1", Value: "a1.example.com", InScope: true}, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_MISMATCH" {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
}

func TestEvaluateSingleUseApproval(t *testing.T) {
	store := &fakeStore{
		accounts:  map[domain.AccountID]domain.Account{"acc1": testAccount(true)},
		programs:  map[domain.ProgramID]domain.Program{"p1": testProgram(domain.StatusRunning)},
		decisions: map[domain.DecisionID]domain.ActionDecision{},
	}
	g := newGateway(store, &fakeAuditor{}, &fakeLedger{})
	in := EvaluateInput{
		Request: testRequest(domain.ActionReportSubmit), ResourceID: "report1", Now: testNow,
		HumanApproval: testApproval(domain.ActionReportSubmit, "report1"),
	}
	if d, err := g.Evaluate(context.Background(), in); err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("first = %v %v", d.Decision, err)
	}
	d, err := g.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("second = %s, want REQUIRE_HUMAN", d.Decision)
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

func ptrTime(t time.Time) *time.Time { return &t }
