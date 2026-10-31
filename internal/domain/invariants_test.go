package domain

import (
	"testing"
	"time"
)

func now() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }

func validEligibility() EligibilityState {
	return EligibilityState{
		Decision:      Eligible,
		EvaluatedAt:   now(),
		PolicyVersion: "test-1",
	}
}

func validProgram() Program {
	return Program{
		ID:       "p1",
		Platform: PlatformButian,
		Name:     "test program",
		Type:     TypeBrandSRC,
		Status: ProgramState{
			Status:     StatusRunning,
			ObservedAt: now(),
			ValidUntil: now().Add(time.Hour),
			Confidence: 1,
		},
		Eligibility: validEligibility(),
		Scope: Scope{
			Visibility:    ScopePublic,
			Access:        ScopeAccessGranted,
			ContentsKnown: true,
			ObservedAt:    now(),
		},
		Reward: RewardPolicy{TaxBasis: TaxUnknown},
		Automation: AutomationPolicy{
			AIAnalysis: CapUnknown, AutonomousAgent: CapUnknown, ActiveHTTP: CapUnknown,
			BrowserAutomation: CapUnknown,
			AutomatedScanning: CapUnknown, DirectoryEnumeration: CapUnknown, SubdomainDiscovery: CapUnknown,
			HighConcurrency: CapUnknown, BruteForce: CapUnknown, CredentialStuffing: CapUnknown,
			FileUpload: CapUnknown, StateMutation: CapUnknown, RCEVerification: CapUnknown,
			SensitiveDataRead: CapUnknown, InternalNetwork: CapUnknown, LateralMovement: CapUnknown,
			DoS: CapUnknown,
		},
	}
}

func TestScopeGatedMustNotContainAssets(t *testing.T) {
	s := Scope{
		Visibility:    ScopeGatedBySignup,
		Access:        ScopeAccessNotGranted,
		ContentsKnown: false,
		ObservedAt:    now(),
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("empty gated scope should be valid: %v", err)
	}
	s.Include = []Asset{{ID: "a1", Value: "example.com"}}
	if err := s.Validate(); err == nil {
		t.Fatal("gated scope with assets should be invalid")
	}
}

func TestEligibilityDecisionConsistency(t *testing.T) {
	gates := []EligibilityGate{
		{Kind: GateIdentityVerified, Required: true, Satisfied: GateSatisfied, Blocking: true},
		{Kind: GateComplianceExam, Required: true, Satisfied: GateUnsatisfied, Blocking: true},
		{Kind: GatePracticalCertification, Required: true, Satisfied: GateUnsatisfied, Blocking: true},
		{Kind: GateProgramSignup, Required: true, Satisfied: GateUnsatisfied, Blocking: true},
	}
	state := EligibilityState{
		Decision:      HumanActionNeeded,
		Gates:         gates,
		EvaluatedAt:   now(),
		PolicyVersion: "test-1",
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("human-action state should be valid: %v", err)
	}

	state.Decision = Eligible
	if err := state.Validate(); err == nil {
		t.Fatal("ELIGIBLE with blocking gates should be invalid")
	}

	state.Decision = Ineligible
	if err := state.Validate(); err == nil {
		t.Fatal("INELIGIBLE with human-resolvable blocking gates should be invalid")
	}
}

func TestAccountCanSubmitRequiresCompliance(t *testing.T) {
	compliance := false
	a := Account{
		ID:                      "acc1",
		Platform:                PlatformButian,
		ObservedAt:              now(),
		ComplianceExamCompleted: &compliance,
		Capability:              AccountCapability{CanSubmit: true},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("CanSubmit with incomplete compliance exam should be invalid")
	}
	a.Capability.CanSubmit = false
	if err := a.Validate(); err != nil {
		t.Fatalf("CanSubmit=false should be valid: %v", err)
	}
}

func TestProgramValidation(t *testing.T) {
	p := validProgram()
	if err := p.Validate(); err != nil {
		t.Fatalf("valid program rejected: %v", err)
	}
	p.Scope.Visibility = ScopeGatedByNDA
	p.Scope.Access = ScopeAccessNotGranted
	p.Scope.Include = []Asset{{ID: "a1", Value: "example.com"}}
	if err := p.Validate(); err == nil {
		t.Fatal("gated program scope with assets should be invalid")
	}
}

func TestActionDecisionHumanOnlyNeedsApproval(t *testing.T) {
	d := ActionDecision{
		ID:            "d1",
		RequestID:     "r1",
		ProgramID:     "p1",
		Action:        ActionReportSubmit,
		Decision:      DecisionAllow,
		PolicyVersion: "test-1",
		DecidedAt:     now(),
	}
	if err := d.Validate(); err == nil {
		t.Fatal("ALLOW REPORT_SUBMIT without approval should be invalid")
	}
	d.HumanApproval = &HumanApproval{
		ID:         "h1",
		ApprovedBy: "human",
		ApprovedAt: now(),
		Action:     ActionReportSubmit,
		ProgramID:  "p1",
		ExpiresAt:  now().Add(time.Hour),
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("approved REPORT_SUBMIT should be valid: %v", err)
	}
}

func TestSubmissionRequiresApproval(t *testing.T) {
	s := Submission{
		ID:          "s1",
		ReportID:    "r1",
		ProgramID:   "p1",
		SubmittedAt: now(),
		Receipt:     "receipt",
	}
	if err := s.Validate(); err == nil {
		t.Fatal("submission without approval should be invalid")
	}
	s.ApprovalID = "h1"
	if err := s.Validate(); err != nil {
		t.Fatalf("submission with approval should be valid: %v", err)
	}
}

func TestLedgerRejectsNegativeAmount(t *testing.T) {
	l := LedgerEntry{
		ID:         "l1",
		Platform:   PlatformVulbox,
		ProgramID:  "p1",
		Kind:       LedgerPaid,
		Amount:     Money{Amount: -1, Currency: "CNY"},
		OccurredAt: now(),
	}
	if err := l.Validate(); err == nil {
		t.Fatal("negative ledger amount should be invalid")
	}
	l.Amount.Amount = 100
	if err := l.Validate(); err != nil {
		t.Fatalf("positive ledger amount should be valid: %v", err)
	}
}

func TestFactRequiresEvidence(t *testing.T) {
	acc := AccountID("acc1")
	f := ObservedFact{
		ID:               "f1",
		Subject:          SubjectAccount,
		Platform:         PlatformButian,
		AccountID:        &acc,
		Predicate:        "level",
		ObservedAt:       now(),
		Confidence:       1,
		ExtractorVersion: "test-1",
	}
	if err := f.Validate(); err == nil {
		t.Fatal("fact without evidence should be invalid")
	}
	f.EvidenceRefs = []EvidenceID{"e1"}
	if err := f.Validate(); err != nil {
		t.Fatalf("fact with evidence should be valid: %v", err)
	}
}
