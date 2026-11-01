// Package capability owns the shared execution-capability plane.
//
// Egress, the local proxy and (later) any other execution surface select a
// lease from one Broker keyed by account + program + interface. The Broker is
// the single place that enforces:
//
//   - RPS (a shared, strictly tightening schedule),
//   - concurrency (a shared, strictly tightening in-flight ceiling), and
//   - the durable daily quota (via DailyQuota).
//
// Keeping this state out of individual executions is what prevents a second
// tool call, a retry, or a proxy-forwarded request from resetting the
// platform's limits.
package capability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/timeutil"
)

// ErrLimit marks a local capability limit (currently the daily quota). It also
// wraps ErrGrantDenied so existing Egress callers that match the broader grant
// sentinel keep failing closed. Callers may map it to HTTP 429 / a non-retryable
// execution error. Context errors and storage errors are returned unwrapped so a
// caller can tell "denied" from "cannot decide".
var ErrLimit = fmt.Errorf("%w: daily request limit", ErrGrantDenied)

// Request is one acquisition of execution capability.
type Request struct {
	AccountID domain.AccountID
	ProgramID domain.ProgramID
	// Interface namespaces the limits (for example "http" or
	// "https_connect"). Two surfaces that name the same interface for the same
	// account/program share a limiter.
	Interface string
	// MaxRPS is the strictest permitted request rate. Nil means the requester
	// states no rate; a strictly lower rate already observed on the shared
	// limiter still applies.
	MaxRPS *float64
	// MaxConcurrency is the strictest permitted in-flight request count. Nil
	// means the requester states no ceiling; a strictly lower ceiling already
	// observed on the shared limiter still applies.
	MaxConcurrency *int
	// DailyLimit is the maximum requests in one business day. 0 means no
	// explicit ceiling; the request is still counted so a later tighter grant
	// sees the true usage.
	DailyLimit int64
	// Now is the reference time for the business date and the RPS schedule. A
	// zero value uses time.Now.
	Now time.Time
}

// Lease is one held capability slot. Release is idempotent and must be called
// after the request (or tunnel) finishes so the concurrency slot is freed.
type Lease interface {
	Release()
}

// Broker acquires a capability lease for one request. Implementations must be
// safe for concurrent use.
type Broker interface {
	Acquire(ctx context.Context, req Request) (Lease, error)
}

// DailyQuota is the durable daily usage counter. Consume must atomically
// increment the counter only while it is below limit and report whether the
// increment happened. A limit of 0 means "no ceiling, but keep counting".
//
// key identifies account + program + interface (see QuotaKey); businessDate is
// the Asia/Shanghai calendar date ("YYYY-MM-DD").
type DailyQuota interface {
	Consume(ctx context.Context, key string, businessDate string, limit int64) (bool, error)
}

// QuotaKey builds the stable key shared by the in-memory broker and its
// durable adapters. The unit-separator byte cannot appear in a platform ID.
func QuotaKey(accountID domain.AccountID, programID domain.ProgramID, iface string) string {
	return string(accountID) + keySeparator + string(programID) + keySeparator + iface
}

// SplitQuotaKey reverses QuotaKey. It is used by durable adapters that need
// the typed columns rather than the opaque key.
func SplitQuotaKey(key string) (accountID, programID, iface string, err error) {
	parts := splitKey(key)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("capability: malformed quota key %q", key)
	}
	return parts[0], parts[1], parts[2], nil
}

const keySeparator = "\x1f"

func splitKey(key string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == keySeparator[0] {
			out = append(out, key[start:i])
			start = i + 1
		}
	}
	out = append(out, key[start:])
	return out
}

// MemoryBroker is the default in-process Broker. It keeps one limiter per
// account+program+interface and delegates daily counting to a DailyQuota
// (in-memory by default; inject PostgresDailyQuota for durable usage).
type MemoryBroker struct {
	mu        sync.Mutex
	limiters  map[string]*programLimiter
	quota     DailyQuota
	ephemeral uint64
}

// BrokerOption customises the in-memory broker.
type BrokerOption func(*MemoryBroker)

// WithDailyQuota injects a durable daily quota implementation.
func WithDailyQuota(q DailyQuota) BrokerOption {
	return func(b *MemoryBroker) {
		if q != nil {
			b.quota = q
		}
	}
}

// NewMemoryBroker returns a process-local broker. Its limiter map is shared by
// every Egress/Proxy that receives the returned value.
func NewMemoryBroker(opts ...BrokerOption) *MemoryBroker {
	b := &MemoryBroker{
		limiters: make(map[string]*programLimiter),
		quota:    NewMemoryDailyQuota(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

// Acquire waits for an RPS slot, consumes one daily request, and takes a
// concurrency slot, in that order. A request with neither account nor program
// identity gets an ephemeral (non-shared) limiter, matching the fail-safe
// behavior of a grant that cannot be attributed to a platform object.
func (b *MemoryBroker) Acquire(ctx context.Context, req Request) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, limiter := b.limiterFor(req)
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := limiter.wait(ctx, req.MaxRPS, now); err != nil {
		return nil, err
	}
	day := now.In(timeutil.BusinessLocation()).Format("2006-01-02")
	ok, err := b.quota.Consume(ctx, key, day, req.DailyLimit)
	if err != nil {
		return nil, err
	}
	if !ok {
		if req.DailyLimit > 0 {
			return nil, fmt.Errorf("%w: %d reached", ErrLimit, req.DailyLimit)
		}
		return nil, ErrLimit
	}
	maxConc := 0
	if req.MaxConcurrency != nil {
		maxConc = *req.MaxConcurrency
	}
	return limiter.acquireConc(ctx, maxConc)
}

// limiterFor returns the shared limiter and quota key for a request. It never
// returns a nil limiter; an unidentified request receives a new limiter that
// is not stored, so it cannot reset or leak another caller's shared state.
func (b *MemoryBroker) limiterFor(req Request) (string, *programLimiter) {
	account := req.AccountID
	program := req.ProgramID
	if account == "" && program == "" {
		b.mu.Lock()
		b.ephemeral++
		id := b.ephemeral
		b.mu.Unlock()
		return fmt.Sprintf("__ephemeral/%d", id), newProgramLimiter()
	}
	key := QuotaKey(account, program, req.Interface)
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.limiters[key]
	if l == nil {
		l = newProgramLimiter()
		b.limiters[key] = l
	}
	return key, l
}

// MemoryDailyQuota is an in-memory DailyQuota for tests and single-process
// runs. It is safe for concurrent use.
type MemoryDailyQuota struct {
	mu     sync.Mutex
	counts map[string]*dailyCount
}

type dailyCount struct {
	date  string
	count int64
}

func NewMemoryDailyQuota() *MemoryDailyQuota {
	return &MemoryDailyQuota{counts: make(map[string]*dailyCount)}
}

func (q *MemoryDailyQuota) Consume(ctx context.Context, key, businessDate string, limit int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	c := q.counts[key]
	if c == nil || c.date != businessDate {
		c = &dailyCount{date: businessDate}
		q.counts[key] = c
	}
	if limit > 0 && c.count >= limit {
		return false, nil
	}
	c.count++
	return true, nil
}

// programLimiter is the shared rate/concurrency state for one
// account+program+interface.
type programLimiter struct {
	mu sync.Mutex
	// last is the last scheduled RPS slot, shared by every request bound to
	// this limiter.
	last time.Time
	// rps is the strictest positive RPS observed. A later lower rate tightens
	// the shared schedule instead of resetting it.
	rps float64
	// maxConc is the strictest concurrency ceiling observed (0 = unlimited).
	maxConc int
	// inFlight is the number of requests currently holding a slot.
	inFlight int
	// notify is closed and replaced on every release so blocked acquisitions
	// wake and re-check without polling.
	notify chan struct{}
}

func newProgramLimiter() *programLimiter {
	return &programLimiter{notify: make(chan struct{})}
}

// wait schedules the next request slot on the shared RPS timeline.
func (l *programLimiter) wait(ctx context.Context, rps *float64, now time.Time) error {
	l.mu.Lock()
	if rps != nil && *rps > 0 && (l.rps == 0 || *rps < l.rps) {
		l.rps = *rps
	}
	if l.rps == 0 {
		l.mu.Unlock()
		return nil
	}
	interval := time.Duration(float64(time.Second) / l.rps)
	next := l.last.Add(interval)
	if next.Before(now) {
		next = now
	}
	l.last = next
	wait := next.Sub(now)
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

// acquireConc takes one shared concurrency slot. A limit <= 0 states no
// ceiling of its own, but a tighter ceiling already observed on the shared
// limiter is still enforced. A later lower ceiling tightens subsequent
// acquisitions; it never raises an existing one.
func (l *programLimiter) acquireConc(ctx context.Context, limit int) (Lease, error) {
	for {
		l.mu.Lock()
		if limit > 0 && (l.maxConc == 0 || limit < l.maxConc) {
			l.maxConc = limit
		}
		if l.maxConc == 0 {
			l.mu.Unlock()
			return noopLease{}, nil
		}
		if l.inFlight < l.maxConc {
			l.inFlight++
			l.mu.Unlock()
			var once sync.Once
			return releaseLease(func() { once.Do(l.releaseConc) }), nil
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

type releaseLease func()

func (l releaseLease) Release() { l() }

type noopLease struct{}

func (noopLease) Release() {}
