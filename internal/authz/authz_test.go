package authz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func testProgram() domain.Program {
	return domain.Program{
		ID: "p1", Platform: domain.PlatformButian, Name: "p1", Type: domain.TypeBrandSRC,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, ObservedAt: testNow,
			ValidUntil: testNow.Add(time.Hour), Confidence: 1,
		},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true,
			Include: []domain.Asset{
				{ID: "a1", ProgramID: "p1", Value: "api.example.com", InScope: true},
				{ID: "a2", ProgramID: "p1", Value: "other.example.com", InScope: true},
			},
			Conditional: []domain.Asset{{ID: "a3", ProgramID: "p1", Value: "cond.example.com", InScope: true}},
			Exclude:     []domain.Asset{{ID: "a4", ProgramID: "p1", Value: "excluded.example.com", InScope: true}},
			ObservedAt:  testNow,
		},
		ObservedAt:       testNow,
		PolicyObservedAt: testNow,
	}
}

func testAccount() domain.Account {
	return domain.Account{
		ID: "acc1", Platform: domain.PlatformButian, ObservedAt: testNow,
		Capability: domain.AccountCapability{
			CanViewPrograms: true, CanViewScope: true, CanJoinPrograms: true, CanSubmit: true,
			EvaluatedAt: testNow,
		},
	}
}

type fakeStore struct {
	evidence map[domain.EvidenceID]domain.Evidence
	err      error
}

func (f fakeStore) GetEvidence(_ context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	if f.err != nil {
		return domain.Evidence{}, f.err
	}
	ev, ok := f.evidence[id]
	if !ok {
		return domain.Evidence{}, domain.ErrNotFound
	}
	return ev, nil
}

func validStore() fakeStore {
	return fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{
		"ev-1": {ID: "ev-1", Platform: domain.PlatformButian, CapturedAt: testNow},
		"ev-2": {ID: "ev-2", Platform: domain.PlatformButian, CapturedAt: testNow},
	}}
}

func testSpec(program domain.Program) rules.PlatformRuleSpec {
	return rules.PlatformRuleSpec{
		ID:             "platform-allow",
		Actions:        []domain.Action{domain.ActionActiveHTTP},
		Programs:       []domain.ProgramID{program.ID},
		Hosts:          []string{"api.example.com"},
		Methods:        []string{"GET"},
		EvidenceRefs:   []domain.EvidenceID{"ev-1"},
		ObservedAt:     testNow.Add(-time.Hour),
		ValidUntil:     testNow.Add(time.Hour),
		PolicyRevision: program.PolicyRevision(),
		Justification:  "platform evidence explicitly permits this endpoint",
	}
}

func buildPolicy(t *testing.T, program domain.Program, specs ...rules.PlatformRuleSpec) *PlatformPolicy {
	t.Helper()
	if len(specs) == 0 {
		specs = []rules.PlatformRuleSpec{testSpec(program)}
	}
	pp, err := BuildPlatformPolicy(context.Background(), program, specs, validStore(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	return pp
}

func testRequest() rules.Request {
	return rules.Request{
		Action: domain.ActionActiveHTTP, Program: "p1",
		Asset: "https://api.example.com/v1", Method: "GET", Now: testNow,
	}
}

func TestResolveInScopeAssetClassifiesAndFailsClosed(t *testing.T) {
	p := testProgram()
	cases := []struct {
		name    string
		id      domain.AssetID
		want    domain.ScopeMatch
		wantErr error
	}{
		{"included", "a1", domain.ScopeMatchIncluded, nil},
		{"conditional", "a3", domain.ScopeMatchConditional, nil},
		{"excluded", "a4", domain.ScopeMatchExcluded, nil},
		{"unknown id", "missing", domain.ScopeMatchUnknown, ErrAssetUnknown},
		{"empty id", "", domain.ScopeMatchUnknown, ErrAssetRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asset, match, err := ResolveInScopeAsset(p, c.id)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) || asset != nil || match != domain.ScopeMatchUnknown {
					t.Fatalf("asset=%v match=%v err=%v, want %v", asset, match, err, c.wantErr)
				}
				return
			}
			if err != nil || asset == nil || match != c.want {
				t.Fatalf("asset=%v match=%v err=%v, want %v", asset, match, err, c.want)
			}
			if asset.ProgramID != p.ID || asset.Platform != p.Platform {
				t.Fatalf("asset binding not filled: %+v", asset)
			}
		})
	}
}

func TestResolveInScopeAssetGatedScopeIsUnknown(t *testing.T) {
	p := testProgram()
	p.Scope.Access = domain.ScopeAccessNotGranted
	p.Scope.ContentsKnown = false
	asset, match, err := ResolveInScopeAsset(p, "a1")
	if err != nil || asset == nil || match != domain.ScopeMatchUnknown {
		t.Fatalf("gated scope asset=%v match=%v err=%v", asset, match, err)
	}
}

func TestResolveInScopeAssetEmptyIncludeIncludesNothing(t *testing.T) {
	p := testProgram()
	p.Scope.Include = nil
	if _, match, err := ResolveInScopeAsset(p, "a1"); !errors.Is(err, ErrAssetUnknown) || match != domain.ScopeMatchUnknown {
		t.Fatalf("empty include match=%v err=%v", match, err)
	}
}

func TestCheckAuthorizationFreshness(t *testing.T) {
	freshProgram := testProgram()
	freshAccount := testAccount()
	if err := CheckAuthorizationFreshness(freshProgram, freshAccount, domain.ActionActiveHTTP, testNow); err != nil {
		t.Fatalf("fresh inputs rejected: %v", err)
	}
	// Viewing is deliberately not gated by freshness.
	stale := freshProgram
	stale.Status.ObservedAt = time.Time{}
	stale.PolicyObservedAt = time.Time{}
	stale.Scope.ObservedAt = time.Time{}
	if err := CheckAuthorizationFreshness(stale, freshAccount, domain.ActionViewRules, testNow); err != nil {
		t.Fatalf("view action rejected for staleness: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*domain.Program, *domain.Account)
		reason string
	}{
		{"stale status", func(p *domain.Program, _ *domain.Account) { p.Status.ValidUntil = testNow.Add(-time.Second) }, ReasonStaleProgramState},
		{"low confidence", func(p *domain.Program, _ *domain.Account) { p.Status.Confidence = 0.5 }, ReasonLowConfidenceState},
		{"stale policy", func(p *domain.Program, _ *domain.Account) {
			p.PolicyObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour)
		}, ReasonStaleAutomationPolicy},
		{"stale capability", func(_ *domain.Program, a *domain.Account) {
			a.Capability.EvaluatedAt = testNow.Add(-domain.AccountCapabilityMaxAge - time.Hour)
		}, ReasonStaleAccountCapability},
		{"stale scope", func(p *domain.Program, _ *domain.Account) {
			p.Scope.ObservedAt = testNow.Add(-domain.ScopeMaxAge - time.Hour)
		}, ReasonStaleScope},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, a := testProgram(), testAccount()
			c.mutate(&p, &a)
			err := CheckAuthorizationFreshness(p, a, domain.ActionActiveHTTP, testNow)
			var fe *FreshnessError
			if !errors.As(err, &fe) || fe.Reason != c.reason {
				t.Fatalf("err = %v, want freshness %s", err, c.reason)
			}
		})
	}
}

func TestCheckRevisionBinding(t *testing.T) {
	p := testProgram()
	if err := CheckRevisionBinding(p, "", ""); err != nil {
		t.Fatalf("empty bindings should be skipped: %v", err)
	}
	if err := CheckRevisionBinding(p, p.Scope.Revision(), p.PolicyRevision()); err != nil {
		t.Fatalf("matching revisions rejected: %v", err)
	}

	var re *RevisionError
	err := CheckRevisionBinding(p, "sha256:wrong", "")
	if !errors.As(err, &re) || re.Reason != ReasonScopeRevisionMismatch {
		t.Fatalf("scope err = %v", err)
	}
	err = CheckRevisionBinding(p, "", "sha256:wrong")
	if !errors.As(err, &re) || re.Reason != ReasonPolicyRevisionMismatch {
		t.Fatalf("policy err = %v", err)
	}
}

func TestVerifyPlatformEvidenceAcceptsVerifiedRule(t *testing.T) {
	p := testProgram()
	rule, err := rules.NewPlatformRule(testSpec(p))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlatformEvidence(context.Background(), rule, p, validStore(), testNow); err != nil {
		t.Fatalf("valid platform rule rejected: %v", err)
	}
	// A small clock skew is tolerated.
	future := testSpec(p)
	future.ObservedAt = testNow.Add(30 * time.Second)
	rule, err = rules.NewPlatformRule(future)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlatformEvidence(context.Background(), rule, p, validStore(), testNow); err != nil {
		t.Fatalf("small skew rejected: %v", err)
	}
}

func TestVerifyPlatformEvidenceRejects(t *testing.T) {
	p := testProgram()
	cases := []struct {
		name   string
		mutate func(*rules.PlatformRuleSpec)
		store  fakeStore
		check  func(error) bool
	}{
		{
			name:  "missing evidence",
			store: fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{}},
			check: reasonIs(ReasonEvidenceMissing),
		},
		{
			name: "wrong platform",
			store: fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{
				"ev-1": {ID: "ev-1", Platform: domain.PlatformVulbox, CapturedAt: testNow},
			}},
			check: reasonIs(ReasonEvidencePlatformMismatch),
		},
		{
			name:   "other program",
			mutate: func(s *rules.PlatformRuleSpec) { s.Programs = []domain.ProgramID{"p2"} },
			store:  validStore(),
			check:  reasonIs(ReasonRuleProgramMismatch),
		},
		{
			name:   "wrong policy revision",
			mutate: func(s *rules.PlatformRuleSpec) { s.PolicyRevision = "sha256:stale" },
			store:  validStore(),
			check: func(err error) bool {
				var re *RevisionError
				return errors.As(err, &re) && re.Reason == ReasonPolicyRevisionMismatch
			},
		},
		{
			name: "expired rule",
			mutate: func(s *rules.PlatformRuleSpec) {
				s.ObservedAt = testNow.Add(-2 * time.Hour)
				s.ValidUntil = testNow.Add(-time.Minute)
			},
			store: validStore(),
			check: reasonIs(ReasonRuleExpired),
		},
		{
			name:   "future observed",
			mutate: func(s *rules.PlatformRuleSpec) { s.ObservedAt = testNow.Add(5 * time.Minute) },
			store:  validStore(),
			check:  reasonIs(ReasonRuleFromFuture),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := testSpec(p)
			if c.mutate != nil {
				c.mutate(&spec)
			}
			rule, err := rules.NewPlatformRule(spec)
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyPlatformEvidence(context.Background(), rule, p, c.store, testNow)
			if err == nil || !c.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerifyPlatformEvidenceFailsClosedOnMissingStoreOrStalePolicy(t *testing.T) {
	p := testProgram()
	rule, err := rules.NewPlatformRule(testSpec(p))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlatformEvidence(context.Background(), rule, p, nil, testNow); !reasonIs(ReasonEvidenceStoreMissing)(err) {
		t.Fatalf("nil store err = %v", err)
	}

	stale := p
	stale.PolicyObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour)
	// The rule revision must match the stale program for this to reach the
	// freshness check.
	spec := testSpec(stale)
	rule, err = rules.NewPlatformRule(spec)
	if err != nil {
		t.Fatal(err)
	}
	err = VerifyPlatformEvidence(context.Background(), rule, stale, validStore(), testNow)
	var fe *FreshnessError
	if !errors.As(err, &fe) || fe.Reason != ReasonStaleAutomationPolicy {
		t.Fatalf("stale policy err = %v", err)
	}
}

func TestVerifyPlatformEvidenceSurfacesStoreErrors(t *testing.T) {
	p := testProgram()
	rule, err := rules.NewPlatformRule(testSpec(p))
	if err != nil {
		t.Fatal(err)
	}
	infra := errors.New("database unavailable")
	err = VerifyPlatformEvidence(context.Background(), rule, p, fakeStore{err: infra}, testNow)
	var rejected *RejectedError
	if !errors.Is(err, infra) || errors.As(err, &rejected) {
		t.Fatalf("infrastructure error was misclassified: %v", err)
	}
}

func TestBuildPlatformPolicyRejectsFakeEvidence(t *testing.T) {
	p := testProgram()
	_, err := BuildPlatformPolicy(context.Background(), p, []rules.PlatformRuleSpec{testSpec(p)},
		fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{}}, testNow)
	if !reasonIs(ReasonEvidenceMissing)(err) {
		t.Fatalf("fake evidence accepted: %v", err)
	}
}

func TestPlatformPolicyRelaxesOnlyWhenVerifiedAndFresh(t *testing.T) {
	p := testProgram()
	pp := buildPolicy(t, p)
	if pp.Len() != 1 || pp.ProgramID() != p.ID || pp.Fingerprint() == "" {
		t.Fatalf("policy = len %d program %q fp %q", pp.Len(), pp.ProgramID(), pp.Fingerprint())
	}
	res, err := pp.Evaluate(context.Background(), p, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanRelax || len(res.Matched) != 1 || res.Matched[0].Source != rules.SourcePlatformEvidence {
		t.Fatalf("result = %+v", res)
	}

	// A policy built for a different program/platform never applies.
	other := p
	other.ID = "p2"
	if res, err := pp.Evaluate(context.Background(), other, testRequest()); err != nil || res.CanRelax {
		t.Fatalf("foreign program result = %+v err=%v", res, err)
	}
	mismatchedPlatform := p
	mismatchedPlatform.Platform = domain.PlatformVulbox
	if res, err := pp.Evaluate(context.Background(), mismatchedPlatform, testRequest()); err != nil || res.CanRelax {
		t.Fatalf("foreign platform result = %+v err=%v", res, err)
	}

	// A changed automation/constraints snapshot changes PolicyRevision.
	changed := p
	changed.Automation.ActiveHTTP = domain.CapAllowed
	if res, err := pp.Evaluate(context.Background(), changed, testRequest()); err != nil || res.CanRelax {
		t.Fatalf("revision-changed result = %+v err=%v", res, err)
	}

	// A stale policy snapshot removes authority even if the rule itself is
	// still inside its observation window.
	stale := p
	stale.PolicyObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour)
	if res, err := pp.Evaluate(context.Background(), stale, testRequest()); err != nil || res.CanRelax {
		t.Fatalf("stale-policy result = %+v err=%v", res, err)
	}

	// The rule stops matching once its own validity window has passed.
	later := testRequest()
	later.Now = testNow.Add(2 * time.Hour)
	if res, err := pp.Evaluate(context.Background(), p, later); err != nil || res.CanRelax {
		t.Fatalf("expired rule result = %+v err=%v", res, err)
	}
}

func TestPlatformPolicyFingerprintCoversProvenance(t *testing.T) {
	p := testProgram()
	base := buildPolicy(t, p)
	spec := testSpec(p)
	spec.EvidenceRefs = []domain.EvidenceID{"ev-2"}
	other := buildPolicy(t, p, spec)
	if base.Fingerprint() == other.Fingerprint() {
		t.Fatal("evidence provenance did not affect the fingerprint")
	}

	spec = testSpec(p)
	spec.ValidUntil = spec.ValidUntil.Add(time.Minute)
	third := buildPolicy(t, p, spec)
	if base.Fingerprint() == third.Fingerprint() {
		t.Fatal("validity window did not affect the fingerprint")
	}

	spec = testSpec(p)
	spec.PolicyRevision = "sha256:other"
	if _, err := BuildPlatformPolicy(context.Background(), p, []rules.PlatformRuleSpec{spec}, validStore(), testNow); err == nil {
		t.Fatal("a non-matching revision was accepted")
	}
}

func TestPlatformPolicyEvaluateRejectsInvalidRequest(t *testing.T) {
	p := testProgram()
	pp := buildPolicy(t, p)
	req := testRequest()
	req.Asset = "://bad"
	if _, err := pp.Evaluate(context.Background(), p, req); err == nil || !strings.Contains(err.Error(), "asset") {
		t.Fatalf("invalid request err = %v", err)
	}
}

func reasonIs(reason string) func(error) bool {
	return func(err error) bool {
		var re *RejectedError
		return errors.As(err, &re) && re.Reason == reason
	}
}

type mutableStore struct {
	evidence map[domain.EvidenceID]domain.Evidence
}

func (m *mutableStore) GetEvidence(_ context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	ev, ok := m.evidence[id]
	if !ok {
		return domain.Evidence{}, domain.ErrNotFound
	}
	return ev, nil
}

func TestPlatformPolicyRechecksEvidenceAtDecisionTime(t *testing.T) {
	p := testProgram()
	store := &mutableStore{evidence: validStore().evidence}
	pp, err := BuildPlatformPolicy(context.Background(), p, []rules.PlatformRuleSpec{testSpec(p)}, store, testNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pp.Evaluate(context.Background(), p, testRequest())
	if err != nil || !res.CanRelax {
		t.Fatalf("initial authority = %+v err=%v", res, err)
	}
	// Evidence deleted after the policy was compiled must remove authority; a
	// build-time-only check would keep relaxing forever.
	delete(store.evidence, "ev-1")
	res, err = pp.Evaluate(context.Background(), p, testRequest())
	if err != nil || res.CanRelax {
		t.Fatalf("authority survived evidence deletion = %+v err=%v", res, err)
	}
}
