package harness

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/capability"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

// TestEgressAndProxyShareDailyCapability proves that one broker enforces a
// single account/program/interface daily budget across both execution surfaces.
func TestEgressAndProxyShareDailyCapability(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)

	broker := newDurableTestBroker()
	egress := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}).
		AllowUnvalidatedGrantsForTests().WithBroker(broker)
	expires := time.Now().Add(time.Hour)
	grant := ExecutionGrant{
		AccountID:      "acc1",
		ProgramID:      "p1",
		AssetTarget:    "127.0.0.1",
		AllowedMethods: map[string]struct{}{http.MethodGet: {}},
		DailyLimit:     1,
		ExpiresAt:      expires,
		LeaseUntil:     time.Now().Add(time.Minute),
	}

	resp, err := egress.WithGrant(grant).Get(context.Background(), upstream.URL+"/one")
	if err != nil {
		t.Fatalf("egress request: %v", err)
	}
	_ = resp.Body.Close()

	audit := &recordingNetworkAudit{}
	proxy := NewProxy(ProxyConfig{
		Policy:        GrantPolicy{Grant: &grant, Validator: testGrantValidator{}},
		Audit:         audit,
		Broker:        broker,
		AllowLoopback: true,
	})
	if err := proxy.Validate(); err != nil {
		t.Fatalf("proxy config: %v", err)
	}
	proxyServer := httptest.NewServer(proxy.Handler())
	t.Cleanup(proxyServer.Close)
	client := testProxyClient(t, proxyServer)

	// The daily budget was consumed by Egress; the proxied request must be
	// rejected before it reaches the upstream.
	resp, err = client.Get(upstream.URL + "/two")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("proxy status = %d, want 429", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 (only the egress request)", calls.Load())
	}
	if len(audit.recorded()) != 1 {
		t.Fatalf("audit events = %d, want 1 allow decision", len(audit.recorded()))
	}
}

// TestEgressReleasesLeaseOnError proves a failed request does not leak the
// concurrency slot.
func TestEgressReleasesLeaseOnError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens here, so the request fails after dialing

	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}).AllowUnvalidatedGrantsForTests()
	maxConc := 1
	granted := e.WithGrant(ExecutionGrant{
		AccountID:      "acc1",
		ProgramID:      "p1",
		MaxConcurrency: &maxConc,
		AllowedMethods: map[string]struct{}{http.MethodGet: {}},
	})
	target := "http://" + addr + "/x"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := granted.Get(ctx, target); err == nil {
		t.Fatal("expected the request to fail")
	}

	// A leaked slot would block this request until its (much shorter) deadline.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	if _, err := granted.Get(ctx2, target); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrency lease leaked after a failed request: %v", err)
	}
}

// durableTestQuota makes the test broker satisfy the production durability
// requirement without a database. The counter is still in memory; only the
// proxy's configuration check cares.
type durableTestQuota struct{ inner capability.DailyQuota }

func (q durableTestQuota) Consume(ctx context.Context, key, businessDate string, limit int64) (bool, error) {
	return q.inner.Consume(ctx, key, businessDate, limit)
}

func (q durableTestQuota) Durable() bool { return true }

func newDurableTestBroker() *capability.MemoryBroker {
	return capability.NewMemoryBroker(
		capability.WithDailyQuota(durableTestQuota{inner: capability.NewMemoryDailyQuota()}),
	)
}

type staticProgramStore struct {
	program domain.Program
	err     error
}

func (s staticProgramStore) GetProgram(context.Context, domain.ProgramID) (domain.Program, error) {
	if s.err != nil {
		return domain.Program{}, s.err
	}
	return s.program, nil
}

func grantValidatorFixture(t *testing.T, now time.Time) (capability.ProgramValidator, domain.Program, *capability.MemoryRevocationRegistry) {
	t.Helper()
	prog := domain.Program{
		ID:       "p1",
		Platform: domain.PlatformButian,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, ObservedAt: now,
			ValidUntil: now.Add(time.Hour), Confidence: 1,
		},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted,
			ContentsKnown: true, ObservedAt: now, ValidUntil: now.Add(time.Hour),
		},
		PolicyObservedAt: now,
		PolicyValidUntil: now.Add(time.Hour),
	}
	rev := capability.NewMemoryRevocationRegistry()
	v := capability.ProgramValidator{
		Programs:    staticProgramStore{program: prog},
		Revocations: rev,
		Now:         func() time.Time { return now },
	}
	return v, prog, rev
}

func epochOf(t *testing.T, r capability.RevocationRegistry) uint64 {
	t.Helper()
	epoch, err := r.Epoch(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	return epoch
}

func validatorGrant(now time.Time, prog domain.Program, epoch uint64) ExecutionGrant {
	return ExecutionGrant{
		AccountID:          "acc1",
		ProgramID:          "p1",
		AssetTarget:        "api.example.com",
		AllowedMethods:     map[string]struct{}{http.MethodGet: {}},
		ExpiresAt:          now.Add(time.Hour),
		LeaseUntil:         now.Add(time.Minute),
		ScopeRevision:      prog.Scope.Revision(),
		PolicyRevision:     prog.PolicyRevision(),
		AuthorizationEpoch: epoch,
	}
}

func decideWithValidator(p GrantPolicy) (PolicyDecision, string) {
	return p.Decide(context.Background(), Request{
		Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443,
		Method: http.MethodGet, Path: "/x",
	})
}

func TestGrantPolicyValidatorDeniesLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	v, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))
	grant.LeaseUntil = now.Add(-time.Second)

	decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
	if decision != Deny {
		t.Fatalf("decision = %s (%s), want Deny", decision, reason)
	}
	if !strings.Contains(reason, capability.ErrLeaseExpired.Error()) {
		t.Fatalf("reason = %q, want lease-expired detail", reason)
	}
}

func TestGrantPolicyValidatorDeniesBumpedEpoch(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	v, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))
	if _, err := rev.Bump(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}

	decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
	if decision != Deny {
		t.Fatalf("decision = %s (%s), want Deny", decision, reason)
	}
	if !strings.Contains(reason, capability.ErrGrantRevoked.Error()) {
		t.Fatalf("reason = %q, want revocation detail", reason)
	}
}

func TestGrantPolicyValidatorDeniesPausedProgram(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	v, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))
	paused := prog
	paused.Status.Status = domain.StatusPaused
	v.Programs = staticProgramStore{program: paused}
	grant.ScopeRevision = paused.Scope.Revision()
	grant.PolicyRevision = paused.PolicyRevision()

	decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
	if decision != Deny {
		t.Fatalf("decision = %s (%s), want Deny", decision, reason)
	}
	if !strings.Contains(reason, capability.ErrProgramNotRunning.Error()) {
		t.Fatalf("reason = %q, want not-running detail", reason)
	}
}

func TestGrantPolicyValidatorDeniesRevisionMismatch(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	v, prog, rev := grantValidatorFixture(t, now)

	for _, tc := range []struct {
		name   string
		mutate func(*ExecutionGrant)
		want   string
	}{
		{"scope", func(g *ExecutionGrant) { g.ScopeRevision = "old-scope" }, capability.ErrScopeRevisionMismatch.Error()},
		{"policy", func(g *ExecutionGrant) { g.PolicyRevision = "old-policy" }, capability.ErrPolicyRevisionMismatch.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := validatorGrant(now, prog, epochOf(t, rev))
			tc.mutate(&grant)
			decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
			if decision != Deny {
				t.Fatalf("decision = %s (%s), want Deny", decision, reason)
			}
			if !strings.Contains(reason, tc.want) {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestGrantPolicyValidatorAllowsFreshMatchingGrant(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	v, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))

	decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
	if decision != Allow {
		t.Fatalf("decision = %s (%s), want Allow", decision, reason)
	}
}

// TestEgressGrantValidatorDeniesInvalidState proves Egress enforces the same
// authoritative re-check as the proxy: an expired lease, a bumped epoch, a
// paused program or a changed scope/policy revision is rejected before any I/O.
func TestEgressGrantValidatorDeniesInvalidState(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *capability.ProgramValidator, *ExecutionGrant, *capability.MemoryRevocationRegistry, domain.Program)
		want   error
	}{
		{
			name: "expired lease",
			mutate: func(_ *testing.T, _ *capability.ProgramValidator, g *ExecutionGrant, _ *capability.MemoryRevocationRegistry, _ domain.Program) {
				g.LeaseUntil = now.Add(-time.Second)
			},
			want: capability.ErrLeaseExpired,
		},
		{
			name: "bumped epoch",
			mutate: func(t *testing.T, _ *capability.ProgramValidator, _ *ExecutionGrant, rev *capability.MemoryRevocationRegistry, _ domain.Program) {
				if _, err := rev.Bump(context.Background(), "p1"); err != nil {
					t.Fatal(err)
				}
			},
			want: capability.ErrGrantRevoked,
		},
		{
			name: "paused program",
			mutate: func(_ *testing.T, v *capability.ProgramValidator, _ *ExecutionGrant, _ *capability.MemoryRevocationRegistry, prog domain.Program) {
				prog.Status.Status = domain.StatusPaused
				v.Programs = staticProgramStore{program: prog}
			},
			want: capability.ErrProgramNotRunning,
		},
		{
			name: "scope revision changed",
			mutate: func(_ *testing.T, _ *capability.ProgramValidator, g *ExecutionGrant, _ *capability.MemoryRevocationRegistry, _ domain.Program) {
				g.ScopeRevision = "stale-scope"
			},
			want: capability.ErrScopeRevisionMismatch,
		},
		{
			name: "policy revision changed",
			mutate: func(_ *testing.T, _ *capability.ProgramValidator, g *ExecutionGrant, _ *capability.MemoryRevocationRegistry, _ domain.Program) {
				g.PolicyRevision = "stale-policy"
			},
			want: capability.ErrPolicyRevisionMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, prog, rev := grantValidatorFixture(t, now)
			grant := validatorGrant(now, prog, epochOf(t, rev))
			tc.mutate(t, &v, &grant, rev, prog)

			e := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}}).WithValidator(v)
			req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.WithGrant(grant).Do(context.Background(), req); !errors.Is(err, tc.want) {
				t.Fatalf("Egress err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestEgressGrantRequiresValidator proves a grant-bound Egress cannot run
// without the authoritative re-check; the test-only escape hatch is explicit.
func TestEgressGrantRequiresValidator(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	_, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))

	e := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}})
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.WithGrant(grant).Do(context.Background(), req); !errors.Is(err, ErrGrantValidatorRequired) {
		t.Fatalf("err = %v, want ErrGrantValidatorRequired", err)
	}

	// The explicit test-only escape hatch is the sole exception.
	escaped := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}}).AllowUnvalidatedGrantsForTests()
	if err := escaped.WithGrant(grant).validateGrant(context.Background()); err != nil {
		t.Fatalf("escape hatch err = %v", err)
	}
}

// TestEgressGrantBoundRejectsRedirect proves one grant cannot become multiple
// requests: a redirect fails closed instead of re-authorizing/re-charging.
func TestEgressGrantBoundRejectsRedirect(t *testing.T) {
	var start, next atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		start.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		next.Add(1)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}).AllowUnvalidatedGrantsForTests()
	grant := ExecutionGrant{
		ProgramID:      "p1",
		AssetTarget:    "127.0.0.1",
		AllowedMethods: map[string]struct{}{http.MethodGet: {}},
	}
	resp, err := e.WithGrant(grant).Get(context.Background(), srv.URL+"/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrGrantRedirect) {
		t.Fatalf("redirect err = %v, want ErrGrantRedirect", err)
	}
	if start.Load() != 1 {
		t.Fatalf("start handler calls = %d, want 1", start.Load())
	}
	if next.Load() != 0 {
		t.Fatalf("redirect target calls = %d, want 0", next.Load())
	}
}
