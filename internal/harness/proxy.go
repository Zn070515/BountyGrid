package harness

import (
	"bufio"
	"bytes"
	"context"
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
	// PolicyVersion is an optional fingerprint of the loaded policy data,
	// recorded with every audit event.
	PolicyVersion string
}

// Proxy is a forward proxy that evaluates an explicit NetworkPolicy for every
// request, audits the decision, and only then forwards or tunnels. It never
// consults http.ProxyFromEnvironment.
type Proxy struct {
	policy        NetworkPolicy
	audit         NetworkAuditSink
	transport     http.RoundTripper
	maxBody       int64
	allowLoopback bool
	policyVersion string

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
	return &Proxy{
		policy:        cfg.Policy,
		audit:         cfg.Audit,
		transport:     transport,
		maxBody:       maxBody,
		allowLoopback: cfg.AllowLoopback,
		policyVersion: cfg.PolicyVersion,
	}
}

// Handler returns the proxy as an http.Handler, suitable for httptest.
func (p *Proxy) Handler() http.Handler { return http.HandlerFunc(p.serveHTTP) }

// ListenAndServe listens on addr and serves until Close is called.
func (p *Proxy) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return p.Serve(ln)
}

// Serve serves the proxy on an existing listener until Close is called.
func (p *Proxy) Serve(ln net.Listener) error {
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
