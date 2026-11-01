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
	"github.com/Zn070515/BountyGrid/internal/capability"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
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
	// ErrGrantDenied aliases the capability sentinel so existing harness
	// callers can keep matching it with errors.Is.
	ErrGrantDenied = capability.ErrGrantDenied
	// ErrGrantValidatorRequired means a grant-bound Egress has no
	// capability.GrantValidator. Production must configure one; without it the
	// lease, program status, revisions and revocation epoch are never re-checked.
	ErrGrantValidatorRequired = errors.New("egress: grant-bound request requires a capability.GrantValidator")
	// ErrGrantRedirect means a grant-bound request tried to follow a redirect.
	// One grant authorizes one request; a redirect is a second request that was
	// never authorized or charged against the platform budget.
	ErrGrantRedirect = fmt.Errorf("%w: redirects are not permitted under an execution grant", ErrGrantDenied)
)

// ExecutionGrant is the capability package's grant type. The alias keeps the
// harness/proxy API stable while the shared capability plane owns grant
// semantics (target matching, lease and revision binding).
type ExecutionGrant = capability.ExecutionGrant

type Egress struct {
	sandbox  *Sandbox
	client   *http.Client
	maxBytes int64
	grant    *ExecutionGrant
	broker   capability.Broker
	// validator re-checks a grant against authoritative state (lease, program
	// status, revisions, revocation epoch) before every request.
	validator capability.GrantValidator
	// allowUnvalidatedGrant is a test-only escape hatch. A grant-bound Egress
	// without a validator fails closed in production.
	allowUnvalidatedGrant bool
}

// DefaultEgressMaxBytes bounds a single response body. Tools must not be able
// to pull unbounded data through the harness.
const DefaultEgressMaxBytes = 8 << 20

func NewEgress(s *Sandbox) *Egress {
	if s == nil {
		s = &Sandbox{}
	}
	e := &Egress{
		sandbox:  s,
		maxBytes: DefaultEgressMaxBytes,
		broker:   capability.NewMemoryBroker(),
	}
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
// concurrency and the daily counter are shared through the injected
// capability.Broker (in-memory by default): a second execution for the same
// account/program cannot reset the platform's ceilings.
func (e *Egress) WithGrant(g ExecutionGrant) *Egress {
	out := *e
	grant := g
	out.grant = &grant
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
			// Fail closed: one grant authorizes one request. Following a redirect
			// would send a second request that was never authorized, validated or
			// charged against the platform's RPS/daily budget. A caller that needs
			// the redirect target must obtain a new decision/grant.
			return ErrGrantRedirect
		},
	}
	return &out
}

// WithBroker returns a copy of this Egress that uses a shared capability
// broker. Use it when a proxy (or another execution surface) must enforce the
// same account/program/interface RPS, concurrency and daily quota.
func (e *Egress) WithBroker(b capability.Broker) *Egress {
	out := *e
	if b != nil {
		out.broker = b
	} else {
		out.broker = capability.NewMemoryBroker()
	}
	return &out
}

// SetBroker replaces the base Egress broker. It is a setup-time operation;
// callers must not race it with in-flight requests.
func (e *Egress) SetBroker(b capability.Broker) {
	if b != nil {
		e.broker = b
	}
}

// WithValidator returns a copy of this Egress bound to a grant validator.
// Set it before WithGrant (the grant copy inherits the validator) or on the
// grant-bound copy.
func (e *Egress) WithValidator(v capability.GrantValidator) *Egress {
	out := *e
	if v != nil {
		out.validator = v
	}
	return &out
}

// SetValidator replaces the grant validator on the base Egress. It is a
// setup-time operation; callers must not race it with in-flight requests.
func (e *Egress) SetValidator(v capability.GrantValidator) {
	if v != nil {
		e.validator = v
	}
}

// AllowUnvalidatedGrantsForTests lets a grant-bound Egress run without a
// capability.GrantValidator. It exists only for tests that exercise the
// transport/grant mechanics in isolation; production must configure a
// ProgramValidator.
func (e *Egress) AllowUnvalidatedGrantsForTests() *Egress {
	out := *e
	out.allowUnvalidatedGrant = true
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
	if err := e.validateGrant(ctx); err != nil {
		return nil, err
	}
	// Waiting for an RPS/concurrency slot can outlive the grant. Bound the wait
	// by the lease/expiry and re-validate after the wait, immediately before
	// the request would be sent.
	acquireCtx, cancel := e.acquireContext(ctx)
	release, err := e.acquire(acquireCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	if err := e.validateGrant(ctx); err != nil {
		release()
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
	if err := g.AllowsTarget(req.URL); err != nil {
		return err
	}
	return nil
}

// validateGrant re-checks the grant against authoritative state before the
// request is admitted. A grant-bound Egress without a validator fails closed;
// the test-only escape hatch must never be set in production.
func (e *Egress) validateGrant(ctx context.Context) error {
	if e.grant == nil {
		return nil
	}
	if e.validator == nil {
		if e.allowUnvalidatedGrant {
			return nil
		}
		return ErrGrantValidatorRequired
	}
	return e.validator.ValidateGrant(ctx, *e.grant)
}

// acquireContext bounds a broker wait by the grant's lease and decision
// expiry. A grant that expires while queued must not result in a request.
func (e *Egress) acquireContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.grant == nil {
		return ctx, func() {}
	}
	deadline := e.grant.ExpiresAt
	if !e.grant.LeaseUntil.IsZero() && (deadline.IsZero() || e.grant.LeaseUntil.Before(deadline)) {
		deadline = e.grant.LeaseUntil
	}
	if deadline.IsZero() {
		return ctx, func() {}
	}
	// An already-past recorded deadline is a validator concern: creating an
	// immediately-cancelled wait context would hide the authoritative rejection
	// (and breaks injected-clock tests). The validator still runs before and
	// after the wait.
	if !deadline.After(time.Now()) {
		return ctx, func() {}
	}
	if d, ok := ctx.Deadline(); ok && !d.After(deadline) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline)
}

// acquire takes a shared capability lease for one request. The grant's
// account/program/interface selects the limiter in the injected broker.
func (e *Egress) acquire(ctx context.Context) (func(), error) {
	g := e.grant
	if g == nil || e.broker == nil {
		return func() {}, nil
	}
	lease, err := e.broker.Acquire(ctx, capability.Request{
		AccountID:      g.AccountID,
		ProgramID:      g.ProgramID,
		Interface:      "http",
		MaxRPS:         g.MaxRPS,
		MaxConcurrency: g.MaxConcurrency,
		DailyLimit:     g.DailyLimit,
	})
	if err != nil {
		return nil, err
	}
	if lease == nil {
		// Defensive: a custom broker must not make Egress panic on a nil lease.
		return func() {}, nil
	}
	return lease.Release, nil
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
	revocations      capability.RevocationRegistry
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

// SetBroker injects the shared capability broker (RPS/concurrency/daily
// quota). The same broker can be given to a proxy so both surfaces enforce one
// account/program/interface budget.
func (h *Harness) SetBroker(b capability.Broker) {
	h.egress.SetBroker(b)
}

// SetRevocationRegistry wires the authorization-epoch source used to stamp
// execution grants. A later Bump makes every grant for the program invalid.
func (h *Harness) SetRevocationRegistry(r capability.RevocationRegistry) {
	h.revocations = r
}

// SetGrantValidator wires the authoritative grant re-check used by every
// network request. Production must configure a capability.ProgramValidator (or
// an equivalent); without one a grant-bound Egress fails closed.
func (h *Harness) SetGrantValidator(v capability.GrantValidator) {
	h.egress.SetValidator(v)
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
		grant := buildGrant(decision, in.Request, h.now())
		if h.revocations != nil {
			epoch, epochErr := h.revocations.Epoch(ctx, grant.ProgramID)
			if epochErr != nil {
				return domain.ActionDecision{}, ToolResult{}, fmt.Errorf("harness: read revocation epoch: %w", epochErr)
			}
			grant.AuthorizationEpoch = epoch
		}
		tc.Egress = h.egress.WithGrant(grant)
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
func buildGrant(decision domain.ActionDecision, req domain.ActionRequest, now time.Time) ExecutionGrant {
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
		// The grant is bound to the exact scope/policy snapshots the decision
		// was evaluated against. A later edit invalidates it in ProgramValidator.
		ScopeRevision:  decision.ScopeRevision,
		PolicyRevision: decision.PolicyRevision,
	}
	if decision.ExpiresAt != nil {
		g.ExpiresAt = *decision.ExpiresAt
	}
	// A grant has its own short lease, always capped by the decision expiry. An
	// execution that outlives the lease must obtain a new decision.
	g.LeaseUntil = now.Add(capability.DefaultGrantLease)
	if !g.ExpiresAt.IsZero() && g.ExpiresAt.Before(g.LeaseUntil) {
		g.LeaseUntil = g.ExpiresAt
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
