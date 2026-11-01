package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/capability"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
)

// Protocol is the proxy protocol a policy decision is evaluated against.
type Protocol string

const (
	// ProtocolHTTP is an absolute-form HTTP request.
	ProtocolHTTP Protocol = "http"
	// ProtocolHTTPSConnect is a CONNECT tunnel request.
	ProtocolHTTPSConnect Protocol = "https_connect"
)

// PolicyDecision is the network-policy answer for one proxied request. There is
// no approval UI in this layer, so Ask fails closed exactly like Deny.
type PolicyDecision string

const (
	Allow PolicyDecision = "allow"
	Deny  PolicyDecision = "deny"
	Ask   PolicyDecision = "ask"
)

// Request is the policy input for one proxied request. Method and Path are set
// for HTTP requests and empty for CONNECT. Path excludes the query string so
// callback tokens and other query secrets never reach the audit sink.
type Request struct {
	Protocol Protocol
	Host     string
	Port     int
	Method   string
	Path     string
	Client   string
}

// NetworkPolicy decides whether one network request may leave the harness.
// Implementations must be safe for concurrent use.
type NetworkPolicy interface {
	Decide(ctx context.Context, req Request) (PolicyDecision, string)
}

// NetworkDecisionAudit is one append-only decision record. It carries no
// request/response body, headers, or query string.
type NetworkDecisionAudit struct {
	Timestamp     time.Time
	Protocol      Protocol
	Host          string
	Port          int
	Method        string
	Path          string
	Decision      PolicyDecision
	Reason        string
	Client        string
	PolicyVersion string
}

// NetworkAuditSink records network policy decisions. A nil sink is permitted for
// local tests; production wiring should provide one.
type NetworkAuditSink interface {
	RecordNetworkDecision(ctx context.Context, event NetworkDecisionAudit) error
}

// ProxyConfig configures a local forward proxy. The zero value is fail-closed:
// with no Policy every request is denied.
type ProxyConfig struct {
	Policy NetworkPolicy
	Audit  NetworkAuditSink
	// Transport forwards allowed HTTP requests. Nil builds a dedicated
	// transport that never consults environment proxy settings and applies the
	// same non-public address filter as Egress.
	Transport http.RoundTripper
	// MaxBodyBytes bounds request and response bodies. Zero uses
	// DefaultEgressMaxBytes (8 MiB).
	MaxBodyBytes int64
	// AllowLoopback exists for tests using httptest/loopback servers. It also
	// governs CONNECT target dialing and must never be enabled in production.
	AllowLoopback bool
	// AllowUnauditedForTests permits an Allow decision to be forwarded without a
	// NetworkAuditSink. It exists only so a test can exercise forwarding in
	// isolation; production must leave it false. Without an audit sink (and
	// without this escape hatch) the proxy refuses to serve and denies every
	// request.
	AllowUnauditedForTests bool
	// AllowUngrantedForTests permits a policy that is not a GrantPolicy. It
	// exists only for tests that exercise the proxy mechanics; production must
	// bind the proxy to an ExecutionGrant so an external worker cannot bypass
	// the Action Gateway's grant.
	AllowUngrantedForTests bool
	// AllowNonDurableCapabilityForTests permits a nil Broker or an in-memory
	// (non-durable) daily quota. It exists only for tests that exercise proxy
	// mechanics without a database; production must configure a durable broker
	// so a restart or a second process cannot reset the daily budget. It is
	// deliberately separate from AllowUngrantedForTests.
	AllowNonDurableCapabilityForTests bool
	// AllowOpaqueConnect permits host-level CONNECT tunnels. The default is
	// false because a tunnel hides method/path from the policy and audit layer;
	// phase one uses controlled Egress (absolute-form HTTP) instead. Enable it
	// only together with a reviewed network-namespace/firewall boundary.
	AllowOpaqueConnect bool
	// PolicyVersion is an optional fingerprint of the loaded policy data,
	// recorded with every audit event.
	PolicyVersion string
	// Broker enforces the same account/program/interface RPS, concurrency and
	// daily quota as the client-side Egress. When set, an allowed request
	// acquires a lease before forwarding/tunneling and releases it afterwards.
	// The policy must expose the limits (GrantPolicy does via
	// CapabilityRequest); otherwise the request fails closed with 429.
	Broker capability.Broker
}

// Proxy is a forward proxy that evaluates an explicit NetworkPolicy for every
// request, audits the decision, and only then forwards or tunnels. It never
// consults http.ProxyFromEnvironment.
type Proxy struct {
	policy          NetworkPolicy
	audit           NetworkAuditSink
	transport       http.RoundTripper
	maxBody         int64
	allowLoopback   bool
	allowUnaudited  bool
	allowUngranted  bool
	allowNonDurable bool
	allowOpaqueConn bool
	policyVersion   string
	broker          capability.Broker
	configErr       error

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
}

// NewProxy builds a proxy. A nil Transport is replaced with a dedicated
// transport; the proxy never chains to another environment-configured proxy.
func NewProxy(cfg ProxyConfig) *Proxy {
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultEgressMaxBytes
	}
	transport := cfg.Transport
	if transport == nil {
		transport = &http.Transport{
			// Proxy is nil: environment proxy variables must not reroute
			// policy-approved traffic.
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			DialContext:           publicDialContext(cfg.AllowLoopback),
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
		}
	}
	p := &Proxy{
		policy:          cfg.Policy,
		audit:           cfg.Audit,
		transport:       transport,
		maxBody:         maxBody,
		allowLoopback:   cfg.AllowLoopback,
		allowUnaudited:  cfg.AllowUnauditedForTests,
		allowUngranted:  cfg.AllowUngrantedForTests,
		allowNonDurable: cfg.AllowNonDurableCapabilityForTests,
		allowOpaqueConn: cfg.AllowOpaqueConnect,
		policyVersion:   cfg.PolicyVersion,
		broker:          cfg.Broker,
	}
	p.configErr = p.validate()
	return p
}

// validate returns the fail-closed configuration error. Both Serve and the
// request handler consult it; the handler-level check also covers callers that
// wire Handler directly into an http.Server.
func (p *Proxy) validate() error {
	if p.policy == nil {
		return errors.New("proxy: a NetworkPolicy is required")
	}
	if p.audit == nil && !p.allowUnaudited {
		return errors.New("proxy: a NetworkAuditSink is required (AllowUnauditedForTests is test-only)")
	}
	if !p.allowUngranted {
		switch gp := p.policy.(type) {
		case GrantPolicy:
			if gp.Grant == nil {
				return errors.New("proxy: GrantPolicy has no ExecutionGrant")
			}
			if gp.Validator == nil {
				return errors.New("proxy: GrantPolicy requires a capability.GrantValidator (revocation/revision re-check)")
			}
		case *GrantPolicy:
			if gp == nil || gp.Grant == nil {
				return errors.New("proxy: GrantPolicy has no ExecutionGrant")
			}
			if gp.Validator == nil {
				return errors.New("proxy: GrantPolicy requires a capability.GrantValidator (revocation/revision re-check)")
			}
		default:
			return errors.New("proxy: a GrantPolicy bound to an ExecutionGrant is required (AllowUngrantedForTests is test-only)")
		}
	}
	// A proxy that cannot account for the platform's daily budget must not
	// forward. Without a broker it cannot acquire RPS/concurrency/quota at all;
	// with a process-local quota a restart or a second process resets the
	// platform's daily count. Both are hard configuration errors in production.
	if !p.allowNonDurable {
		if p.broker == nil {
			return errors.New("proxy: a capability.Broker is required (AllowNonDurableCapabilityForTests is test-only)")
		}
		durable, ok := p.broker.(capability.DurableBroker)
		if !ok || !durable.Durable() {
			return errors.New("proxy: a durable capability.Broker is required: configure capability.PostgresDailyQuota (AllowNonDurableCapabilityForTests is test-only)")
		}
	}
	return nil
}

// Validate reports whether the proxy may serve. It is exported so an operator
// can reject a misconfigured proxy before binding a listener.
func (p *Proxy) Validate() error { return p.configErr }

// Handler returns the proxy as an http.Handler, suitable for httptest.
func (p *Proxy) Handler() http.Handler { return http.HandlerFunc(p.serveHTTP) }

// ListenAndServe listens on addr and serves until Close is called. A
// misconfigured proxy fails before binding a listener.
func (p *Proxy) ListenAndServe(addr string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return p.Serve(ln)
}

// Serve serves the proxy on an existing listener until Close is called. A
// misconfigured proxy returns an error instead of accepting traffic.
func (p *Proxy) Serve(ln net.Listener) error {
	if err := p.Validate(); err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	p.mu.Lock()
	p.server = srv
	p.listener = ln
	p.mu.Unlock()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the proxy. It is safe to call when the proxy never started.
func (p *Proxy) Close() error {
	p.mu.Lock()
	srv := p.server
	p.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// Addr reports the bound listener address, or "" before Serve starts.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// A proxy wired directly through Handler must fail closed too. Production
	// must never forward an Allow decision without a policy or an audit trail.
	if p.configErr != nil {
		http.Error(w, "proxy: misconfigured: "+p.configErr.Error(), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

func (p *Proxy) handleForward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "proxy: absolute-form request required", http.StatusBadRequest)
		return
	}
	host, port, err := requestTarget(r.URL)
	if err != nil {
		http.Error(w, "proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	req := Request{
		Protocol: ProtocolHTTP,
		Host:     host,
		Port:     port,
		Method:   r.Method,
		Path:     requestPath(r.URL),
		Client:   clientID(r),
	}
	decision, reason := p.decide(r.Context(), req)
	auditErr := p.record(r.Context(), req, decision, reason)
	if decision != Allow {
		http.Error(w, denialMessage(decision, reason), http.StatusForbidden)
		return
	}
	if auditErr != nil {
		// Allowed traffic must be auditable; an unwritable audit trail fails
		// closed instead of forwarding an unrecorded request.
		http.Error(w, "proxy: network policy audit failed", http.StatusBadGateway)
		return
	}

	// The same shared broker as Egress enforces RPS, concurrency and the daily
	// quota. Acquire only after the request is allowed and audited, and hold the
	// lease until the response has been written.
	lease, err := p.acquireCapability(r.Context(), "http")
	if err != nil {
		http.Error(w, "proxy: rate limit: "+err.Error(), http.StatusTooManyRequests)
		return
	}
	if lease != nil {
		defer lease.Release()
	}

	body, err := p.readRequestBody(r)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			http.Error(w, "proxy: request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "proxy: read request body", http.StatusBadRequest)
		return
	}

	outReq := p.outboundRequest(r, body)
	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "proxy: upstream request failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := readLimited(resp.Body, p.maxBody)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) || resp.ContentLength > p.maxBody {
			http.Error(w, "proxy: response body too large", http.StatusBadGateway)
			return
		}
		http.Error(w, "proxy: read upstream response", http.StatusBadGateway)
		return
	}

	copyHeader(w.Header(), resp.Header)
	stripHopByHop(w.Header())
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	authority := r.Host
	if authority == "" && r.URL != nil {
		authority = r.URL.Host
	}
	host, portStr, err := splitAuthority(authority)
	if err != nil {
		http.Error(w, "proxy: CONNECT requires host:port", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "proxy: invalid CONNECT port", http.StatusBadRequest)
		return
	}
	req := Request{
		Protocol: ProtocolHTTPSConnect,
		Host:     host,
		Port:     port,
		Client:   clientID(r),
	}
	// CONNECT is host/port level only: a tunnel hides method and path from the
	// policy and audit layer. Opaque tunnels are opt-in so phase one traffic
	// goes through the controlled HTTP Egress instead.
	if !p.allowOpaqueConn {
		const reason = "opaque CONNECT is disabled; use controlled http(s) egress"
		// Denials are still recorded when an audit sink exists; the request never
		// reaches the target either way.
		_ = p.record(r.Context(), req, Deny, reason)
		http.Error(w, "proxy: "+reason, http.StatusForbidden)
		return
	}
	decision, reason := p.decide(r.Context(), req)
	auditErr := p.record(r.Context(), req, decision, reason)
	if decision != Allow {
		http.Error(w, denialMessage(decision, reason), http.StatusForbidden)
		return
	}
	if auditErr != nil {
		http.Error(w, "proxy: network policy audit failed", http.StatusBadGateway)
		return
	}

	// CONNECT holds a concurrency slot for the lifetime of the tunnel, so the
	// shared broker reflects the real in-flight connection count.
	lease, err := p.acquireCapability(r.Context(), "http")
	if err != nil {
		http.Error(w, "proxy: rate limit: "+err.Error(), http.StatusTooManyRequests)
		return
	}
	if lease != nil {
		defer lease.Release()
	}

	// CONNECT target dialing is filtered here; the policy may allow the host by
	// name but a loopback/private/link-local resolution still has to be an
	// explicit test-only allowance.
	dial := publicDialContext(p.allowLoopback)
	upstream, err := dial(r.Context(), "tcp", net.JoinHostPort(host, portStr))
	if err != nil {
		http.Error(w, "proxy: upstream connection rejected", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy: CONNECT is not supported by this server", http.StatusNotImplemented)
		return
	}
	clientConn, rw, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		_ = upstream.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = clientConn.Close()
		_ = upstream.Close()
		return
	}
	// The server may have read past the CONNECT headers into its bufio reader
	// (a client is allowed to pipeline the first tunnel bytes). Read through
	// that buffer first so those bytes are not lost.
	tunnel(bufferedConn{Conn: clientConn, reader: rw.Reader}, upstream)
}

// bufferedConn preserves the hijacked connection's buffered reader before
// falling through to the socket.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (p *Proxy) decide(ctx context.Context, req Request) (PolicyDecision, string) {
	if p.policy == nil {
		return Deny, "no network policy configured"
	}
	decision, reason := p.policy.Decide(ctx, req)
	switch decision {
	case Allow, Deny, Ask:
		return decision, reason
	default:
		// An unknown decision is not a grant.
		return Deny, "unknown network policy decision"
	}
}

func (p *Proxy) record(ctx context.Context, req Request, decision PolicyDecision, reason string) error {
	if p.audit == nil {
		return nil
	}
	err := p.audit.RecordNetworkDecision(ctx, NetworkDecisionAudit{
		Timestamp:     time.Now().UTC(),
		Protocol:      req.Protocol,
		Host:          req.Host,
		Port:          req.Port,
		Method:        req.Method,
		Path:          req.Path,
		Decision:      decision,
		Reason:        reason,
		Client:        req.Client,
		PolicyVersion: p.policyVersion,
	})
	if err != nil {
		return fmt.Errorf("proxy: record network decision: %w", err)
	}
	return nil
}

// CapabilitySource is implemented by a policy that knows the execution limits
// bound to it (GrantPolicy reads them from its ExecutionGrant).
type CapabilitySource interface {
	CapabilityRequest(now time.Time) (capability.Request, bool)
}

// acquireCapability takes a lease from the configured broker. A configured
// broker with a policy that exposes no limits fails closed: the proxy must not
// forward traffic whose platform budget it cannot account for.
func (p *Proxy) acquireCapability(ctx context.Context, iface string) (capability.Lease, error) {
	if p.broker == nil {
		if p.allowNonDurable {
			return nil, nil
		}
		// Defense in depth: even if a caller bypassed NewProxy's validation, the
		// handler must not forward traffic it cannot account for.
		return nil, errors.New("no capability broker configured")
	}
	src, ok := p.policy.(CapabilitySource)
	if !ok {
		return nil, errors.New("policy exposes no execution limits")
	}
	req, ok := src.CapabilityRequest(time.Now())
	if !ok {
		return nil, errors.New("policy has no execution grant")
	}
	req.Interface = iface
	return p.broker.Acquire(ctx, req)
}

var errBodyTooLarge = errors.New("proxy: body too large")

func (p *Proxy) readRequestBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > p.maxBody {
		return nil, errBodyTooLarge
	}
	if r.Body == nil {
		return nil, nil
	}
	return readLimited(r.Body, p.maxBody)
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func (p *Proxy) outboundRequest(r *http.Request, body []byte) *http.Request {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL = r.URL
	out.Host = r.URL.Host
	out.Header = cloneHeader(r.Header)
	stripHopByHop(out.Header)
	out.Header.Del("Content-Length")
	// The body is buffered, so the forwarded message is neither chunked nor
	// trailed; leaving these would reintroduce hop-by-hop framing metadata.
	out.Trailer = nil
	out.TransferEncoding = nil
	if len(body) == 0 {
		out.Body = nil
		out.GetBody = nil
		out.ContentLength = 0
	} else {
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		out.ContentLength = int64(len(body))
	}
	return out
}

func requestTarget(u *url.URL) (string, int, error) {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", 0, errors.New("request has no host")
	}
	portStr := u.Port()
	if portStr == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			portStr = "80"
		case "https":
			portStr = "443"
		default:
			return "", 0, fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("invalid target port")
	}
	return host, port, nil
}

func splitAuthority(authority string) (string, string, error) {
	if authority == "" {
		return "", "", errors.New("empty authority")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		if strings.Contains(authority, ":") {
			return "", "", err // malformed IPv6/port, do not guess
		}
		return strings.ToLower(strings.TrimSuffix(authority, ".")), "443", nil
	}
	return strings.ToLower(strings.TrimSuffix(host, ".")), port, nil
}

func requestPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func clientID(r *http.Request) string {
	return r.RemoteAddr
}

func denialMessage(decision PolicyDecision, reason string) string {
	if reason == "" {
		reason = "denied"
	}
	if decision == Ask {
		return "proxy: approval required: " + reason
	}
	return "proxy: " + reason
}

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// stripHopByHop removes the standard hop-by-hop headers and every header named
// by a Connection header, so proxy-only metadata cannot cross the boundary.
func stripHopByHop(h http.Header) {
	for _, value := range h.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.TrimSpace(token); token != "" {
				h.Del(token)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	copyHeader(dst, src)
	return dst
}

func copyHeader(dst, src http.Header) {
	for name, values := range src {
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func tunnel(client, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	_ = client.Close()
	_ = upstream.Close()
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// proxyDialContext dials the explicitly approved proxy with a pinned address.
// The proxy is operator-configured, so it may legitimately be loopback or
// private; every other destination keeps the public-address filter. Pinning the
// resolved address still closes the DNS-rebinding window for the proxy itself.
func proxyDialContext(proxy *url.URL, allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	base := publicDialContext(allowLoopback)
	if proxy == nil || proxy.Hostname() == "" {
		return base
	}
	proxyHost := strings.ToLower(proxy.Hostname())
	proxyPort := proxy.Port()
	if proxyPort == "" {
		switch strings.ToLower(proxy.Scheme) {
		case "http":
			proxyPort = "80"
		case "https":
			proxyPort = "443"
		}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("egress: dial %s: %w", addr, err)
		}
		if proxyPort != "" && strings.EqualFold(host, proxyHost) && port == proxyPort {
			ip, err := resolveApprovedProxyIP(ctx, host)
			if err != nil {
				return nil, err
			}
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return base(ctx, network, addr)
	}
}

func resolveApprovedProxyIP(ctx context.Context, host string) (netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return literal.Unmap(), nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("egress: resolve approved proxy %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("egress: resolve approved proxy %s: no addresses", host)
	}
	return addrs[0].Unmap(), nil
}

// GrantPolicy binds the proxy to one approved ExecutionGrant. It is the
// production boundary: a request is allowed only when the grant is present,
// unexpired, and explicitly covers the method and target. An optional Next
// policy (for example RulePolicy) can only tighten the result further; it can
// never turn a grant denial into an Allow.
//
// RulePolicy by itself is a guardrail, not an authorization source. Production
// wiring must wrap it as GrantPolicy{Next: &RulePolicy{...}} so an operator
// rule cannot grant network access on its own.
type GrantPolicy struct {
	Grant *ExecutionGrant
	// Next is an optional additional guardrail. When nil, a request covered by
	// the grant is allowed.
	Next NetworkPolicy
	// Now is injectable for tests. Nil uses time.Now.
	Now func() time.Time
	// Validator re-checks the grant against authoritative state (lease,
	// program status/scope freshness, revisions and revocation epoch) before
	// Next runs. A nil validator keeps the local expiry/target checks only;
	// production should configure capability.ProgramValidator.
	Validator capability.GrantValidator
}

// CapabilityRequest exposes the grant's shared limits to the proxy broker so a
// proxied request consumes the same RPS/concurrency/daily budget as Egress.
func (p GrantPolicy) CapabilityRequest(now time.Time) (capability.Request, bool) {
	g := p.Grant
	if g == nil {
		return capability.Request{}, false
	}
	return capability.Request{
		AccountID:      g.AccountID,
		ProgramID:      g.ProgramID,
		Interface:      "http",
		MaxRPS:         g.MaxRPS,
		MaxConcurrency: g.MaxConcurrency,
		DailyLimit:     g.DailyLimit,
		Now:            now,
	}, true
}

func (p GrantPolicy) Decide(ctx context.Context, req Request) (PolicyDecision, string) {
	g := p.Grant
	if g == nil {
		return Deny, "no execution grant configured"
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if g.ExpiresAt.IsZero() {
		return Deny, "execution grant has no expiry"
	}
	if !g.ExpiresAt.After(now) {
		return Deny, "execution grant has expired"
	}

	// CONNECT carries no method; only the host/port target is checked. HTTP
	// requests must use an approved method.
	if req.Protocol != ProtocolHTTPSConnect {
		method := strings.ToUpper(strings.TrimSpace(req.Method))
		if method == "" {
			method = http.MethodGet
		}
		if _, forbidden := g.ForbiddenMethods[method]; forbidden {
			return Deny, "method " + method + " is forbidden by the execution grant"
		}
		if len(g.AllowedMethods) > 0 {
			if _, ok := g.AllowedMethods[method]; !ok {
				return Deny, "method " + method + " is not approved by the execution grant"
			}
		} else if method != http.MethodGet && method != http.MethodHead {
			return Deny, "only GET/HEAD are approved by the execution grant"
		}
	}

	u, err := grantRequestURL(req)
	if err != nil {
		return Deny, "invalid grant request: " + err.Error()
	}
	if err := g.AllowsTarget(u); err != nil {
		return Deny, err.Error()
	}

	// Re-check the grant against authoritative state before any upstream I/O.
	// An expired lease, a paused/stale program, a changed scope/policy snapshot
	// or a bumped revocation epoch is a hard Deny.
	if p.Validator != nil {
		if err := p.Validator.ValidateGrant(ctx, *g); err != nil {
			return Deny, "execution grant is not valid: " + err.Error()
		}
	}

	if p.Next == nil {
		return Allow, "execution grant allows the request"
	}
	decision, reason := p.Next.Decide(ctx, req)
	switch decision {
	case Allow, Deny, Ask:
		return decision, reason
	default:
		return Deny, "unknown network policy decision"
	}
}

// grantRequestURL rebuilds the request target a grant check needs. CONNECT is a
// host-level https target with path "/" and no method; HTTP uses its escaped
// path. Parsing the assembled string preserves the escaped form so the path
// canonicalizer sees the same bytes the policy did.
func grantRequestURL(req Request) (*url.URL, error) {
	scheme := "http"
	path := req.Path
	if req.Protocol == ProtocolHTTPSConnect {
		scheme = "https"
		path = "/"
	}
	if path == "" {
		path = "/"
	}
	port := req.Port
	if port == 0 {
		if scheme == "http" {
			port = 80
		} else {
			port = 443
		}
	}
	host := strings.Trim(strings.TrimSpace(req.Host), "[]")
	if host == "" {
		return nil, errors.New("request has no host")
	}
	return url.Parse(scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + path)
}

// RulePolicy adapts a declarative rule set to the proxy's NetworkPolicy. It is
// a guardrail, not an authorization source: rule allow means "this rule does
// not object", not "the request is approved". Production must bind it under a
// GrantPolicy so an operator rule cannot grant network access by itself.
type RulePolicy struct {
	Rules   *rules.RuleSet
	Action  domain.Action
	Program domain.ProgramID
	// DenyUnmatched makes an unmatched request a Deny. The default (false) is
	// pass-through so the policy can sit under GrantPolicy as a guardrail; set it
	// to true when RulePolicy is the only policy.
	DenyUnmatched bool
}

func (p RulePolicy) Decide(_ context.Context, req Request) (PolicyDecision, string) {
	if p.Rules == nil {
		return Deny, "no rule set configured"
	}
	action := p.Action
	if action == "" {
		action = domain.ActionActiveHTTP
	}
	asset := req.Host
	if req.Path != "" {
		asset = req.Host + req.Path
	}
	result, err := p.Rules.Evaluate(rules.Request{
		Action: action, Program: p.Program, Asset: asset, Method: req.Method,
	})
	if err != nil {
		return Deny, "invalid rule request: " + err.Error()
	}
	reason := rulePolicyReason(result)
	switch result.Decision {
	case rules.DecisionAllow:
		return Allow, reason
	case rules.DecisionForbidden:
		return Deny, reason
	case rules.DecisionRequireHuman:
		return Ask, reason
	case rules.DecisionNone:
		if p.DenyUnmatched {
			return Deny, "no matching rule"
		}
		return Allow, "no matching rule (guardrail pass-through)"
	default:
		return Deny, "unknown rule decision"
	}
}

func rulePolicyReason(result rules.Result) string {
	if len(result.Matched) == 0 {
		return "no matching rule"
	}
	parts := make([]string, 0, len(result.Matched))
	for _, m := range result.Matched {
		part := m.ID + ":" + string(m.Decision)
		if m.Justification != "" {
			part += ":" + m.Justification
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// AuditSink adapts network policy decisions to the existing audit.Event sink.
// The raw path is hashed because it can contain callback tokens or long digit
// runs that the audit secret scanner would reject.
type AuditSink struct {
	Auditor Auditor
}

func (s AuditSink) RecordNetworkDecision(ctx context.Context, e NetworkDecisionAudit) error {
	if s.Auditor == nil {
		return errors.New("harness: network audit sink has no auditor")
	}
	at := e.Timestamp
	if at.IsZero() {
		at = time.Now().UTC()
	}
	pathHash := ""
	if e.Path != "" {
		sum := sha256.Sum256([]byte(e.Path))
		pathHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	payload, err := json.Marshal(struct {
		Protocol      Protocol       `json:"protocol"`
		Host          string         `json:"host"`
		Port          int            `json:"port"`
		Method        string         `json:"method,omitempty"`
		PathHash      string         `json:"path_hash,omitempty"`
		Decision      PolicyDecision `json:"decision"`
		Reason        string         `json:"reason,omitempty"`
		Client        string         `json:"client,omitempty"`
		PolicyVersion string         `json:"policy_version,omitempty"`
	}{
		Protocol: e.Protocol, Host: e.Host, Port: e.Port, Method: e.Method,
		PathHash: pathHash, Decision: e.Decision, Reason: e.Reason,
		Client: e.Client, PolicyVersion: e.PolicyVersion,
	})
	if err != nil {
		return fmt.Errorf("harness: marshal network audit: %w", err)
	}
	_, err = s.Auditor.Append(ctx, audit.Event{
		Type: "network.policy_decision", EntityType: "network",
		EntityID:  e.Host + ":" + strconv.Itoa(e.Port),
		ActorKind: string(domain.ActorHarness), ActorID: "proxy",
		Payload: payload, RecordedAt: at,
	})
	if err != nil {
		return fmt.Errorf("harness: append network audit: %w", err)
	}
	return nil
}
