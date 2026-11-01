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

const testEvidenceHash = "sha256:policy-content"

func testEvidence(id domain.EvidenceID, sourceType domain.SourceType) domain.Evidence {
	return domain.Evidence{
		ID: id, Platform: domain.PlatformButian, SourceType: sourceType,
		CapturedAt: testNow, ContentHash: testEvidenceHash,
	}
}

func validStore() fakeStore {
	return fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{
		"ev-1": testEvidence("ev-1", domain.SourcePolicy),
		"ev-2": testEvidence("ev-2", domain.SourceAnnouncement),
	}}
}

func testPermission(program domain.Program) PermissionFact {
	return PermissionFact{
		EvidenceID:          "ev-1",
		EvidenceContentHash: testEvidenceHash,
		Platform:            program.Platform,
		ProgramID:           program.ID,
		Action:              domain.ActionActiveHTTP,
		Hosts:               []string{"api.example.com"},
		Methods:             []string{"GET"},
		PathPrefixes:        []string{"/v1"},
		ObservedAt:          testNow.Add(-time.Hour),
		ValidUntil:          testNow.Add(time.Hour),
		PolicyRevision:      program.PolicyRevision(),
		Justification:       "platform policy explicitly permits this endpoint",
	}
}

func buildPolicy(t *testing.T, program domain.Program, permissions ...PermissionFact) *PlatformPolicy {
	t.Helper()
	if len(permissions) == 0 {
		permissions = []PermissionFact{testPermission(program)}
	}
	pp, err := BuildPlatformPolicy(context.Background(), program, permissions, validStore(), testNow)
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

func TestVerifyPlatformEvidenceAcceptsPolicyAndAnnouncement(t *testing.T) {
	p := testProgram()
	for _, c := range []struct {
		name     string
		evID     domain.EvidenceID
		observed time.Time
	}{
		{"policy", "ev-1", testNow.Add(-time.Hour)},
		{"announcement", "ev-2", testNow.Add(-time.Hour)},
		// A small clock skew is tolerated.
		{"small skew", "ev-1", testNow.Add(30 * time.Second)},
	} {
		t.Run(c.name, func(t *testing.T) {
			permission := testPermission(p)
			permission.EvidenceID = c.evID
			permission.ObservedAt = c.observed
			if err := VerifyPlatformEvidence(context.Background(), permission, p, validStore(), testNow); err != nil {
				t.Fatalf("valid permission rejected: %v", err)
			}
		})
	}
}

func TestVerifyPlatformEvidenceRejects(t *testing.T) {
	p := testProgram()
	cases := []struct {
		name   string
		mutate func(*PermissionFact)
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
				"ev-1": {ID: "ev-1", Platform: domain.PlatformVulbox, SourceType: domain.SourcePolicy,
					CapturedAt: testNow, ContentHash: testEvidenceHash},
			}},
			check: reasonIs(ReasonEvidencePlatformMismatch),
		},
		{
			name:   "permission platform mismatch",
			mutate: func(pf *PermissionFact) { pf.Platform = domain.PlatformVulbox },
			store:  validStore(),
			check:  reasonIs(ReasonEvidencePlatformMismatch),
		},
		{
			name:  "program listing evidence",
			store: fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{"ev-1": testEvidence("ev-1", domain.SourceProgram)}},
			check: reasonIs(ReasonEvidenceSourceTypeNotAuthorizing),
		},
		{
			name:   "wrong content hash",
			mutate: func(pf *PermissionFact) { pf.EvidenceContentHash = "sha256:forged" },
			store:  validStore(),
			check:  reasonIs(ReasonEvidenceContentHashMismatch),
		},
		{
			name:   "other program",
			mutate: func(pf *PermissionFact) { pf.ProgramID = "p2" },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionProgramMismatch),
		},
		{
			name:   "invalid action",
			mutate: func(pf *PermissionFact) { pf.Action = domain.Action("BOGUS") },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionActionInvalid),
		},
		{
			name:   "missing hosts",
			mutate: func(pf *PermissionFact) { pf.Hosts = nil },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionHostsMissing),
		},
		{
			name:   "missing methods",
			mutate: func(pf *PermissionFact) { pf.Methods = nil },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionMethodsMissing),
		},
		{
			name:   "invalid host",
			mutate: func(pf *PermissionFact) { pf.Hosts = []string{"*"} },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionHostInvalid),
		},
		{
			name:   "invalid path",
			mutate: func(pf *PermissionFact) { pf.PathPrefixes = []string{"/?query=1"} },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionPathInvalid),
		},
		{
			name:   "restricted permission",
			mutate: func(pf *PermissionFact) { pf.Restrictions = []string{"requires prior approval"} },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionRestrictionsPresent),
		},
		{
			name:   "wrong policy revision",
			mutate: func(pf *PermissionFact) { pf.PolicyRevision = "sha256:stale" },
			store:  validStore(),
			check: func(err error) bool {
				var re *RevisionError
				return errors.As(err, &re) && re.Reason == ReasonPolicyRevisionMismatch
			},
		},
		{
			name: "expired permission",
			mutate: func(pf *PermissionFact) {
				pf.ObservedAt = testNow.Add(-2 * time.Hour)
				pf.ValidUntil = testNow.Add(-time.Minute)
			},
			store: validStore(),
			check: reasonIs(ReasonPermissionExpired),
		},
		{
			name:   "future observed",
			mutate: func(pf *PermissionFact) { pf.ObservedAt = testNow.Add(5 * time.Minute) },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionFromFuture),
		},
		{
			name:   "missing observed",
			mutate: func(pf *PermissionFact) { pf.ObservedAt = time.Time{} },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionObservationInvalid),
		},
		{
			name:   "valid until before observed",
			mutate: func(pf *PermissionFact) { pf.ValidUntil = pf.ObservedAt.Add(-time.Minute) },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionObservationInvalid),
		},
		{
			name:   "missing revision",
			mutate: func(pf *PermissionFact) { pf.PolicyRevision = " " },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionRevisionMissing),
		},
		{
			name:   "missing justification",
			mutate: func(pf *PermissionFact) { pf.Justification = " " },
			store:  validStore(),
			check:  reasonIs(ReasonPermissionJustificationMissing),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			permission := testPermission(p)
			if c.mutate != nil {
				c.mutate(&permission)
			}
			store := c.store
			if store.evidence == nil && store.err == nil {
				store = validStore()
			}
			err := VerifyPlatformEvidence(context.Background(), permission, p, store, testNow)
			if err == nil || !c.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerifyPlatformEvidenceRejectsNonAuthorizingSourceTypes(t *testing.T) {
	p := testProgram()
	for _, sourceType := range []domain.SourceType{
		domain.SourceProgram, domain.SourceAccount, domain.SourcePayout, domain.SourceScreenshot,
	} {
		t.Run(string(sourceType), func(t *testing.T) {
			store := fakeStore{evidence: map[domain.EvidenceID]domain.Evidence{
				"ev-1": testEvidence("ev-1", sourceType),
			}}
			err := VerifyPlatformEvidence(context.Background(), testPermission(p), p, store, testNow)
			if !reasonIs(ReasonEvidenceSourceTypeNotAuthorizing)(err) {
				t.Fatalf("%s evidence accepted: %v", sourceType, err)
			}
		})
	}
}

func TestVerifyPlatformEvidenceFailsClosedOnMissingStoreOrStalePolicy(t *testing.T) {
	p := testProgram()
	permission := testPermission(p)
	if err := VerifyPlatformEvidence(context.Background(), permission, p, nil, testNow); !reasonIs(ReasonEvidenceStoreMissing)(err) {
		t.Fatalf("nil store err = %v", err)
	}

	stale := p
	stale.PolicyObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour)
	// The permission revision must match the stale program for this to reach
	// the freshness check.
	permission = testPermission(stale)
	err := VerifyPlatformEvidence(context.Background(), permission, stale, validStore(), testNow)
	var fe *FreshnessError
	if !errors.As(err, &fe) || fe.Reason != ReasonStaleAutomationPolicy {
		t.Fatalf("stale policy err = %v", err)
	}
}

func TestVerifyPlatformEvidenceSurfacesStoreErrors(t *testing.T) {
	p := testProgram()
	infra := errors.New("database unavailable")
	err := VerifyPlatformEvidence(context.Background(), testPermission(p), p, fakeStore{err: infra}, testNow)
	var rejected *RejectedError
	if !errors.Is(err, infra) || errors.As(err, &rejected) {
		t.Fatalf("infrastructure error was misclassified: %v", err)
	}
}

func TestBuildPlatformPolicyRejectsFakeEvidence(t *testing.T) {
	p := testProgram()
	_, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{testPermission(p)},
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

	// A stale policy snapshot removes authority even if the permission itself
	// is still inside its observation window.
	stale := p
	stale.PolicyObservedAt = testNow.Add(-domain.PolicyMaxAge - time.Hour)
	if res, err := pp.Evaluate(context.Background(), stale, testRequest()); err != nil || res.CanRelax {
		t.Fatalf("stale-policy result = %+v err=%v", res, err)
	}

	// The permission stops matching once its own validity window has passed.
	later := testRequest()
	later.Now = testNow.Add(2 * time.Hour)
	if res, err := pp.Evaluate(context.Background(), p, later); err != nil || res.CanRelax {
		t.Fatalf("expired permission result = %+v err=%v", res, err)
	}
}

func TestPlatformPolicyRequiresExplicitHostsAndMethods(t *testing.T) {
	p := testProgram()
	for _, c := range []struct {
		name   string
		mutate func(*PermissionFact)
	}{
		{"no hosts", func(pf *PermissionFact) { pf.Hosts = nil }},
		{"no methods", func(pf *PermissionFact) { pf.Methods = nil }},
		{"restricted", func(pf *PermissionFact) { pf.Restrictions = []string{"human review"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			permission := testPermission(p)
			c.mutate(&permission)
			if _, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{permission}, validStore(), testNow); err == nil {
				t.Fatal("permission without an explicit allow shape was accepted")
			}
		})
	}
	// A request that has no host/method cannot match even a valid permission.
	pp := buildPolicy(t, p)
	req := testRequest()
	req.Asset = ""
	if res, err := pp.Evaluate(context.Background(), p, req); err != nil || res.CanRelax {
		t.Fatalf("host-less request matched: %+v err=%v", res, err)
	}
}

func TestPlatformPolicyFingerprintCoversProvenance(t *testing.T) {
	p := testProgram()
	base := buildPolicy(t, p)
	permission := testPermission(p)
	permission.EvidenceID = "ev-2"
	other := buildPolicy(t, p, permission)
	if base.Fingerprint() == other.Fingerprint() {
		t.Fatal("evidence id did not affect the fingerprint")
	}

	permission = testPermission(p)
	permission.EvidenceContentHash = "sha256:other"
	if _, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{permission}, validStore(), testNow); err == nil {
		t.Fatal("a mismatched content hash was accepted")
	}

	permission = testPermission(p)
	permission.ValidUntil = permission.ValidUntil.Add(time.Minute)
	third := buildPolicy(t, p, permission)
	if base.Fingerprint() == third.Fingerprint() {
		t.Fatal("validity window did not affect the fingerprint")
	}

	permission = testPermission(p)
	permission.PolicyRevision = "sha256:other"
	if _, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{permission}, validStore(), testNow); err == nil {
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

func TestPermissionIDsDistinguishFactsFromTheSameEvidence(t *testing.T) {
	p := testProgram()
	read := testPermission(p)
	write := testPermission(p)
	write.Methods = []string{"POST"}
	if read.ID() == write.ID() {
		t.Fatalf("distinct permissions share audit id %q", read.ID())
	}
}

func TestPermissionRejectsEmptyPathPrefix(t *testing.T) {
	p := testProgram()
	permission := testPermission(p)
	permission.PathPrefixes = []string{" "}
	if _, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{permission}, validStore(), testNow); err == nil {
		t.Fatal("an empty path prefix was accepted (it would allow every path)")
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
	pp, err := BuildPlatformPolicy(context.Background(), p, []PermissionFact{testPermission(p)}, store, testNow)
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

	// A changed content hash means the permission no longer describes the
	// stored evidence.
	store.evidence["ev-1"] = testEvidence("ev-1", domain.SourcePolicy)
	changed := store.evidence["ev-1"]
	changed.ContentHash = "sha256:changed"
	store.evidence["ev-1"] = changed
	res, err = pp.Evaluate(context.Background(), p, testRequest())
	if err != nil || res.CanRelax {
		t.Fatalf("authority survived a content hash change = %+v err=%v", res, err)
	}

	// A source-type change (e.g. the evidence is reclassified as a program
	// listing) also removes authority.
	store.evidence["ev-1"] = testEvidence("ev-1", domain.SourceProgram)
	res, err = pp.Evaluate(context.Background(), p, testRequest())
	if err != nil || res.CanRelax {
		t.Fatalf("authority survived a source-type change = %+v err=%v", res, err)
	}
}
