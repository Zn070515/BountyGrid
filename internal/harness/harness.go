// Package harness is the execution boundary. A tool declares the Action it
// requires; the harness asks the Action Gateway for a decision and re-checks it
// immediately before running the tool. The harness has no policy authority.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
	"github.com/Zn070515/BountyGrid/internal/timeutil"
)

type ToolSpec struct {
	Name           string
	RequiredAction domain.Action
	NetworkEgress  bool
	ReadsFiles     bool
	WritesFiles    bool
	Destructive    bool
	Description    string
}

// RequiresReceipt reports whether the tool can produce an external or local
// side effect. Such tools must carry an idempotency key so a retry replays the
// recorded execution instead of running twice.
func (s ToolSpec) RequiresReceipt() bool {
	return s.Destructive || s.WritesFiles || s.NetworkEgress
}

type ToolResult struct {
	Output   string
	Evidence []domain.EvidenceID
}

// ToolContext is the only capability surface a tool receives. Egress is non-nil
// only for tools that declare NetworkEgress; every request through it is
// re-checked against the sandbox, including redirects.
type ToolContext struct {
	Request   domain.ActionRequest
	Decision  domain.ActionDecision
	Egress    *Egress
	Workspace *Workspace
	Budget    *Budget
}

type Tool interface {
	Spec() ToolSpec
	Cost() domain.Money
	Execute(ctx context.Context, tc ToolContext) (ToolResult, error)
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("harness: tool is nil")
	}
	spec := t.Spec()
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("harness: tool name is empty")
	}
	if !spec.RequiredAction.Valid() {
		return fmt.Errorf("harness: tool %s has invalid action %q", spec.Name, spec.RequiredAction)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[spec.Name]; exists {
		return fmt.Errorf("harness: tool %s already registered", spec.Name)
	}
	r.tools[spec.Name] = t
	return nil
}

func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("harness: tool %s not found", name)
	}
	return t, nil
}

func (r *Registry) List() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	return out
}

type Sandbox struct {
	AllowedHosts []string
	Workspace    string
	// ApprovedProxy is the only proxy egress may use. An empty value disables
	// proxy support entirely (http.ProxyFromEnvironment is not consulted), so an
	// environment variable cannot silently reroute traffic.
	ApprovedProxy string
	// AllowLoopback exists only for tests using httptest servers. It must never
	// be enabled in a production harness.
	AllowLoopback bool
}

// CheckURL denies egress unless the host is explicitly allowed. Wildcards match
// subdomains only, never the apex.
func (s *Sandbox) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("sandbox: invalid url %q", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("sandbox: scheme %q is not allowed", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range s.AllowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed {
			return nil
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := allowed[1:]
			if len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
				return nil
			}
		}
	}
	return fmt.Errorf("sandbox: egress to %s denied", host)
}

// Egress is the harness-controlled network client. A tool never receives raw
// network capability: every request (and every redirect) is checked against the
// sandbox allowlist before the connection is made.
var (
	ErrResponseTooLarge = errors.New("egress: response too large")
	ErrNonPublicAddress = errors.New("egress: resolved address is not public")
	ErrGrantDenied      = errors.New("egress: request is outside the execution grant")
)

// ExecutionGrant is the per-execution network authority derived from the
// approved decision. A tool cannot widen it: the approved method, target,
// rate/concurrency ceiling and daily limit are enforced on every request, not
// once per tool call.
type ExecutionGrant struct {
	DecisionID domain.DecisionID
	// AccountID is the account the decision was made for. Together with
	// ProgramID it identifies the shared rate/concurrency/daily limiter, so a
	// second execution for the same account and program cannot reset the ceiling.
	AccountID domain.AccountID
	ProgramID domain.ProgramID
	AssetID   *domain.AssetID
	// AssetTarget is the canonical asset value resolved by the Action Gateway.
	AssetTarget string
	// ExpiresAt is the decision's expiry. A zero value means the grant carries no
	// expiry; GrantPolicy rejects such a grant rather than treating it as
	// unlimited.
	ExpiresAt time.Time
	// AllowedMethods is empty only for a grant with no bound method, in which
	// case only GET/HEAD are permitted.
	AllowedMethods   map[string]struct{}
	ForbiddenMethods map[string]struct{}
	// AllowedPathPrefixes restricts a URL asset to the approved path tree. Empty
	// means the asset did not specify a path.
	AllowedPathPrefixes []string
	MaxRPS              *float64
	MaxConcurrency      *int
	// DailyLimit is the maximum requests in one business day; 0 means the grant
	// has no explicit daily ceiling (it still counts requests for audit).
	DailyLimit int64
}

// programLimiter is the shared rate/concurrency/daily state for one
// account+program+interface. It lives on the base Egress and is selected by a
// stable key, so separate WithGrant/Execute calls cannot each reset the
// platform's RPS, concurrency or daily ceilings.
type programLimiter struct {
	mu sync.Mutex
	// last is the last scheduled RPS slot. It is shared by every grant bound to
	// this limiter.
	last time.Time
	// rps is the strictest positive RPS observed on this limiter. A later grant
	// with a lower RPS tightens the shared schedule.
	rps float64
	// maxConc is the strictest concurrency ceiling observed (0 = unlimited).
	// A later tighter ceiling is applied to subsequent acquisitions.
	maxConc int
	// inFlight is the number of requests currently holding a concurrency slot.
	inFlight int
	// notify is closed and replaced on every release so blocked acquisitions
	// wake up and re-check the ceiling without a polling loop.
	notify chan struct{}
	daily  *dailyCounter
}

func newProgramLimiter() *programLimiter {
	return &programLimiter{notify: make(chan struct{}), daily: &dailyCounter{}}
}

// dailyCounter is the per-program daily request budget. Every ExecutionGrant
// for the same program shares one counter, so separate tool executions cannot
// each reset the platform's PerInterfaceDailyLimits.
type dailyCounter struct {
	mu   sync.Mutex
	day  string
	used int64
}

func (c *dailyCounter) count(now time.Time, limit int64) error {
	day := businessDay(now)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.day != day {
		c.day = day
		c.used = 0
	}
	if limit > 0 && c.used >= limit {
		return fmt.Errorf("%w: daily request limit %d reached", ErrGrantDenied, limit)
	}
	c.used++
	return nil
}

// limiterRegistry holds one programLimiter per account+program+interface key.
// The base Egress owns the registry so every derived WithGrant client shares
// the same limiter.
type limiterRegistry struct {
	mu       sync.Mutex
	limiters map[string]*programLimiter
}

func newLimiterRegistry() *limiterRegistry {
	return &limiterRegistry{limiters: make(map[string]*programLimiter)}
}

func (r *limiterRegistry) limiter(key string) *programLimiter {
	if key == "" {
		// A grant without account/program/asset identity cannot be shared
		// safely; give it its own limiter (fail-safe, not global).
		return newProgramLimiter()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.limiters[key]
	if l == nil {
		l = newProgramLimiter()
		r.limiters[key] = l
	}
	return l
}

// grantScopeKey groups grants that must share one rate/concurrency/daily
// limiter. AccountID+ProgramID is the stable identity; asset/asset-target are
// fallbacks for callers that build a grant without a program. The trailing
// "http" component is the interface/action namespace: HTTP and ACTIVE_HTTP
// collapse into the grant's single request budget.
func grantScopeKey(g ExecutionGrant) string {
	account := strings.TrimSpace(string(g.AccountID))
	scope := strings.TrimSpace(string(g.ProgramID))
	if scope == "" && g.AssetID != nil {
		scope = "asset:" + string(*g.AssetID)
	}
	if scope == "" {
		scope = strings.TrimSpace(g.AssetTarget)
	}
	if account == "" && scope == "" {
		return ""
	}
	return account + "|" + scope + "|http"
}

type Egress struct {
	sandbox  *Sandbox
	client   *http.Client
	maxBytes int64
	grant    *ExecutionGrant
	limiter  *programLimiter
	limiters *limiterRegistry
}

// DefaultEgressMaxBytes bounds a single response body. Tools must not be able
// to pull unbounded data through the harness.
const DefaultEgressMaxBytes = 8 << 20

func NewEgress(s *Sandbox) *Egress {
	if s == nil {
		s = &Sandbox{}
	}
	e := &Egress{sandbox: s, maxBytes: DefaultEgressMaxBytes, limiters: newLimiterRegistry()}
	approved, proxyErr := parseApprovedProxy(s.ApprovedProxy)
	transport := &http.Transport{
		// Proxy is nil unless an operator configured an approved proxy. An
		// environment variable must not silently reroute execution traffic.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DialContext:           proxyDialContext(approved, s.AllowLoopback),
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
	}
	switch {
	case proxyErr != nil:
		// A configured but unusable proxy fails closed instead of silently
		// falling back to a direct connection.
		transport.Proxy = func(*http.Request) (*url.URL, error) { return nil, proxyErr }
	case approved != nil:
		// The proxy dials the target; its NetworkPolicy enforces per-request
		// access. proxyDialContext still pins the proxy address and keeps the
		// public-address filter for direct (non-proxied) connections.
		transport.Proxy = http.ProxyURL(approved)
	}
	e.client = &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("egress: too many redirects")
			}
			return e.CheckURL(req.URL.String())
		},
	}
	return e
}

// parseApprovedProxy validates the operator-configured proxy. A nil URL with a
// nil error means no proxy is configured.
func parseApprovedProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("egress: approved proxy is not a valid url")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("egress: approved proxy must be an http(s) url with a host")
	}
	return u, nil
}

// WithGrant returns a per-execution client bound to the approved decision. The
// shared transport is reused. Method/target/path checks are per grant, but RPS,
// concurrency and the daily counter are shared through the base Egress's
// limiter registry: a second execution for the same account/program cannot
// reset the platform's ceilings.
func (e *Egress) WithGrant(g ExecutionGrant) *Egress {
	out := *e
	grant := g
	out.grant = &grant
	if e.limiters != nil {
		out.limiter = e.limiters.limiter(grantScopeKey(g))
	} else {
		out.limiter = newProgramLimiter()
	}
	// Redirects must be checked against the grant, not only the sandbox. The
	// base client's hook captures the original Egress (grant == nil), so a grant
	// client gets its own redirect policy closure.
	timeout := 30 * time.Second
	var transport http.RoundTripper
	if e.client != nil {
		timeout = e.client.Timeout
		transport = e.client.Transport
	}
	out.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("egress: too many redirects")
			}
			if err := out.CheckURL(req.URL.String()); err != nil {
				return err
			}
			return out.checkGrant(req)
		},
	}
	return &out
}

// SetMaxBytes overrides the response-body limit (tests or a stricter tool).
func (e *Egress) SetMaxBytes(n int64) {
	if n > 0 {
		e.maxBytes = n
	}
}

func (e *Egress) CheckURL(raw string) error {
	return e.sandbox.CheckURL(raw)
}

// Do performs one request after the sandbox and execution-grant checks. The
// response body is wrapped so reads beyond the configured limit fail rather
// than silently pulling unbounded data.
func (e *Egress) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("egress: request has no url")
	}
	if err := e.CheckURL(req.URL.String()); err != nil {
		return nil, err
	}
	if err := e.checkGrant(req); err != nil {
		return nil, err
	}
	release, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req.WithContext(ctx))
	if err != nil {
		release()
		return nil, err
	}
	if resp.ContentLength > e.maxBytes {
		_ = resp.Body.Close()
		release()
		return nil, ErrResponseTooLarge
	}
	resp.Body = &limitedBody{ReadCloser: &releasingBody{ReadCloser: resp.Body, release: release}, remaining: e.maxBytes + 1}
	return resp, nil
}

func (e *Egress) checkGrant(req *http.Request) error {
	g := e.grant
	if g == nil {
		return nil
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if _, forbidden := g.ForbiddenMethods[method]; forbidden {
		return fmt.Errorf("%w: method %s is forbidden", ErrGrantDenied, method)
	}
	if len(g.AllowedMethods) > 0 {
		if _, ok := g.AllowedMethods[method]; !ok {
			return fmt.Errorf("%w: method %s is not approved", ErrGrantDenied, method)
		}
	} else if method != http.MethodGet && method != http.MethodHead {
		return fmt.Errorf("%w: only GET/HEAD are approved", ErrGrantDenied)
	}
	if err := g.allowsTarget(req.URL); err != nil {
		return err
	}
	return nil
}

func (g *ExecutionGrant) allowsTarget(u *url.URL) error {
	target := strings.TrimSpace(g.AssetTarget)
	host := strings.ToLower(u.Hostname())
	// Asset values may be bare host patterns (api.example.com, *.example.com)
	// or full URLs including a path. A bare pattern is matched against the host;
	// a URL target additionally pins scheme and path prefix.
	if target != "" && !strings.Contains(target, "/") && !strings.Contains(target, ":") {
		if !rules.HostMatches(target, host) {
			return fmt.Errorf("%w: host %s is not the approved asset %s", ErrGrantDenied, host, target)
		}
	} else if target != "" {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Hostname() == "" {
			// A non-URL asset value (a description, CIDR, etc.) cannot be treated
			// as a network target. Do not widen the grant silently.
			return fmt.Errorf("%w: asset target %q is not a network target", ErrGrantDenied, target)
		}
		if parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, u.Scheme) {
			return fmt.Errorf("%w: scheme %s is not approved", ErrGrantDenied, u.Scheme)
		}
		if !rules.HostMatches(parsed.Hostname(), host) {
			return fmt.Errorf("%w: host %s is not the approved asset %s", ErrGrantDenied, host, parsed.Hostname())
		}
		if parsed.Port() != "" && parsed.Port() != u.Port() {
			return fmt.Errorf("%w: port %s is not approved", ErrGrantDenied, u.Port())
		}
		if parsed.Path != "" && parsed.Path != "/" {
			path := u.EscapedPath()
			if path == "" {
				path = "/"
			}
			// Segment-aware matching: /api must not match /apix or /api-admin.
			// CanonicalPath also decodes escapes and resolves dot segments, so
			// /api/%2e%2e/admin cannot escape an /api prefix.
			if !rules.PathWithin(parsed.EscapedPath(), path) {
				return fmt.Errorf("%w: path %s is outside %s", ErrGrantDenied, path, parsed.Path)
			}
		}
	}
	// AllowedPathPrefixes apply to bare-host assets too; a host-only asset must
	// not silently drop the grant's path restriction.
	for _, prefix := range g.AllowedPathPrefixes {
		if prefix == "" {
			continue
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if !rules.PathWithin(prefix, path) {
			return fmt.Errorf("%w: path %s is outside %s", ErrGrantDenied, path, prefix)
		}
	}
	return nil
}

func (e *Egress) acquire(ctx context.Context) (func(), error) {
	g := e.grant
	if g == nil {
		return func() {}, nil
	}
	l := e.limiter
	if l == nil {
		return func() {}, nil
	}
	if err := l.wait(ctx, g.MaxRPS); err != nil {
		return nil, err
	}
	if err := l.daily.count(time.Now(), g.DailyLimit); err != nil {
		return nil, err
	}
	maxConc := 0
	if g.MaxConcurrency != nil {
		maxConc = *g.MaxConcurrency
	}
	return l.acquireConc(ctx, maxConc)
}

// wait schedules the next request slot on the shared RPS timeline. The limiter
// keeps the strictest positive RPS observed, so a later grant with a lower rate
// tightens the shared schedule instead of resetting it.
func (l *programLimiter) wait(ctx context.Context, rps *float64) error {
	l.mu.Lock()
	if rps != nil && *rps > 0 && (l.rps == 0 || *rps < l.rps) {
		l.rps = *rps
	}
	// A grant with no RPS of its own still observes a ceiling already set on the
	// shared limiter.
	if l.rps == 0 {
		l.mu.Unlock()
		return nil
	}
	interval := time.Duration(float64(time.Second) / l.rps)
	now := time.Now()
	next := l.last.Add(interval)
	if next.Before(now) {
		next = now
	}
	l.last = next
	wait := time.Until(next)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// acquireConc takes one shared concurrency slot. A limit <= 0 means the grant
// imposes no ceiling of its own, but an already-observed tighter ceiling from
// another grant on the same limiter is still enforced. A later grant with a
// lower ceiling tightens subsequent acquisitions; it never raises an existing
// one.
func (l *programLimiter) acquireConc(ctx context.Context, limit int) (func(), error) {
	for {
		l.mu.Lock()
		if limit > 0 && (l.maxConc == 0 || limit < l.maxConc) {
			l.maxConc = limit
		}
		if l.maxConc == 0 {
			l.mu.Unlock()
			return func() {}, nil
		}
		if l.inFlight < l.maxConc {
			l.inFlight++
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() { l.releaseConc() })
			}, nil
		}
		ch := l.notify
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (l *programLimiter) releaseConc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight > 0 {
		l.inFlight--
	}
	close(l.notify)
	l.notify = make(chan struct{})
}

func businessDay(t time.Time) string {
	return t.In(timeutil.BusinessLocation()).Format("2006-01-02")
}

// publicDialContext resolves the host, rejects any private/loopback/link-local
// result, and dials the validated IP. Pinning the resolved IP closes the
// DNS-rebinding window between the allowlist check and the connection.
func publicDialContext(allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("egress: dial %s: %w", addr, err)
		}
		ip, err := resolvePublicIP(ctx, host, allowLoopback)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}

func resolvePublicIP(ctx context.Context, host string, allowLoopback bool) (netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !isPublicAddr(literal) && !(allowLoopback && literal.IsLoopback()) {
			return netip.Addr{}, fmt.Errorf("%w: %s", ErrNonPublicAddress, literal)
		}
		return literal, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("egress: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("egress: resolve %s: no addresses", host)
	}
	var chosen netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		// If any answer is non-public, fail closed: a mixed answer is exactly
		// what a rebinding attack uses to pass one check and connect elsewhere.
		if !isPublicAddr(a) && !(allowLoopback && a.IsLoopback()) {
			return netip.Addr{}, fmt.Errorf("%w: %s resolves to %s", ErrNonPublicAddress, host, a)
		}
		if !chosen.IsValid() {
			chosen = a
		}
	}
	return chosen, nil
}

func isPublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return false
	}
	return a.IsGlobalUnicast()
}

// GetBytes reads a response with the configured limit.
func (e *Egress) GetBytes(ctx context.Context, raw string) ([]byte, int, error) {
	resp, err := e.Get(ctx, raw)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

type releasingBody struct {
	io.ReadCloser
	release func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining <= 0 {
		// We allowed exactly maxBytes+1 bytes; consuming all of them means the
		// body is over the limit.
		return n, ErrResponseTooLarge
	}
	return n, err
}

// Get is a convenience wrapper for read-only tools.
func (e *Egress) Get(ctx context.Context, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("egress: build request: %w", err)
	}
	return e.Do(ctx, req)
}

type ReceiptStore interface {
	GetReceipt(ctx context.Context, idempotencyKey string) (domain.ExecutionReceipt, error)
	CreateReceipt(ctx context.Context, r domain.ExecutionReceipt) error
	UpdateReceipt(ctx context.Context, r domain.ExecutionReceipt) error
}

// MemoryReceiptStore is the durable-store contract's in-memory twin for tests.
type MemoryReceiptStore struct {
	mu    sync.Mutex
	byKey map[string]domain.ExecutionReceipt
}

func NewMemoryReceiptStore() *MemoryReceiptStore {
	return &MemoryReceiptStore{byKey: make(map[string]domain.ExecutionReceipt)}
}

func (s *MemoryReceiptStore) GetReceipt(ctx context.Context, key string) (domain.ExecutionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byKey[key]
	if !ok {
		return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
	}
	return r, nil
}

func (s *MemoryReceiptStore) CreateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[r.IdempotencyKey]; ok {
		return domain.ErrReceiptExists
	}
	s.byKey[r.IdempotencyKey] = r
	return nil
}

func (s *MemoryReceiptStore) UpdateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[r.IdempotencyKey]; !ok {
		return domain.ErrReceiptNotFound
	}
	s.byKey[r.IdempotencyKey] = r
	return nil
}

type Budget struct {
	MaxCost     domain.Money
	Spent       domain.Money
	MaxRequests int
	Requests    int

	mu sync.Mutex
}

func (b *Budget) Check(cost domain.Money) error {
	if cost.Amount < 0 {
		return errors.New("budget: negative cost")
	}
	if cost.Currency == "" && cost.Amount > 0 {
		return errors.New("budget: cost currency is empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.MaxRequests > 0 && b.Requests >= b.MaxRequests {
		return errors.New("budget: request limit reached")
	}
	if b.MaxCost.Amount > 0 {
		if cost.Amount > 0 && cost.Currency != b.MaxCost.Currency {
			return errors.New("budget: currency mismatch")
		}
		if b.Spent.Amount+cost.Amount > b.MaxCost.Amount {
			return errors.New("budget: cost limit reached")
		}
	}
	return nil
}

func (b *Budget) Charge(cost domain.Money) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Spent.Amount += cost.Amount
	b.Spent.Currency = cost.Currency
	b.Requests++
}

// BudgetReservation is a pre-charged budget slot. Reserving atomically checks
// and accounts for the cost so two concurrent workers cannot both pass the
// limit; Commit keeps the reservation, Release returns it.
type BudgetReservation struct {
	budget    *Budget
	cost      domain.Money
	committed bool
	released  bool
}

// Reserve atomically checks the limit and reserves the cost. A failure leaves
// the budget unchanged.
func (b *Budget) Reserve(cost domain.Money) (*BudgetReservation, error) {
	if cost.Amount < 0 {
		return nil, errors.New("budget: negative cost")
	}
	if cost.Currency == "" && cost.Amount > 0 {
		return nil, errors.New("budget: cost currency is empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.MaxRequests > 0 && b.Requests >= b.MaxRequests {
		return nil, errors.New("budget: request limit reached")
	}
	if b.MaxCost.Amount > 0 {
		if cost.Amount > 0 && cost.Currency != b.MaxCost.Currency {
			return nil, errors.New("budget: currency mismatch")
		}
		if b.Spent.Amount+cost.Amount > b.MaxCost.Amount {
			return nil, errors.New("budget: cost limit reached")
		}
	}
	b.Spent.Amount += cost.Amount
	if cost.Currency != "" {
		b.Spent.Currency = cost.Currency
	}
	b.Requests++
	return &BudgetReservation{budget: b, cost: cost}, nil
}

// Commit keeps the reserved budget. It is idempotent.
func (r *BudgetReservation) Commit() {
	if r == nil || r.released {
		return
	}
	r.committed = true
}

// Release returns the reserved budget. It is idempotent and never drops below
// zero even if a caller double-releases.
func (r *BudgetReservation) Release() {
	if r == nil || r.released || r.committed {
		return
	}
	r.released = true
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	r.budget.Spent.Amount -= r.cost.Amount
	if r.budget.Spent.Amount < 0 {
		r.budget.Spent.Amount = 0
	}
	if r.budget.Requests > 0 {
		r.budget.Requests--
	}
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

type Gate interface {
	Evaluate(ctx context.Context, in gateway.EvaluateInput) (domain.ActionDecision, error)
	AuthorizeWith(ctx context.Context, id domain.DecisionID, ai gateway.AuthorizeInput) error
}

type DeniedError struct {
	Decision domain.ActionDecision
}

func (e DeniedError) Error() string {
	return fmt.Sprintf("harness: action %s is %s", e.Decision.Action, e.Decision.Decision)
}

type Harness struct {
	registry         *Registry
	gate             Gate
	auditor          Auditor
	sandbox          *Sandbox
	egress           *Egress
	budget           *Budget
	receipts         ReceiptStore
	workspace        *Workspace
	allowDestructive bool
	now              func() time.Time
}

func New(reg *Registry, gate Gate, auditor Auditor, sandbox *Sandbox, budget *Budget) *Harness {
	if sandbox == nil {
		sandbox = &Sandbox{}
	}
	if budget == nil {
		budget = &Budget{}
	}
	return &Harness{
		registry: reg, gate: gate, auditor: auditor, sandbox: sandbox,
		egress: NewEgress(sandbox), budget: budget, receipts: NewMemoryReceiptStore(), now: time.Now,
	}
}

func (h *Harness) SetAllowDestructive(v bool) { h.allowDestructive = v }

// SetWorkspace confines file tools with os.Root. It replaces any previous
// workspace and closes it.
func (h *Harness) SetWorkspace(dir string) error {
	ws, err := OpenWorkspace(dir)
	if err != nil {
		return err
	}
	if h.workspace != nil {
		_ = h.workspace.Close()
	}
	h.workspace = ws
	return nil
}

func (h *Harness) Close() error {
	if h.workspace == nil {
		return nil
	}
	err := h.workspace.Close()
	h.workspace = nil
	return err
}

func (h *Harness) SetReceiptStore(s ReceiptStore) {
	if s != nil {
		h.receipts = s
	}
}
func (h *Harness) SetClock(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// Execute authorizes and runs one tool. The tool's RequiredAction overrides the
// request action; a mismatched non-empty action is rejected. The tool is never
// called unless the gateway returns ALLOW and Authorize passes.
//
// Tools that can produce side effects require an IdempotencyKey. The key is
// reserved before execution; a retry replays the receipt instead of running the
// tool again, and an execution whose audit write failed can be completed
// without repeating the side effect.
func (h *Harness) Execute(ctx context.Context, toolName string, in gateway.EvaluateInput) (domain.ActionDecision, ToolResult, error) {
	tool, err := h.registry.Get(toolName)
	if err != nil {
		return domain.ActionDecision{}, ToolResult{}, err
	}
	spec := tool.Spec()
	if spec.Destructive && !h.allowDestructive {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: destructive tool %s is disabled", toolName)
	}
	if in.Request.Action != "" && in.Request.Action != spec.RequiredAction {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: tool %s requires %s, request asked for %s",
			toolName, spec.RequiredAction, in.Request.Action)
	}
	in.Request.Action = spec.RequiredAction

	if spec.NetworkEgress && (h.sandbox == nil || len(h.sandbox.AllowedHosts) == 0) {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
			"harness: network tool %s requires a non-empty egress allowlist", toolName)
	}
	if (spec.ReadsFiles || spec.WritesFiles) && h.workspace == nil {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
			"harness: file tool %s requires a workspace", toolName)
	}

	key := strings.TrimSpace(in.Request.IdempotencyKey)
	needsReceipt := spec.RequiresReceipt()
	if needsReceipt && key == "" {
		return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
			"harness: tool %s requires an idempotency key", toolName)
	}
	var reserved *domain.ExecutionReceipt
	if key != "" {
		if h.receipts == nil {
			return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: no receipt store configured")
		}
		rec, err := h.receipts.GetReceipt(ctx, key)
		switch {
		case err == nil:
			if err := checkReceiptBinding(rec, in, toolName); err != nil {
				return domain.ActionDecision{}, ToolResult{}, err
			}
			switch rec.Status {
			case domain.ReceiptCompleted, domain.ReceiptExecuted:
				return h.replay(ctx, rec, toolName)
			case domain.ReceiptStarted:
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
					"harness: execution for key %s is unsettled; refusing to repeat it", key)
			case domain.ReceiptAmbiguous:
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
					"harness: execution for key %s may have reached the target; reconcile manually before retrying", key)
			case domain.ReceiptFailed:
				// The tool proved the side effect did not start; a retry under the
				// same key is allowed. Re-arm it only after the gate authorizes, so
				// a denied retry cannot leave a STARTED receipt that blocks future
				// attempts.
				reserved = &rec
			}
		case !errors.Is(err, domain.ErrReceiptNotFound):
			return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: read receipt: %w", err)
		}
	}

	decision, err := h.gate.Evaluate(ctx, in)
	if err != nil {
		return domain.ActionDecision{}, ToolResult{}, err
	}
	if decision.Decision != domain.DecisionAllow {
		return decision, ToolResult{}, DeniedError{Decision: decision}
	}
	// Budget is reserved before Authorize: a budget rejection must not consume a
	// single-use approval, and the atomic reservation prevents two concurrent
	// workers from both passing the limit.
	cost := tool.Cost()
	reservation, err := h.budget.Reserve(cost)
	if err != nil {
		return decision, ToolResult{}, err
	}
	reservationSettled := false
	defer func() {
		if !reservationSettled {
			reservation.Release()
		}
	}()
	if err := h.gate.AuthorizeWith(ctx, decision.ID, gateway.AuthorizeInput{
		UsageToday: in.UsageToday, BudgetSpent: in.BudgetSpent,
		Environment: in.Environment, Now: in.Now,
	}); err != nil {
		reservation.Release()
		reservationSettled = true
		return decision, ToolResult{}, err
	}

	// Reserve the key after authorization and budget checks, immediately
	// before the tool can act.
	if key != "" {
		now := h.now().UTC()
		if reserved == nil {
			rec := newReceipt(key, decision, in, toolName, now)
			if err := h.receipts.CreateReceipt(ctx, rec); err != nil {
				if !errors.Is(err, domain.ErrReceiptExists) {
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: reserve receipt: %w", err)
				}
				existing, gerr := h.receipts.GetReceipt(ctx, key)
				if gerr != nil {
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: read existing receipt: %w", gerr)
				}
				if berr := checkReceiptBinding(existing, in, toolName); berr != nil {
					return domain.ActionDecision{}, ToolResult{}, berr
				}
				switch existing.Status {
				case domain.ReceiptCompleted, domain.ReceiptExecuted:
					return h.replay(ctx, existing, toolName)
				case domain.ReceiptStarted:
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
						"harness: execution for key %s is unsettled; refusing to repeat it", key)
				case domain.ReceiptAmbiguous:
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
						"harness: execution for key %s may have reached the target; reconcile manually before retrying", key)
				case domain.ReceiptFailed:
					reserved = &existing
				default:
					return domain.ActionDecision{}, ToolResult{}, fmt.Errorf(
						"harness: receipt %s has unknown status %q", key, existing.Status)
				}
			} else {
				reserved = &rec
			}
		}
		// A FAILED receipt from an earlier attempt may be retried. Fill in the
		// binding fields as well, so receipts written before the binding existed
		// become fully checked after one retry.
		if reserved != nil && reserved.Status == domain.ReceiptFailed {
			*reserved = newReceipt(key, decision, in, toolName, now)
			reserved.StartedAt = now
			if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: re-arm receipt: %w", err)
			}
		}
	}

	tc := ToolContext{Request: in.Request, Decision: decision, Budget: h.budget}
	if spec.NetworkEgress {
		tc.Egress = h.egress.WithGrant(buildGrant(decision, in.Request))
	}
	if spec.ReadsFiles || spec.WritesFiles {
		tc.Workspace = h.workspace
	}
	// Past this point the reservation belongs to this execution; the deferred
	// release must not undo a charge for a tool that actually runs.
	reservationSettled = true
	res, err := tool.Execute(ctx, tc)
	if err != nil {
		reservation.Release()
		if reserved != nil {
			// An error is not proof that a side effect did not happen. Network and
			// destructive tools default to AMBIGUOUS (manual reconciliation)
			// unless the tool proves the request never left the process.
			reserved.Status = domain.ReceiptFailed
			if (spec.NetworkEgress || spec.Destructive) && !errors.Is(err, domain.ErrNotSent) {
				reserved.Status = domain.ReceiptAmbiguous
			}
			reserved.Error = err.Error()
			reserved.UpdatedAt = h.now().UTC()
			_ = h.receipts.UpdateReceipt(ctx, *reserved)
		}
		return decision, ToolResult{}, err
	}
	reservation.Commit()

	if reserved != nil {
		reserved.DecisionID = decision.ID
		reserved.Output = res.Output
		reserved.Evidence = res.Evidence
		reserved.Status = domain.ReceiptExecuted
		reserved.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
			return decision, res, fmt.Errorf("harness: record executed receipt: %w", err)
		}
	}
	if err := h.appendAudit(ctx, toolName, in.Request, decision); err != nil {
		// The tool already executed; the receipt stays EXECUTED so a retry can
		// finish the audit without executing again.
		return decision, res, err
	}
	if reserved != nil {
		reserved.Status = domain.ReceiptCompleted
		reserved.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, *reserved); err != nil {
			return decision, res, fmt.Errorf("harness: complete receipt: %w", err)
		}
	}
	return decision, res, nil
}

// buildGrant derives the network authority for one execution from the approved
// decision. The approved method is the only method a network tool may use; the
// program's allowed/forbidden lists and rate/concurrency/daily constraints are
// enforced per request by Egress.
func buildGrant(decision domain.ActionDecision, req domain.ActionRequest) ExecutionGrant {
	account := decision.Request.AccountID
	if account == "" {
		account = req.AccountID
	}
	g := ExecutionGrant{
		DecisionID:       decision.ID,
		AccountID:        account,
		ProgramID:        decision.ProgramID,
		AssetID:          decision.Request.AssetID,
		AssetTarget:      decision.AssetTarget,
		AllowedMethods:   map[string]struct{}{},
		ForbiddenMethods: map[string]struct{}{},
	}
	if decision.ExpiresAt != nil {
		g.ExpiresAt = *decision.ExpiresAt
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = strings.ToUpper(strings.TrimSpace(decision.Request.Method))
	}
	if method != "" {
		g.AllowedMethods[method] = struct{}{}
	}
	if c := decision.Constraints; c != nil {
		if method == "" {
			for _, m := range c.AllowedMethods {
				if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
					g.AllowedMethods[m] = struct{}{}
				}
			}
		}
		for _, m := range c.ForbiddenMethods {
			if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
				g.ForbiddenMethods[m] = struct{}{}
			}
		}
		g.MaxRPS = c.MaxRPS
		g.MaxConcurrency = c.MaxConcurrency
		// A per-interface HTTP/ACTIVE_HTTP daily limit applies to network
		// requests. Other interfaces (e.g. an API quota) cannot be attributed to
		// one request by this layer.
		for _, l := range c.PerInterfaceDailyLimits {
			key := strings.ToUpper(strings.TrimSpace(l.Interface))
			if key == "HTTP" || key == "ACTIVE_HTTP" {
				if g.DailyLimit == 0 || l.Amount < g.DailyLimit {
					g.DailyLimit = l.Amount
				}
			}
		}
	}
	if target := strings.TrimSpace(g.AssetTarget); target != "" {
		if u, err := url.Parse(target); err == nil && u.Path != "" && u.Path != "/" {
			g.AllowedPathPrefixes = append(g.AllowedPathPrefixes, u.EscapedPath())
		}
	}
	return g
}

func newReceipt(key string, decision domain.ActionDecision, in gateway.EvaluateInput, toolName string, now time.Time) domain.ExecutionReceipt {
	var asset *domain.AssetID
	if in.Request.AssetID != nil {
		v := *in.Request.AssetID
		asset = &v
	}
	return domain.ExecutionReceipt{
		ID: "rcpt/" + key, IdempotencyKey: key, DecisionID: decision.ID,
		ToolName: toolName, Action: in.Request.Action,
		AccountID: in.Request.AccountID, ProgramID: in.Request.ProgramID,
		AssetID: asset, ResourceID: in.ResourceID,
		ParametersHash: domain.CanonicalParametersHash(in.Request.Parameters),
		Actor:          in.Request.Actor,
		Status:         domain.ReceiptStarted, StartedAt: now, UpdatedAt: now,
	}
}

// checkReceiptBinding refuses a replay whose request identity differs from the
// one that produced the receipt. The idempotency key identifies one logical
// action; it must never return another program's/asset's result without policy.
func checkReceiptBinding(rec domain.ExecutionReceipt, in gateway.EvaluateInput, toolName string) error {
	if rec.ToolName != "" && rec.ToolName != toolName {
		return fmt.Errorf("harness: idempotency key %s is bound to tool %s, not %s",
			rec.IdempotencyKey, rec.ToolName, toolName)
	}
	if rec.Action != "" && rec.Action != in.Request.Action {
		return fmt.Errorf("harness: idempotency key %s is bound to action %s, not %s",
			rec.IdempotencyKey, rec.Action, in.Request.Action)
	}
	if rec.AccountID != "" && rec.AccountID != in.Request.AccountID {
		return fmt.Errorf("harness: idempotency key %s is bound to another account", rec.IdempotencyKey)
	}
	if rec.ProgramID != "" && rec.ProgramID != in.Request.ProgramID {
		return fmt.Errorf("harness: idempotency key %s is bound to another program", rec.IdempotencyKey)
	}
	if !sameAssetID(rec.AssetID, in.Request.AssetID) {
		return fmt.Errorf("harness: idempotency key %s is bound to another asset", rec.IdempotencyKey)
	}
	// New receipts always carry a ParametersHash; compare the resource exactly
	// so an empty original resource cannot be replayed for a report/finding.
	if rec.ParametersHash != "" && rec.ResourceID != in.ResourceID {
		return fmt.Errorf("harness: idempotency key %s is bound to another resource", rec.IdempotencyKey)
	}
	if rec.ParametersHash != "" && rec.ParametersHash != domain.CanonicalParametersHash(in.Request.Parameters) {
		return fmt.Errorf("harness: idempotency key %s is bound to different parameters", rec.IdempotencyKey)
	}
	return nil
}

func sameAssetID(a, b *domain.AssetID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// replay returns the recorded result without calling the tool. An EXECUTED
// receipt gets its pending audit event written first.
func (h *Harness) replay(ctx context.Context, rec domain.ExecutionReceipt, toolName string) (domain.ActionDecision, ToolResult, error) {
	decision := domain.ActionDecision{ID: rec.DecisionID, Decision: domain.DecisionAllow, Action: rec.Action, ProgramID: rec.ProgramID}
	if rec.Status == domain.ReceiptExecuted {
		if err := h.appendAudit(ctx, toolName, domain.ActionRequest{Actor: rec.Actor}, decision); err != nil {
			return decision, ToolResult{}, err
		}
		rec.Status = domain.ReceiptCompleted
		rec.UpdatedAt = h.now().UTC()
		if err := h.receipts.UpdateReceipt(ctx, rec); err != nil {
			return decision, ToolResult{}, fmt.Errorf("harness: complete replayed receipt: %w", err)
		}
	}
	return decision, ToolResult{Output: rec.Output, Evidence: rec.Evidence}, nil
}

func (h *Harness) appendAudit(ctx context.Context, toolName string, req domain.ActionRequest, decision domain.ActionDecision) error {
	if h.auditor == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"tool": toolName, "action": string(decision.Action), "decision": string(decision.Decision),
	})
	if err != nil {
		return err
	}
	if _, err := h.auditor.Append(ctx, audit.Event{
		Type: "tool.executed", EntityType: "decision", EntityID: string(decision.ID),
		ActorKind: string(req.Actor.Kind), ActorID: req.Actor.ID,
		Payload: payload, RecordedAt: h.now().UTC(),
	}); err != nil {
		return fmt.Errorf("harness: audit tool: %w", err)
	}
	return nil
}

// ReadOnlyTool is a local, non-network tool used for the Phase 5 boundary demo.
type ReadOnlyTool struct {
	ToolName    string
	Action      domain.Action
	Description string
}

func (t ReadOnlyTool) Spec() ToolSpec {
	return ToolSpec{Name: t.ToolName, RequiredAction: t.Action, Description: t.Description}
}
func (t ReadOnlyTool) Cost() domain.Money { return domain.Money{} }
func (t ReadOnlyTool) Execute(ctx context.Context, tc ToolContext) (ToolResult, error) {
	return ToolResult{Output: "read-only " + t.ToolName + " completed"}, nil
}

func NewBuiltinRegistry() *Registry {
	r := NewRegistry()
	_ = r.Register(ReadOnlyTool{ToolName: "view_rules", Action: domain.ActionViewRules, Description: "read only public rules"})
	_ = r.Register(ReadOnlyTool{ToolName: "read_scope", Action: domain.ActionReadScope, Description: "read an unlocked scope"})
	_ = r.Register(ReadOnlyTool{ToolName: "report_draft", Action: domain.ActionReportDraft, Description: "local report draft"})
	return r
}
