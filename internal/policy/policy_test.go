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
	return domain.Program{
		ID:          "p1",
		Platform:    domain.PlatformButian,
		Name:        "test",
		Type:        domain.TypeBrandSRC,
		Status:      domain.ProgramState{Status: status, ObservedAt: testNow, ValidUntil: testNow.Add(time.Hour), Confidence: 1},
		Eligibility: eligibleState(),
		Scope:       domain.Scope{Visibility: visibility, ContentsKnown: visibility == domain.ScopePublic, ObservedAt: testNow},
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

func approval(action domain.Action) *domain.HumanApproval {
	return &domain.HumanApproval{
		ID:         "h1",
		ApprovedBy: "human",
		ApprovedAt: testNow,
		Action:     action,
		ProgramID:  "p1",
		ExpiresAt:  testNow.Add(time.Hour),
	}
}

func TestReportSubmitRequiresHumanApproval(t *testing.T) {
	e := NewEngine()
	dc := DecisionContext{Request: req(domain.ActionReportSubmit), Account: testAccount(true), Program: testProgram(domain.StatusRunning, domain.ScopePublic), Now: testNow}
	d, err := e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireHuman {
		t.Fatalf("decision = %s, want REQUIRE_HUMAN", d.Decision)
	}
	dc.HumanApproval = approval(domain.ActionReportSubmit)
	d, err = e.Decide(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow {
		t.Fatalf("approved decision = %s, want ALLOW", d.Decision)
	}
}

func TestButianComplianceBlocksSubmit(t *testing.T) {
	account := testAccount(false)
	program := testProgram(domain.StatusRunning, domain.ScopePublic)
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReportSubmit), Account: account, Program: program,
		Now: testNow, HumanApproval: approval(domain.ActionReportSubmit),
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

func TestXianzhiGatedScopeBlocksRead(t *testing.T) {
	e := NewEngine()
	d, err := e.Decide(context.Background(), DecisionContext{
		Request: req(domain.ActionReadScope), Account: testAccount(true),
		Program: testProgram(domain.StatusRunning, domain.ScopeGatedBySignup), Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionDeny {
		t.Fatalf("decision = %s, want DENY", d.Decision)
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
		Request: req(domain.ActionViewRules), Account: testAccount(true), Program: p, Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionRequireMoreEvidence {
		t.Fatalf("decision = %s, want REQUIRE_MORE_EVIDENCE", d.Decision)
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
