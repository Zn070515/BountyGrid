package policy

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func eligibleState() domain.EligibilityState {
	return domain.EligibilityState{
		Decision:      domain.Eligible,
		EvaluatedAt:   testNow,
		PolicyVersion: DefaultPolicyVersion,
	}
}

func testAccount(canSubmit bool) domain.Account {
	identity := true
	compliance := true
	return domain.Account{
		ID:                      "acc1",
		Platform:                domain.PlatformButian,
		ObservedAt:              testNow,
		IdentityVerified:        &identity,
		ComplianceExamCompleted: &compliance,
		Capability: domain.AccountCapability{
			CanViewPrograms: true,
			CanViewScope:    true,
			CanJoinPrograms: true,
			CanSubmit:       canSubmit,
			EvaluatedAt:     testNow,
		},
	}
}

func testProgram(status domain.ProgramStatus, visibility domain.ScopeVisibility) domain.Program {
	access := domain.ScopeAccessNotGranted
	if visibility == domain.ScopePublic {
		access = domain.ScopeAccessGranted
	}
	return domain.Program{
		ID:          "p1",
		Platform:    domain.PlatformButian,
		Name:        "test",
		Type:        domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: status, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: eligibleState(),
		Scope: domain.Scope{
			Visibility:    visibility,
			Access:        access,
			ContentsKnown: visibility == domain.ScopePublic,
			ObservedAt:    testNow,
		},
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
	}
}

func req(action domain.Action) domain.ActionRequest {
	return domain.ActionRequest{
		ID:          "r1",
		Actor:       domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
		AccountID:   "acc1",
		ProgramID:   "p1",
		Action:      action,
		RequestedAt: testNow,
	}
}

func approval(action domain.Action, resource string) *domain.HumanApproval {
	return &domain.HumanApproval{
		ID:             "h1",
		ApprovedBy:     "human",
		ApprovedAt:     testNow,
		RequestID:      "r1",
		Action:         action,
		ProgramID:      "p1",
		ResourceID:     resource,
		ParametersHash: parametersHash(nil),
		ExpiresAt:      testNow.Add(time.Hour),
		SingleUse:      true,
	}
}

func TestReportSubmitRequiresHumanApproval(t *testing.T) {
	e := NewEngine()
	dc := DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopePublic),
		Now:     testNow, ResourceID: "report1",
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("decision = %s, want REQUIRE_HUMAN", d.Decision)
	}
	dc.HumanApproval = approval(domain.ActionReportSubmit, "report1")
	d, err = e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("approved decision = %s, want ALLOW", d.Decision)
	}
}

func TestApprovalIsSingleUse(t *testing.T) {
	e := NewEngine()
	dc := DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopePublic),
		Now:     testNow, ResourceID: "report1",
		HumanApproval: approval(domain.ActionReportSubmit, "report1"),
	}
	if d, err := e.Decide(context.Background(), dc); err != nil || d.Decision != domain.DecisionAllow {
		t.Fatalf("first decision = %v, %v", d.Decision, err)
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("reused approval decision = %s, want REQUIRE_HUMAN", d.Decision)
	}
}

func TestButianComplianceBlocksSubmit(t *testing.T) {
	account := testAccount(false)
	program := testProgram(domain.StatusRunning, domain.ScopePublic)
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: account, Program: program,
		Now: testNow, ResourceID: "report1",
		HumanApproval: approval(domain.ActionReportSubmit, "report1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny {
		t.Fatalf("decision = %s, want DENY", d.Decision)
	}
	if len(d.Reasons) == 0 || d.Reasons[0] != "ACCOUNT_CANNOT_SUBMIT" {
		t.Fatalf("reasons = %v", d.Reasons)
	}
}

func TestXianzhiGatedScopeReadRequiresHuman(t *testing.T) {
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReadScope), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopeGatedBySignup), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("decision = %s, want REQUIRE_HUMAN", d.Decision)
	}
}

func TestXianzhiUnlockedScopeReadAllowed(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopeGatedBySignup)
	p.Scope.Access = domain.ScopeAccessGranted
	p.Scope.ContentsKnown = true
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReadScope), Account: testAccount(true), Program: p, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s, want ALLOW", d.Decision)
	}
}

func TestGatedScopeBlocksActiveHTTP(t *testing.T) {
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionActiveHTTP), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopeGatedBySignup), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny {
		t.Fatalf("decision = %s, want DENY", d.Decision)
	}
}

func TestUnknownScopeReadNeedsEvidence(t *testing.T) {
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReadScope), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopeUnknown), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence {
		t.Fatalf("decision = %s, want REQUIRE_MORE_EVIDENCE", d.Decision)
	}
}

func TestExpiredApprovalRejected(t *testing.T) {
	e := NewEngine()
	appr := approval(domain.ActionReportSubmit, "report1")
	appr.ExpiresAt = testNow.Add(-time.Minute)
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopePublic),
		Now:     testNow, ResourceID: "report1", HumanApproval: appr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("decision = %s, want REQUIRE_HUMAN", d.Decision)
	}
}

func TestProjectAutomationCanAllowScan(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "example.com", InScope: true}}
	p.Automation.AutomatedScanning = domain.CapAllowed
	aid := domain.AssetID("a1")
	r := req(domain.ActionAutomatedScan)
	r.AssetID = &aid
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: r, Account: testAccount(true), Program: p,
		Asset: &domain.Asset{ID: "a1", ProgramID: "p1", Value: "example.com", InScope: true},
		Now:   testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s %v, want ALLOW", d.Decision, d.Reasons)
	}
}

func TestDailyLimitBlocksAutomation(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "example.com", InScope: true}}
	p.Automation.AutomatedScanning = domain.CapAllowed
	p.Constraints.PerInterfaceDailyLimits = []domain.DailyLimit{{Interface: "item_api", Amount: 1000}}
	aid := domain.AssetID("a1")
	r := req(domain.ActionAutomatedScan)
	r.AssetID = &aid
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: r, Account: testAccount(true), Program: p,
		Asset:      &domain.Asset{ID: "a1", ProgramID: "p1", Value: "example.com", InScope: true},
		UsageToday: map[string]int64{"item_api": 1000},
		Now:        testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "DAILY_LIMIT_EXCEEDED:item_api" {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
}

func TestPausedProgramBlocksExecution(t *testing.T) {
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionActiveHTTP), Account: testAccount(true),
		Program: testProgram(domain.StatusPaused, domain.ScopePublic), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny {
		t.Fatalf("decision = %s, want DENY", d.Decision)
	}
}

func TestStaleStateRequiresEvidence(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Status.ValidUntil = testNow.Add(-time.Minute)
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: p, Now: testNow, ResourceID: "report1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence {
		t.Fatalf("decision = %s, want REQUIRE_MORE_EVIDENCE", d.Decision)
	}
}

func TestViewRulesAllowedDespiteIneligible(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusPaused, domain.ScopePublic)
	p.Eligibility.Decision = domain.Ineligible
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionViewRules), Account: testAccount(false), Program: p, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s, want ALLOW for view rules", d.Decision)
	}
}

func TestAssetRequiredAndOutOfScope(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed

	r := req(domain.ActionActiveHTTP)
	d, err := e.Decide(context.Background(), DecisionContext{Request: r, Account: testAccount(true), Program: p, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_REQUIRED" {
		t.Fatalf("missing asset decision = %s %v", d.Decision, d.Reasons)
	}

	aid := domain.AssetID("a1")
	r.AssetID = &aid
	out := &domain.Asset{ID: "a2", ProgramID: "p1", Value: "other.com", InScope: true}
	d, err = e.Decide(context.Background(), DecisionContext{Request: r, Account: testAccount(true), Program: p, Asset: out, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_OUT_OF_SCOPE" {
		t.Fatalf("out-of-scope decision = %s %v", d.Decision, d.Reasons)
	}

	in := &domain.Asset{ID: "a1", ProgramID: "p1", Value: "example.com", InScope: true}
	d, err = e.Decide(context.Background(), DecisionContext{Request: r, Account: testAccount(true), Program: p, Asset: in, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("in-scope decision = %s %v", d.Decision, d.Reasons)
	}
}

func TestAutomationForbiddenCannotBeOverridden(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Include = []domain.Asset{{ID: "a1", Value: "example.com", InScope: true}}
	p.Automation.FileUpload = domain.CapForbidden
	aid := domain.AssetID("a1")
	r := req(domain.ActionFileUpload)
	r.AssetID = &aid
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: r, Account: testAccount(true), Program: p,
		Asset: &domain.Asset{ID: "a1", ProgramID: "p1", Value: "example.com", InScope: true},
		Now:   testNow, HumanApproval: approval(domain.ActionFileUpload, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "AUTOMATION_FORBIDDEN" {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
}

func TestEvaluateEligibilityButianCompliance(t *testing.T) {
	account := testAccount(true)
	compliance := false
	account.ComplianceExamCompleted = &compliance
	program := testProgram(domain.StatusRunning, domain.ScopePublic)
	st, err := EvaluateEligibility(account, program, testNow, DefaultPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if st.Decision != domain.HumanActionNeeded {
		t.Fatalf("decision = %s, want HUMAN_ACTION_REQUIRED", st.Decision)
	}
}

func TestPracticalCertificationIsProgramScoped(t *testing.T) {
	account := testAccount(true)
	p1 := testProgram(domain.StatusRunning, domain.ScopePublic)
	pid := domain.ProgramID("p1")
	p1.Gates = []domain.EligibilityGate{{
		Kind: domain.GatePracticalCertification, ProgramID: &pid,
		Required: true, Satisfied: domain.GateUnsatisfied, Blocking: true,
	}}
	p2 := testProgram(domain.StatusRunning, domain.ScopePublic)
	p2.ID = "p2"

	st1, err := EvaluateEligibility(account, p1, testNow, DefaultPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if st1.Decision != domain.HumanActionNeeded {
		t.Fatalf("p1 decision = %s, want HUMAN_ACTION_REQUIRED", st1.Decision)
	}
	st2, err := EvaluateEligibility(account, p2, testNow, DefaultPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Decision != domain.Eligible {
		t.Fatalf("p2 decision = %s, want ELIGIBLE", st2.Decision)
	}
}
