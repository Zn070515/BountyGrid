package capability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func acquire(t *testing.T, b Broker, req Request, timeout time.Duration) Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	lease, err := b.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return lease
}

func TestBrokerSharesRPSAcrossAcquisitions(t *testing.T) {
	b := NewMemoryBroker()
	rps := 0.01 // one request per 100 seconds
	req := Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", MaxRPS: &rps}

	first := acquire(t, b, req, time.Second)
	first.Release()

	// A second acquisition for the same account/program/interface shares the
	// scheduled slot and must wait for it.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire err = %v, want deadline exceeded from the shared RPS slot", err)
	}
}

func TestBrokerSharesConcurrencyAndWakesOnRelease(t *testing.T) {
	b := NewMemoryBroker()
	maxConc := 1
	req := Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", MaxConcurrency: &maxConc}

	first := acquire(t, b, req, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire err = %v, want deadline exceeded from the shared concurrency slot", err)
	}

	first.Release()
	// Release is idempotent and wakes the blocked acquisition.
	first.Release()

	second := acquire(t, b, req, time.Second)
	second.Release()
}

func TestBrokerSeparateProgramsHaveSeparateLimiters(t *testing.T) {
	b := NewMemoryBroker()
	maxConc := 1
	held := acquire(t, b, Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", MaxConcurrency: &maxConc}, time.Second)
	defer held.Release()

	other := acquire(t, b, Request{AccountID: "acc1", ProgramID: "p2", Interface: "http", MaxConcurrency: &maxConc}, time.Second)
	other.Release()

	// A different interface for the same program is also independent.
	conn := acquire(t, b, Request{AccountID: "acc1", ProgramID: "p1", Interface: "https_connect", MaxConcurrency: &maxConc}, time.Second)
	conn.Release()
}

func TestBrokerDailyQuota(t *testing.T) {
	b := NewMemoryBroker()
	req := Request{
		AccountID: "acc1", ProgramID: "p1", Interface: "http", DailyLimit: 1,
		Now: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
	}
	first := acquire(t, b, req, time.Second)
	first.Release()

	_, err := b.Acquire(context.Background(), req)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("second acquire err = %v, want ErrLimit", err)
	}
	// Local limits fail closed under the grant sentinel too, so existing Egress
	// callers keep working.
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("second acquire err = %v, want it to wrap ErrGrantDenied", err)
	}

	// A different program keeps its own daily budget.
	other := req
	other.ProgramID = "p2"
	lease := acquire(t, b, other, time.Second)
	lease.Release()

	// The next business day starts a fresh count.
	nextDay := req
	nextDay.Now = req.Now.Add(24 * time.Hour)
	lease = acquire(t, b, nextDay, time.Second)
	lease.Release()
}

func TestBrokerZeroDailyLimitStillCounts(t *testing.T) {
	b := NewMemoryBroker()
	req := Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", DailyLimit: 0}
	for i := 0; i < 3; i++ {
		lease := acquire(t, b, req, time.Second)
		lease.Release()
	}
	// The three unlimited requests were still counted; a later limit of 3 is
	// therefore already exhausted.
	tight := req
	tight.DailyLimit = 3
	if _, err := b.Acquire(context.Background(), tight); !errors.Is(err, ErrLimit) {
		t.Fatalf("acquire with limit 3 after 3 counts err = %v, want ErrLimit", err)
	}
}

// recordingQuota counts Consume calls so a test can prove a cancelled waiter
// never spends a daily slot.
type recordingQuota struct {
	mu    sync.Mutex
	calls int
}

func (q *recordingQuota) Consume(ctx context.Context, _ string, _ string, _ int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	q.calls++
	q.mu.Unlock()
	return true, nil
}

func (q *recordingQuota) Durable() bool { return true }

func (q *recordingQuota) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

// TestBrokerCancelledConcurrencyWaitDoesNotConsumeQuota proves the daily quota
// is consumed only after a concurrency slot is held.
func TestBrokerCancelledConcurrencyWaitDoesNotConsumeQuota(t *testing.T) {
	quota := &recordingQuota{}
	b := NewMemoryBroker(WithDailyQuota(quota))
	maxConc := 1
	req := Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", MaxConcurrency: &maxConc, DailyLimit: 2}

	held := acquire(t, b, req, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire err = %v, want deadline exceeded", err)
	}
	if got := quota.count(); got != 1 {
		t.Fatalf("daily consumes = %d, want 1 (the cancelled waiter must not spend a slot)", got)
	}

	held.Release()
	second := acquire(t, b, req, time.Second)
	second.Release()
	if got := quota.count(); got != 2 {
		t.Fatalf("daily consumes after release = %d, want 2", got)
	}
}

// TestBrokerCancelledRPSWaitDoesNotConsumeQuota proves an RPS cancellation
// releases the concurrency slot it was holding and does not spend a daily slot.
func TestBrokerCancelledRPSWaitDoesNotConsumeQuota(t *testing.T) {
	quota := &recordingQuota{}
	b := NewMemoryBroker(WithDailyQuota(quota))
	maxConc := 2
	rps := 0.01 // one request per 100 seconds
	req := Request{
		AccountID: "acc1", ProgramID: "p1", Interface: "http",
		MaxRPS: &rps, MaxConcurrency: &maxConc, DailyLimit: 2,
	}

	held := acquire(t, b, req, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire err = %v, want deadline exceeded", err)
	}
	if got := quota.count(); got != 1 {
		t.Fatalf("daily consumes = %d, want 1 (the cancelled waiter must not spend a slot)", got)
	}

	// The cancelled waiter must have released its concurrency slot. A future
	// Now makes the shared RPS timeline due, so a leak would surface as a
	// blocked third acquisition (held still occupies one of two slots).
	third := req
	third.Now = time.Now().Add(time.Hour)
	thirdCtx, thirdCancel := context.WithTimeout(context.Background(), time.Second)
	defer thirdCancel()
	thirdLease, err := b.Acquire(thirdCtx, third)
	if err != nil {
		t.Fatalf("third acquire after cancellation: %v", err)
	}
	thirdLease.Release()
	held.Release()
	if got := quota.count(); got != 2 {
		t.Fatalf("daily consumes = %d, want 2", got)
	}
}

func TestBrokerRespectsContextCancellation(t *testing.T) {
	b := NewMemoryBroker()
	maxConc := 1
	req := Request{AccountID: "acc1", ProgramID: "p1", Interface: "http", MaxConcurrency: &maxConc}
	held := acquire(t, b, req, time.Second)
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Acquire(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire err = %v, want context canceled", err)
	}
}

func TestBrokerUnidentifiedRequestIsNotShared(t *testing.T) {
	b := NewMemoryBroker()
	maxConc := 1
	// A grant without account/program identity cannot be attributed to a
	// platform object; it gets an ephemeral limiter rather than a global one.
	held := acquire(t, b, Request{Interface: "http", MaxConcurrency: &maxConc}, time.Second)
	defer held.Release()
	other := acquire(t, b, Request{Interface: "http", MaxConcurrency: &maxConc}, time.Second)
	other.Release()
}

func TestMemoryDailyQuotaCountsAndResets(t *testing.T) {
	q := NewMemoryDailyQuota()
	key := QuotaKey("acc", "prog", "http")
	ok, err := q.Consume(context.Background(), key, "2026-11-01", 2)
	if err != nil || !ok {
		t.Fatalf("first consume = %v, %v", ok, err)
	}
	ok, err = q.Consume(context.Background(), key, "2026-11-01", 2)
	if err != nil || !ok {
		t.Fatalf("second consume = %v, %v", ok, err)
	}
	ok, err = q.Consume(context.Background(), key, "2026-11-01", 2)
	if err != nil || ok {
		t.Fatalf("third consume = %v, %v; want limit reached", ok, err)
	}
	// A new business date resets the counter.
	ok, err = q.Consume(context.Background(), key, "2026-11-02", 2)
	if err != nil || !ok {
		t.Fatalf("next day consume = %v, %v", ok, err)
	}
	// A zero limit keeps counting.
	ok, err = q.Consume(context.Background(), QuotaKey("acc", "prog", "other"), "2026-11-01", 0)
	if err != nil || !ok {
		t.Fatalf("zero-limit consume = %v, %v", ok, err)
	}
}

func TestQuotaKeyRoundTrip(t *testing.T) {
	key := QuotaKey("acc/1", "prog-2", "https_connect")
	account, program, iface, err := SplitQuotaKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if account != "acc/1" || program != "prog-2" || iface != "https_connect" {
		t.Fatalf("split = %q/%q/%q", account, program, iface)
	}
	if _, _, _, err := SplitQuotaKey("no-separators"); err == nil {
		t.Fatal("malformed key must be rejected")
	}
}

type fakeUsageStore struct {
	account, program, iface, date string
	limit                         int64
	ok                            bool
	err                           error
}

func (s *fakeUsageStore) ConsumeNetworkUsage(_ context.Context, accountID, programID, interfaceName, businessDate string, limit int64) (bool, error) {
	s.account, s.program, s.iface, s.date, s.limit = accountID, programID, interfaceName, businessDate, limit
	return s.ok, s.err
}

func TestPostgresDailyQuotaDelegatesTypedColumns(t *testing.T) {
	store := &fakeUsageStore{ok: true}
	q := PostgresDailyQuota{Store: store}
	ok, err := q.Consume(context.Background(), QuotaKey("acc", "prog", "http"), "2026-11-01", 5)
	if err != nil || !ok {
		t.Fatalf("consume = %v, %v", ok, err)
	}
	if store.account != "acc" || store.program != "prog" || store.iface != "http" || store.date != "2026-11-01" || store.limit != 5 {
		t.Fatalf("delegated args = %+v", store)
	}

	if _, err := (PostgresDailyQuota{}).Consume(context.Background(), QuotaKey("a", "p", "http"), "2026-11-01", 1); err == nil {
		t.Fatal("a missing store must fail closed")
	}
	if _, err := q.Consume(context.Background(), "malformed", "2026-11-01", 1); err == nil {
		t.Fatal("a malformed key must be rejected")
	}
}
