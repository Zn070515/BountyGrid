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

	broker := capability.NewMemoryBroker()
	egress := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}).WithBroker(broker)
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
		Policy:        GrantPolicy{Grant: &grant},
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

	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true})
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
	grant := validatorGrant(now, prog, rev.Epoch("p1"))
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
	grant := validatorGrant(now, prog, rev.Epoch("p1"))
	rev.Bump("p1")

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
	grant := validatorGrant(now, prog, rev.Epoch("p1"))
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
			grant := validatorGrant(now, prog, rev.Epoch("p1"))
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
	grant := validatorGrant(now, prog, rev.Epoch("p1"))

	decision, reason := decideWithValidator(GrantPolicy{Grant: &grant, Validator: v})
	if decision != Allow {
		t.Fatalf("decision = %s (%s), want Allow", decision, reason)
	}
}
