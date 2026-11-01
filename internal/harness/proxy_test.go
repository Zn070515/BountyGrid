package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
)

type staticNetworkPolicy struct {
	decision PolicyDecision
	reason   string

	mu       sync.Mutex
	requests []Request
}

func (p *staticNetworkPolicy) Decide(ctx context.Context, req Request) (PolicyDecision, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return p.decision, p.reason
}

func (p *staticNetworkPolicy) recorded() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

type recordingNetworkAudit struct {
	mu     sync.Mutex
	events []NetworkDecisionAudit
	err    error
}

func (a *recordingNetworkAudit) RecordNetworkDecision(ctx context.Context, e NetworkDecisionAudit) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, e)
	return nil
}

func (a *recordingNetworkAudit) recorded() []NetworkDecisionAudit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]NetworkDecisionAudit(nil), a.events...)
}

func newTestProxy(t *testing.T, cfg ProxyConfig) (*Proxy, *httptest.Server) {
	t.Helper()
	p := NewProxy(cfg)
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	return p, srv
}

func testProxyClient(t *testing.T, proxyServer *httptest.Server) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:       http.ProxyURL(u),
			DialContext: publicDialContext(true),
		},
		Timeout: 5 * time.Second,
	}
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func dialConnect(t *testing.T, proxyAddr, target string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	return dialConnectWithPayload(t, proxyAddr, target, "")
}

func dialConnectWithPayload(t *testing.T, proxyAddr, target, payload string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", target, target, payload); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn, br, resp
}

func countingUpstream(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProxyHTTPAllowForwardsAndAudits(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)

	policy := &staticNetworkPolicy{decision: Allow}
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{Policy: policy, Audit: audit, AllowLoopback: true})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("upstream response = %d %q", resp.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}

	events := audit.recorded()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Decision != Allow || ev.Protocol != ProtocolHTTP {
		t.Fatalf("audit = %+v", ev)
	}
	if ev.Host != "127.0.0.1" || ev.Port == 0 || ev.Method != http.MethodGet || ev.Path != "/hello" {
		t.Fatalf("audit target = %+v", ev)
	}
	if ev.Client == "" || ev.Timestamp.IsZero() {
		t.Fatalf("audit client/timestamp missing: %+v", ev)
	}

	requests := policy.recorded()
	if len(requests) != 1 {
		t.Fatalf("policy requests = %d, want 1", len(requests))
	}
	if requests[0].Protocol != ProtocolHTTP || requests[0].Method != http.MethodGet || requests[0].Path != "/hello" {
		t.Fatalf("policy request = %+v", requests[0])
	}
}

func TestProxyHTTPDenyNeverReachesUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)

	policy := &staticNetworkPolicy{decision: Deny, reason: "blocked by test policy"}
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{Policy: policy, Audit: audit, AllowLoopback: true})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/denied")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "blocked by test policy") {
		t.Fatalf("response body = %q, want the policy reason", body)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Deny || events[0].Reason != "blocked by test policy" {
		t.Fatalf("audit = %+v", events)
	}
}

func TestProxyAskFailsClosed(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)

	policy := &staticNetworkPolicy{decision: Ask, reason: "needs human approval"}
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{Policy: policy, Audit: audit, AllowLoopback: true})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/ask")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Ask {
		t.Fatalf("audit = %+v", events)
	}
}

func TestProxyNilPolicyFailsClosed(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	_, proxySrv := newTestProxy(t, ProxyConfig{AllowLoopback: true})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/no-policy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestProxyStripsHopByHopHeaders(t *testing.T) {
	var calls atomic.Int64
	var gotXHop string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotXHop = r.Header.Get("X-Hop")
		w.Header().Set("Connection", "X-Bad")
		w.Header().Set("X-Bad", "secret")
		w.Header().Set("X-Good", "kept")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Allow},
		Audit:         &recordingNetworkAudit{},
		AllowLoopback: true,
	})
	client := testProxyClient(t, proxySrv)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
	if gotXHop != "" {
		t.Fatalf("hop-by-hop request header reached upstream: %q", gotXHop)
	}
	if got := resp.Header.Get("X-Bad"); got != "" {
		t.Fatalf("hop-by-hop response header leaked: %q", got)
	}
	if got := resp.Header.Get("X-Good"); got != "kept" {
		t.Fatalf("end-to-end response header lost: %q", got)
	}
}

func TestProxyBodyLimit(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	newProxy := func() *http.Client {
		_, proxySrv := newTestProxy(t, ProxyConfig{
			Policy:        &staticNetworkPolicy{decision: Allow},
			Audit:         &recordingNetworkAudit{},
			AllowLoopback: true,
			MaxBodyBytes:  8,
		})
		return testProxyClient(t, proxySrv)
	}

	t.Run("request too large", func(t *testing.T) {
		client := newProxy()
		resp, err := client.Post(upstream.URL+"/post", "text/plain", strings.NewReader("123456789"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", resp.StatusCode)
		}
		if calls.Load() != 0 {
			t.Fatalf("upstream calls = %d, want 0", calls.Load())
		}
	})

	t.Run("request exactly at limit", func(t *testing.T) {
		client := newProxy()
		resp, err := client.Post(upstream.URL+"/post", "text/plain", strings.NewReader("12345678"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if calls.Load() != 1 {
			t.Fatalf("upstream calls = %d, want 1", calls.Load())
		}
	})

	t.Run("response too large", func(t *testing.T) {
		big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte("123456789"))
		}))
		defer big.Close()
		client := newProxy()
		resp, err := client.Get(big.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", resp.StatusCode)
		}
	})
}

func TestProxyAuditFailureBlocksForwarding(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	audit := &recordingNetworkAudit{err: errors.New("audit unavailable")}

	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Allow},
		Audit:         audit,
		AllowLoopback: true,
	})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/audit-down")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestProxyConnectAllowTunnels(t *testing.T) {
	echoAddr := startEchoServer(t)
	policy := &staticNetworkPolicy{decision: Allow}
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy: policy, Audit: audit, AllowLoopback: true, AllowOpaqueConnect: true,
	})

	conn, br, resp := dialConnect(t, proxySrv.Listener.Addr().String(), echoAddr)
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("CONNECT status = %d body=%q", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("tunnel echoed %q, want ping", buf)
	}

	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Allow || events[0].Protocol != ProtocolHTTPSConnect {
		t.Fatalf("audit = %+v", events)
	}
	if events[0].Host != "127.0.0.1" || events[0].Port == 0 || events[0].Method != "" || events[0].Path != "" {
		t.Fatalf("CONNECT audit = %+v", events[0])
	}
}

func TestProxyConnectForwardsPipelinedBytes(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:                 &staticNetworkPolicy{decision: Allow},
		AllowLoopback:          true,
		AllowUnauditedForTests: true,
		AllowOpaqueConnect:     true,
	})

	// Send the first tunnel bytes in the same write as the CONNECT request; the
	// server may buffer them past the headers, and the proxy must not drop them.
	conn, br, resp := dialConnectWithPayload(t, proxySrv.Listener.Addr().String(), echoAddr, "ping")
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("CONNECT status = %d body=%q", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("pipelined tunnel echoed %q, want ping", buf)
	}
}

func TestProxyConnectDeny(t *testing.T) {
	policy := &staticNetworkPolicy{decision: Deny, reason: "host is not approved"}
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy: policy, Audit: audit, AllowLoopback: true, AllowOpaqueConnect: true,
	})

	conn, _, resp := dialConnect(t, proxySrv.Listener.Addr().String(), "example.com:443")
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403", resp.StatusCode)
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Deny || events[0].Reason != "host is not approved" {
		t.Fatalf("audit = %+v", events)
	}
	if events[0].Protocol != ProtocolHTTPSConnect || events[0].Host != "example.com" || events[0].Port != 443 {
		t.Fatalf("CONNECT audit = %+v", events[0])
	}
}

func TestProxyConnectLoopbackRequiresExplicitAllowance(t *testing.T) {
	echoAddr := startEchoServer(t)
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:             &staticNetworkPolicy{decision: Allow},
		Audit:              audit,
		AllowLoopback:      false,
		AllowOpaqueConnect: true,
	})

	conn, _, resp := dialConnect(t, proxySrv.Listener.Addr().String(), echoAddr)
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want 502", resp.StatusCode)
	}
	// The policy decision was still recorded before the dial attempt failed.
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Allow {
		t.Fatalf("audit = %+v", events)
	}
}

// TestEgressUsesApprovedProxy proves that Egress routes through the explicit
// proxy even when the proxy itself is loopback and AllowLoopback is false. A
// direct connection to the loopback upstream would be rejected, so success can
// only come from the proxy path.
func TestEgressUsesApprovedProxy(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Allow},
		Audit:         audit,
		AllowLoopback: true,
	})

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	e := NewEgress(&Sandbox{
		AllowedHosts:  []string{"127.0.0.1"},
		ApprovedProxy: proxySrv.URL,
	})
	body, status, err := e.GetBytes(context.Background(), upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || string(body) != "hello" {
		t.Fatalf("response = %d %q", status, body)
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Allow {
		t.Fatalf("proxy audit = %+v", events)
	}
}

func TestEgressDenyThroughApprovedProxy(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Deny, reason: "egress denied"},
		Audit:         audit,
		AllowLoopback: true,
	})

	e := NewEgress(&Sandbox{
		AllowedHosts:  []string{"127.0.0.1"},
		ApprovedProxy: proxySrv.URL,
	})
	resp, err := e.Get(context.Background(), upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Deny {
		t.Fatalf("proxy audit = %+v", events)
	}
}

func TestEgressIgnoresEnvironmentProxy(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)

	envAudit := &recordingNetworkAudit{}
	_, envProxy := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Deny, reason: "environment proxy must not be used"},
		Audit:         envAudit,
		AllowLoopback: true,
	})
	t.Setenv("HTTP_PROXY", envProxy.URL)
	t.Setenv("HTTPS_PROXY", envProxy.URL)
	t.Setenv("NO_PROXY", "")

	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true})
	body, status, err := e.GetBytes(context.Background(), upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || string(body) != "hello" {
		t.Fatalf("response = %d %q", status, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
	if events := envAudit.recorded(); len(events) != 0 {
		t.Fatalf("environment proxy was consulted: %+v", events)
	}
}

func rulePolicyFixture(t *testing.T, decision, justification string) RulePolicy {
	t.Helper()
	rs, err := rules.Parse([]byte(`{"rules":[{
		"id":"net-rule",
		"actions":["ACTIVE_HTTP"],
		"hosts":["api.example.com"],
		"methods":["GET"],
		"decision":"` + decision + `",
		"justification":"` + justification + `",
		"match":[{"action":"ACTIVE_HTTP","asset":"https://api.example.com/v1","method":"GET"}]
	}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return RulePolicy{Rules: rs, Action: domain.ActionActiveHTTP, Program: "vulbox/444"}
}

func TestRulePolicyMapsNetworkDecisions(t *testing.T) {
	req := Request{Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: "GET", Path: "/v1"}
	cases := []struct {
		decision string
		want     PolicyDecision
	}{
		{"allow", Allow},
		{"forbidden", Deny},
		{"require_human", Ask},
	}
	for _, c := range cases {
		got, reason := rulePolicyFixture(t, c.decision, "test reason").Decide(context.Background(), req)
		if got != c.want || reason == "" {
			t.Fatalf("%s = %s %q, want %s", c.decision, got, reason, c.want)
		}
	}
	unmatched := Request{Host: "other.example.com", Method: "GET"}
	// As a guardrail, an unmatched request passes through so GrantPolicy remains
	// the authorization source.
	if got, _ := rulePolicyFixture(t, "allow", "test").Decide(context.Background(), unmatched); got != Allow {
		t.Fatalf("no-match guardrail = %s, want Allow", got)
	}
	strict := rulePolicyFixture(t, "allow", "test")
	strict.DenyUnmatched = true
	if got, _ := strict.Decide(context.Background(), unmatched); got != Deny {
		t.Fatalf("no-match with DenyUnmatched = %s, want Deny", got)
	}
	if got, _ := (RulePolicy{}).Decide(context.Background(), req); got != Deny {
		t.Fatalf("nil rules = %s, want Deny", got)
	}
}

func grantPolicyFixture(expiresAt time.Time) *ExecutionGrant {
	return &ExecutionGrant{
		AccountID: "acc1",
		ProgramID: "vulbox/444",
		// ProtocolHTTP maps to the http scheme; HTTPS traffic reaches a forward
		// proxy as CONNECT and is host-checked only.
		AssetTarget:    "http://api.example.com/v1",
		ExpiresAt:      expiresAt,
		AllowedMethods: map[string]struct{}{http.MethodGet: {}, http.MethodPost: {}},
	}
}

func TestGrantPolicyAllowsGrantedTargetAndMethod(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p := GrantPolicy{Grant: grantPolicyFixture(now.Add(time.Hour)), Now: func() time.Time { return now }}
	decision, reason := p.Decide(context.Background(), Request{
		Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: http.MethodGet, Path: "/v1/users",
	})
	if decision != Allow || reason == "" {
		t.Fatalf("granted request = %s %q, want Allow", decision, reason)
	}
}

func TestGrantPolicyDeniesWrongMethod(t *testing.T) {
	now := time.Now()
	p := GrantPolicy{Grant: grantPolicyFixture(now.Add(time.Hour)), Now: func() time.Time { return now }}
	got, _ := p.Decide(context.Background(), Request{
		Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: http.MethodDelete, Path: "/v1/users",
	})
	if got != Deny {
		t.Fatalf("wrong method = %s, want Deny", got)
	}
}

func TestGrantPolicyDeniesWrongHost(t *testing.T) {
	now := time.Now()
	p := GrantPolicy{Grant: grantPolicyFixture(now.Add(time.Hour)), Now: func() time.Time { return now }}
	got, _ := p.Decide(context.Background(), Request{
		Protocol: ProtocolHTTP, Host: "evil.example.com", Port: 443, Method: http.MethodGet, Path: "/v1",
	})
	if got != Deny {
		t.Fatalf("wrong host = %s, want Deny", got)
	}
}

func TestGrantPolicyDeniesExpiredAndNilGrant(t *testing.T) {
	now := time.Now()
	expired := GrantPolicy{Grant: grantPolicyFixture(now.Add(-time.Minute)), Now: func() time.Time { return now }}
	if got, _ := expired.Decide(context.Background(), Request{Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: http.MethodGet}); got != Deny {
		t.Fatalf("expired grant = %s, want Deny", got)
	}
	if got, _ := (GrantPolicy{}).Decide(context.Background(), Request{Host: "api.example.com"}); got != Deny {
		t.Fatalf("nil grant = %s, want Deny", got)
	}
	// A grant without an expiry is rejected rather than treated as unlimited.
	noExpiry := grantPolicyFixture(time.Time{})
	if got, _ := (GrantPolicy{Grant: noExpiry, Now: func() time.Time { return now }}).Decide(
		context.Background(), Request{Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: http.MethodGet}); got != Deny {
		t.Fatalf("zero-expiry grant = %s, want Deny", got)
	}
}

func TestGrantPolicyDelegatesToGuardrail(t *testing.T) {
	now := time.Now()
	req := Request{Protocol: ProtocolHTTP, Host: "api.example.com", Port: 443, Method: http.MethodGet, Path: "/v1"}
	forbidden := RulePolicy{Rules: rulePolicyFixture(t, "forbidden", "blocked by operator").Rules, Action: domain.ActionActiveHTTP}
	p := GrantPolicy{Grant: grantPolicyFixture(now.Add(time.Hour)), Next: forbidden, Now: func() time.Time { return now }}
	if got, _ := p.Decide(context.Background(), req); got != Deny {
		t.Fatalf("grant + forbidden rule = %s, want Deny", got)
	}
	requireHuman := RulePolicy{Rules: rulePolicyFixture(t, "require_human", "needs review").Rules, Action: domain.ActionActiveHTTP}
	p.Next = requireHuman
	if got, _ := p.Decide(context.Background(), req); got != Ask {
		t.Fatalf("grant + require_human rule = %s, want Ask", got)
	}
}

func TestProxyRequiresAudit(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Allow},
		AllowLoopback: true,
	})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/unaudited")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without an audit sink", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}

	// Serve must refuse to start a proxy that could forward unaudited Allow
	// traffic. A nil policy is rejected as well.
	misconfigured := NewProxy(ProxyConfig{Policy: &staticNetworkPolicy{decision: Allow}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := misconfigured.Serve(ln); err == nil {
		t.Fatal("Serve must fail without an audit sink")
	}
	if err := NewProxy(ProxyConfig{Audit: &recordingNetworkAudit{}}).Validate(); err == nil {
		t.Fatal("Validate must fail without a policy")
	}
}

func TestProxyAllowsUnauditedOnlyWhenExplicit(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:                 &staticNetworkPolicy{decision: Allow},
		AllowLoopback:          true,
		AllowUnauditedForTests: true,
	})
	if err := NewProxy(ProxyConfig{
		Policy:                 &staticNetworkPolicy{decision: Allow},
		AllowUnauditedForTests: true,
	}).Validate(); err != nil {
		t.Fatalf("explicit test escape hatch was rejected: %v", err)
	}
	client := testProxyClient(t, proxySrv)
	resp, err := client.Get(upstream.URL + "/explicit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("response = %d %q, want 200 hello", resp.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestProxyOpaqueConnectDisabledByDefault(t *testing.T) {
	echoAddr := startEchoServer(t)
	audit := &recordingNetworkAudit{}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy:        &staticNetworkPolicy{decision: Allow},
		Audit:         audit,
		AllowLoopback: true,
	})
	conn, _, resp := dialConnect(t, proxySrv.Listener.Addr().String(), echoAddr)
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403 (opaque CONNECT disabled)", resp.StatusCode)
	}
	events := audit.recorded()
	if len(events) != 1 || events[0].Decision != Deny ||
		!strings.Contains(events[0].Reason, "opaque CONNECT") {
		t.Fatalf("audit = %+v", events)
	}
}

func TestAuditSinkHashesPathAndRecords(t *testing.T) {
	store := audit.NewMemoryStore()
	sink := AuditSink{Auditor: store}
	err := sink.RecordNetworkDecision(context.Background(), NetworkDecisionAudit{
		Timestamp: time.Now().UTC(), Protocol: ProtocolHTTP,
		Host: "api.example.com", Port: 443, Method: "GET",
		Path:     "/v1/orders/1234567890123456",
		Decision: Allow, Reason: "allowed", Client: "127.0.0.1:1234", PolicyVersion: "sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), audit.Filter{Type: "network.policy_decision"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if strings.Contains(string(events[0].Payload), "1234567890123456") {
		t.Fatal("raw path leaked into audit payload")
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["path_hash"] == "" || payload["host"] != "api.example.com" || payload["decision"] != "allow" {
		t.Fatalf("payload = %+v", payload)
	}
	if err := (AuditSink{}).RecordNetworkDecision(context.Background(), NetworkDecisionAudit{}); err == nil {
		t.Fatal("nil auditor should fail closed")
	}
}
