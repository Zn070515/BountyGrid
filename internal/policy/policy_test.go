package policy

import (
	"context"
	"regexp"
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
		ObservedAt: testNow,
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
		ParametersHash: domain.ParametersHash(nil),
		ExpiresAt:      testNow.Add(time.Hour),
		SingleUse:      true,
	}
}

func TestDecisionIDAvoidsLongDigitRuns(t *testing.T) {
	id := decisionID("r1", testNow)
	if regexp.MustCompile(`[0-9]{16,}`).MatchString(id) {
		t.Fatalf("decision id has a long digit run: %s", id)
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

// Decide validates a single-use approval but does not consume it. The
// consumption gate lives in Gateway.Authorize so a decision that is never
// executed does not burn the approval.
func TestDecisionDoesNotConsumeApproval(t *testing.T) {
	e := NewEngine()
	dc := DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopePublic),
		Now:     testNow, ResourceID: "report1",
		HumanApproval: approval(domain.ActionReportSubmit, "report1"),
	}
	for i := 0; i < 2; i++ {
		d, err := e.Decide(context.Background(), dc)
		if err != nil {
			t.Fatal(err)
		}
		if d.Decision != domain.DecisionAllow {
			t.Fatalf("decision %d = %s, want ALLOW (approval is consumed at Authorize)", i, d.Decision)
		}
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

	outID := domain.AssetID("a3")
	r.AssetID = &outID
	out := &domain.Asset{ID: "a3", ProgramID: "p1", Value: "other.com", InScope: true}
	d, err = e.Decide(context.Background(), DecisionContext{Request: r, Account: testAccount(true), Program: p, Asset: out, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_OUT_OF_SCOPE" {
		t.Fatalf("out-of-scope decision = %s %v", d.Decision, d.Reasons)
	}

	aid := domain.AssetID("a1")
	r.AssetID = &aid
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

func executableProgram() domain.Program {
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Include = []domain.Asset{
		{ID: "a1", Value: "a1.example.com", InScope: true},
		{ID: "a2", Value: "a2.example.com", InScope: true},
	}
	p.Automation.ActiveHTTP = domain.CapAllowed
	return p
}

func assetRequest(id domain.AssetID) domain.ActionRequest {
	r := req(domain.ActionActiveHTTP)
	r.AssetID = &id
	return r
}

func scopedAsset(id domain.AssetID, value string) *domain.Asset {
	return &domain.Asset{ID: id, ProgramID: "p1", Value: value, InScope: true}
}

func TestPolicyRejectsContextMismatch(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	base := DecisionContext{Request: req(domain.ActionViewRules), Account: testAccount(true), Program: p, Now: testNow}

	acc := testAccount(true)
	acc.ID = "other-account"
	dc := base
	dc.Account = acc
	if d, err := e.Decide(context.Background(), dc); err != nil || d.Decision != domain.DecisionDeny || d.Reasons[0] != "CONTEXT_MISMATCH" {
		t.Fatalf("account mismatch = %s %v (err=%v), want DENY CONTEXT_MISMATCH", d.Decision, d.Reasons, err)
	}

	prog := p
	prog.ID = "other-program"
	dc = base
	dc.Program = prog
	if d, err := e.Decide(context.Background(), dc); err != nil || d.Decision != domain.DecisionDeny || d.Reasons[0] != "CONTEXT_MISMATCH" {
		t.Fatalf("program mismatch = %s %v (err=%v), want DENY CONTEXT_MISMATCH", d.Decision, d.Reasons, err)
	}

	prog = p
	prog.Platform = domain.PlatformVulbox
	dc = base
	dc.Program = prog
	if d, err := e.Decide(context.Background(), dc); err != nil || d.Decision != domain.DecisionDeny || d.Reasons[0] != "PLATFORM_MISMATCH" {
		t.Fatalf("platform mismatch = %s %v (err=%v), want DENY PLATFORM_MISMATCH", d.Decision, d.Reasons, err)
	}
}

func TestPolicyBindsAssetToRequest(t *testing.T) {
	e := NewEngine()
	p := executableProgram()

	// The request names a2 but the context supplies the in-scope a1. Without an
	// identity binding the scope check would authorize the wrong asset.
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a2"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_MISMATCH" {
		t.Fatalf("asset mismatch = %s %v, want DENY ASSET_MISMATCH", d.Decision, d.Reasons)
	}

	// An asset that is not bound to the requested program must not authorize.
	d, err = e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: &domain.Asset{ID: "a1", Value: "a1.example.com", InScope: true}, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ASSET_PROGRAM_MISMATCH" {
		t.Fatalf("unbound asset = %s %v, want DENY ASSET_PROGRAM_MISMATCH", d.Decision, d.Reasons)
	}
}

func TestConditionalAssetRequiresHuman(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Scope.Conditional = []domain.Asset{{ID: "a1", Value: "example.com", InScope: true}}
	p.Automation.ActiveHTTP = domain.CapAllowed
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "example.com"), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman || d.Reasons[0] != "ASSET_CONDITIONAL" {
		t.Fatalf("conditional = %s %v, want REQUIRE_HUMAN ASSET_CONDITIONAL", d.Decision, d.Reasons)
	}
}

func TestLowConfidenceStateBlocksExecution(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Status.Confidence = 0.5
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "LOW_CONFIDENCE_STATE" {
		t.Fatalf("low confidence = %s %v, want REQUIRE_MORE_EVIDENCE LOW_CONFIDENCE_STATE", d.Decision, d.Reasons)
	}

	// Viewing is not execution: low-confidence state must not hide the rules.
	d, err = e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionViewRules), Account: testAccount(true), Program: p, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("view rules with low confidence = %s, want ALLOW", d.Decision)
	}
}

func TestInvalidConstraintConfigFailsClosed(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Constraints.PerInterfaceDailyLimits = []domain.DailyLimit{{Interface: "item_api", Amount: 0}}
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "INVALID_DAILY_LIMIT" {
		t.Fatalf("invalid daily limit = %s %v, want DENY INVALID_DAILY_LIMIT", d.Decision, d.Reasons)
	}
}

func TestBudgetCurrencyMismatchFailsClosed(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Constraints.BudgetCap = &domain.Money{Amount: 100, Currency: "CNY"}
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		BudgetSpent: &domain.Money{Amount: 0, Currency: "USD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "BUDGET_CURRENCY_MISMATCH" {
		t.Fatalf("budget mismatch = %s %v, want DENY BUDGET_CURRENCY_MISMATCH", d.Decision, d.Reasons)
	}
}

func TestConditionalAutomationCannotLoosenHardDeny(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Automation.DoS = domain.CapConditional
	r := req(domain.ActionDoS)
	aid := domain.AssetID("a1")
	r.AssetID = &aid
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: r, Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		HumanApproval: approval(domain.ActionDoS, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ACTION_DENIED" {
		t.Fatalf("conditional DoS = %s %v, want DENY ACTION_DENIED", d.Decision, d.Reasons)
	}
}

func TestDecisionExpiryCappedByStateAndApproval(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Status.ValidUntil = testNow.Add(30 * time.Second)
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow || d.ExpiresAt == nil || !d.ExpiresAt.Equal(p.Status.ValidUntil) {
		t.Fatalf("expiry = %v, want state valid_until %v", d.ExpiresAt, p.Status.ValidUntil)
	}

	p2 := testProgram(domain.StatusRunning, domain.ScopePublic)
	appr := approval(domain.ActionReportSubmit, "report1")
	appr.ExpiresAt = testNow.Add(2 * time.Minute)
	d, err = e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true), Program: p2,
		Now: testNow, ResourceID: "report1", HumanApproval: appr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow || d.ExpiresAt == nil || !d.ExpiresAt.Equal(appr.ExpiresAt) {
		t.Fatalf("expiry = %v, want approval expiry %v", d.ExpiresAt, appr.ExpiresAt)
	}
}

func TestViewRulesWithStaleStateGetsLiveExpiry(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusPaused, domain.ScopePublic)
	p.Status.ValidUntil = testNow.Add(-time.Hour)
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionViewRules), Account: testAccount(true), Program: p, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow || d.ExpiresAt == nil || !d.ExpiresAt.After(testNow) {
		t.Fatalf("view rules with stale state = %s %v, want ALLOW with future expiry", d.Decision, d.ExpiresAt)
	}
}

func TestProgramSignupRequiresAccountCapabilityAndRunningProgram(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	appr := approval(domain.ActionProgramSignup, "")
	dc := DecisionContext{
		Request: req(domain.ActionProgramSignup), Account: testAccount(true),
		Program: p, Now: testNow, HumanApproval: appr,
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("signup = %s %v, want ALLOW", d.Decision, d.Reasons)
	}

	noJoin := testAccount(true)
	noJoin.Capability.CanJoinPrograms = false
	dc.Account = noJoin
	d, err = e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ACCOUNT_CANNOT_JOIN_PROGRAMS" {
		t.Fatalf("signup without capability = %s %v", d.Decision, d.Reasons)
	}

	dc.Account = testAccount(true)
	dc.Program = testProgram(domain.StatusEnded, domain.ScopePublic)
	d, err = e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "PROGRAM_NOT_RUNNING" {
		t.Fatalf("signup on ended program = %s %v", d.Decision, d.Reasons)
	}
}

func TestReportSubmitIgnoresExecutionUsage(t *testing.T) {
	e := NewEngine()
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Constraints.PerInterfaceDailyLimits = []domain.DailyLimit{{Interface: "item_api", Amount: 1}}
	p.Constraints.BudgetCap = &domain.Money{Amount: 1, Currency: "CNY"}
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: testAccount(true),
		Program: p, Now: testNow, ResourceID: "report1",
		HumanApproval: approval(domain.ActionReportSubmit, "report1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("report submit = %s %v, want ALLOW without execution usage", d.Decision, d.Reasons)
	}
}

func TestPayoutRequiresAccountPayoutCapability(t *testing.T) {
	e := NewEngine()
	dc := DecisionContext{
		Request: req(domain.ActionPayout), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopePublic),
		Now:     testNow, HumanApproval: approval(domain.ActionPayout, ""),
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny || d.Reasons[0] != "ACCOUNT_CANNOT_PAYOUT" {
		t.Fatalf("payout without capability = %s %v, want DENY ACCOUNT_CANNOT_PAYOUT", d.Decision, d.Reasons)
	}
}

func TestRuntimeConstraintsFailClosed(t *testing.T) {
	e := NewEngine()
	base := func() DecisionContext {
		p := executableProgram()
		return DecisionContext{
			Request: assetRequest("a1"), Account: testAccount(true), Program: p,
			Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		}
	}

	// VPN required: no environment -> more evidence; explicit disconnect -> DENY.
	dc := base()
	vpn := domain.TriYes
	dc.Program.Constraints.VPNRequired = vpn
	dc.Asset = scopedAsset("a1", "a1.example.com")
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "VPN_STATE_UNKNOWN" {
		t.Fatalf("vpn unknown = %s %v", d.Decision, d.Reasons)
	}
	dc.Environment = &domain.ExecutionEnvironment{ObservedAt: testNow, VPNConnected: domain.TriNo}
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionDeny || d.Reasons[0] != "VPN_REQUIRED" {
		t.Fatalf("vpn absent = %s %v", d.Decision, d.Reasons)
	}
	dc.Environment = &domain.ExecutionEnvironment{ObservedAt: testNow, VPNConnected: domain.TriYes}
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionAllow {
		t.Fatalf("vpn present = %s %v, want ALLOW", d.Decision, d.Reasons)
	}

	// RPS: a missing measurement is not proof of compliance.
	dc = base()
	maxRPS := 2.0
	dc.Program.Constraints.MaxRPS = &maxRPS
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "RPS_UNKNOWN" {
		t.Fatalf("rps unknown = %s %v", d.Decision, d.Reasons)
	}
	rps := 3.0
	dc.Environment = &domain.ExecutionEnvironment{ObservedAt: testNow, CurrentRPS: &rps}
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionDeny || d.Reasons[0] != "RATE_LIMIT_EXCEEDED" {
		t.Fatalf("rps exceeded = %s %v", d.Decision, d.Reasons)
	}

	// Method allowlists are request-level, not runtime state.
	dc = base()
	dc.Program.Constraints.ForbiddenMethods = []string{"POST"}
	dc.Request.Method = "POST"
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionDeny || d.Reasons[0] != "METHOD_FORBIDDEN" {
		t.Fatalf("forbidden method = %s %v", d.Decision, d.Reasons)
	}
	dc.Request.Method = ""
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "METHOD_UNKNOWN" {
		t.Fatalf("method unknown = %s %v", d.Decision, d.Reasons)
	}

	// A program that pins an exact egress IP rejects any other source address.
	dc = base()
	dc.Program.Constraints.AllowedEgressIPs = []string{"203.0.113.7"}
	dc.Environment = &domain.ExecutionEnvironment{ObservedAt: testNow, EgressIP: "198.51.100.9"}
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionDeny || d.Reasons[0] != "EGRESS_IP_NOT_ALLOWED" {
		t.Fatalf("wrong egress ip = %s %v", d.Decision, d.Reasons)
	}

	// Time windows are evaluated against decision time.
	dc = base()
	dc.Program.Constraints.AllowedTimeWindows = []domain.TimeWindow{{
		Start: testNow.Add(-time.Hour), End: testNow.Add(-30 * time.Minute),
	}}
	if d, _ := e.Decide(context.Background(), dc); d.Decision != domain.DecisionDeny || d.Reasons[0] != "OUTSIDE_ALLOWED_TIME_WINDOW" {
		t.Fatalf("outside window = %s %v", d.Decision, d.Reasons)
	}
}

func TestMissingUsageAndBudgetFailClosed(t *testing.T) {
	e := NewEngine()
	base := func() DecisionContext {
		p := executableProgram()
		return DecisionContext{
			Request: assetRequest("a1"), Account: testAccount(true), Program: p,
			Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		}
	}

	dc := base()
	dc.Program.Constraints.PerInterfaceDailyLimits = []domain.DailyLimit{{Interface: "item_api", Amount: 10}}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "USAGE_UNKNOWN" {
		t.Fatalf("missing usage = %s %v, want REQUIRE_MORE_EVIDENCE USAGE_UNKNOWN", d.Decision, d.Reasons)
	}

	dc = base()
	dc.Program.Constraints.BudgetCap = &domain.Money{Amount: 100, Currency: "CNY"}
	d, err = e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "BUDGET_UNKNOWN" {
		t.Fatalf("missing budget = %s %v, want REQUIRE_MORE_EVIDENCE BUDGET_UNKNOWN", d.Decision, d.Reasons)
	}
}

func TestStaleExecutionEnvironmentFailsClosed(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	vpn := domain.TriYes
	p.Constraints.VPNRequired = vpn
	dc := DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		Environment: &domain.ExecutionEnvironment{
			ObservedAt:   testNow.Add(-2 * domain.ExecutionEnvironmentMaxAge),
			VPNConnected: domain.TriYes,
		},
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "VPN_STATE_UNKNOWN" {
		t.Fatalf("stale environment = %s %v, want REQUIRE_MORE_EVIDENCE VPN_STATE_UNKNOWN", d.Decision, d.Reasons)
	}
}

func TestEmptyTestAccountIsUnknownNotDenied(t *testing.T) {
	e := NewEngine()
	p := executableProgram()
	p.Constraints.TestAccountRequired = domain.TriYes
	dc := DecisionContext{
		Request: assetRequest("a1"), Account: testAccount(true), Program: p,
		Asset: scopedAsset("a1", "a1.example.com"), Now: testNow,
		Environment: &domain.ExecutionEnvironment{ObservedAt: testNow},
	}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence || d.Reasons[0] != "TEST_ACCOUNT_UNKNOWN" {
		t.Fatalf("empty test account = %s %v, want REQUIRE_MORE_EVIDENCE TEST_ACCOUNT_UNKNOWN", d.Decision, d.Reasons)
	}
}

func TestApprovalAcceptsCanonicalParametersHash(t *testing.T) {
	pretty := []byte("{\n  \"b\": 2,\n  \"a\": 1\n}")
	compact := []byte(`{"a":1,"b":2}`)
	appr := approval(domain.ActionReportSubmit, "report1")
	appr.ParametersHash = domain.CanonicalParametersHash(pretty)
	r := req(domain.ActionReportSubmit)
	r.Parameters = compact
	if err := validateApproval(appr, r, "report1", testNow); err != nil {
		t.Fatalf("canonical hash rejected after JSON reformatting: %v", err)
	}
}

func TestProgramGateSatisfiedByAccountState(t *testing.T) {
	account := testAccount(true)
	practical := true
	account.PracticalCertification = &practical
	pid := domain.ProgramID("p1")
	p := testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Gates = []domain.EligibilityGate{{
		Kind: domain.GatePracticalCertification, ProgramID: &pid,
		Required: true, Satisfied: domain.GateUnknown, Blocking: true,
	}}
	st, err := EvaluateEligibility(account, p, testNow, DefaultPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if st.Decision != domain.Eligible {
		t.Fatalf("practical certification true = %s, want ELIGIBLE", st.Decision)
	}

	// A joined program satisfies its signup gate without re-reading the page.
	account = testAccount(true)
	account.JoinedProgramIDs = []domain.ProgramID{"p1"}
	p = testProgram(domain.StatusRunning, domain.ScopePublic)
	p.Gates = []domain.EligibilityGate{{
		Kind: domain.GateProgramSignup, Required: true,
		Satisfied: domain.GateUnknown, Blocking: true,
	}}
	st, err = EvaluateEligibility(account, p, testNow, DefaultPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if st.Decision != domain.Eligible {
		t.Fatalf("joined program = %s, want ELIGIBLE", st.Decision)
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

func TestStaleAuthorizationInputsFailClosed(t *testing.T) {
	base := func() DecisionContext {
		r := req(domain.ActionActiveHTTP)
		asset := domain.AssetID("a1")
		r.AssetID = &asset
		return DecisionContext{
			Request: r, Account: testAccount(true),
			Program: testProgram(domain.StatusRunning, domain.ScopePublic),
			Now:     testNow,
		}
	}
	cases := []struct {
		name   string
		mutate func(*DecisionContext)
		reason string
	}{
		{"policy", func(dc *DecisionContext) { dc.Program.ObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour) }, "STALE_AUTOMATION_POLICY"},
		{"scope", func(dc *DecisionContext) { dc.Program.Scope.ObservedAt = testNow.Add(-domain.ScopeMaxAge - time.Hour) }, "STALE_SCOPE"},
		{"account", func(dc *DecisionContext) {
			dc.Account.ObservedAt = testNow.Add(-domain.AccountCapabilityMaxAge - time.Hour)
			dc.Account.Capability.EvaluatedAt = dc.Account.ObservedAt
		}, "STALE_ACCOUNT_CAPABILITY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dc := base()
			c.mutate(&dc)
			d, err := NewEngine().Decide(context.Background(), dc)
			if err != nil {
				t.Fatal(err)
			}
			if d.Decision != domain.DecisionRequireMoreEvidence || len(d.Reasons) == 0 || d.Reasons[0] != c.reason {
				t.Fatalf("decision = %s %v, want REQUIRE_MORE_EVIDENCE %s", d.Decision, d.Reasons, c.reason)
			}
		})
	}
}

func TestFreshAuthorizationInputsProceed(t *testing.T) {
	r := req(domain.ActionActiveHTTP)
	asset := domain.AssetID("a1")
	r.AssetID = &asset
	prog := testProgram(domain.StatusRunning, domain.ScopePublic)
	prog.Scope.Include = []domain.Asset{{ID: asset, ProgramID: "p1", Value: "a1.example.com", InScope: true}}
	prog.Automation.ActiveHTTP = domain.CapAllowed
	d, err := NewEngine().Decide(context.Background(), DecisionContext{
		Request: r, Account: testAccount(true), Program: prog,
		Asset: &domain.Asset{ID: asset, ProgramID: "p1", Value: "a1.example.com", InScope: true},
		Now:   testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("decision = %s %v", d.Decision, d.Reasons)
	}
	if d.AssetTarget != "a1.example.com" || d.ScopeRevision == "" {
		t.Fatalf("decision missing asset target/scope revision: %+v", d)
	}
}
