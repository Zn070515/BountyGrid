package harness

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/capability"
)

type denyOnSecondCall struct {
	inner capability.GrantValidator
	calls atomic.Int64
}

func (v *denyOnSecondCall) ValidateGrant(ctx context.Context, g capability.ExecutionGrant) error {
	if v.calls.Add(1) >= 2 {
		return capability.ErrGrantRevoked
	}
	return v.inner.ValidateGrant(ctx, g)
}

// TestEgressRevalidatesGrantAfterQueue proves the post-acquire check: a grant
// that is valid before waiting for a broker lease but revoked by the time the
// slot is available must not result in an HTTP request.
func TestEgressRevalidatesGrantAfterQueue(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	inner, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))
	validator := &denyOnSecondCall{inner: inner}

	e := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}}).WithValidator(validator)
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.WithGrant(grant).Do(context.Background(), req); !errors.Is(err, capability.ErrGrantRevoked) {
		t.Fatalf("Egress err = %v, want ErrGrantRevoked", err)
	}
	if got := validator.calls.Load(); got != 2 {
		t.Fatalf("validator calls = %d, want 2 (before and after the broker wait)", got)
	}
}

type deadlineRecordingBroker struct {
	deadline time.Time
	has      bool
}

func (b *deadlineRecordingBroker) Acquire(ctx context.Context, _ capability.Request) (capability.Lease, error) {
	b.deadline, b.has = ctx.Deadline()
	return nil, errors.New("broker stop")
}

// TestEgressBoundsQueueWaitByLease proves the broker wait cannot outlive the
// grant's lease even when the caller's context has a later deadline.
func TestEgressBoundsQueueWaitByLease(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	validator, prog, rev := grantValidatorFixture(t, now)
	grant := validatorGrant(now, prog, epochOf(t, rev))
	// Ensure the lease is in the future for both the fixture clock and the
	// wall clock, so the broker wait is actually bounded.
	lease := time.Now().Add(time.Hour)
	if lease.Before(now.Add(time.Hour)) {
		lease = now.Add(time.Hour)
	}
	grant.LeaseUntil = lease
	grant.ExpiresAt = lease.Add(time.Hour)

	broker := &deadlineRecordingBroker{}
	e := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}}).
		WithValidator(validator).WithBroker(broker)
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if _, err := e.WithGrant(grant).Do(ctx, req); err == nil || err.Error() != "broker stop" {
		t.Fatalf("Egress err = %v, want the broker sentinel", err)
	}
	if !broker.has {
		t.Fatal("broker acquire received no deadline")
	}
	if !broker.deadline.Equal(grant.LeaseUntil) {
		t.Fatalf("broker deadline = %s, want lease %s", broker.deadline, grant.LeaseUntil)
	}
}

type allowThenDeny struct{ calls atomic.Int64 }

func (v *allowThenDeny) ValidateGrant(context.Context, capability.ExecutionGrant) error {
	if v.calls.Add(1) >= 2 {
		return capability.ErrGrantRevoked
	}
	return nil
}

// TestProxyRevalidatesGrantAfterQueue covers the proxy's authorize-then-queue
// window: the pre-queue Allow is not enough if the grant is revoked while the
// request waits for a capability slot.
func TestProxyRevalidatesGrantAfterQueue(t *testing.T) {
	var calls atomic.Int64
	upstream := countingUpstream(t, &calls)
	now := time.Now().UTC()
	grant := &ExecutionGrant{
		AccountID:      "acc1",
		ProgramID:      "p1",
		AssetTarget:    upstream.URL,
		ExpiresAt:      now.Add(time.Hour),
		LeaseUntil:     now.Add(time.Hour),
		AllowedMethods: map[string]struct{}{http.MethodGet: {}},
	}
	validator := &allowThenDeny{}
	policy := GrantPolicy{Grant: grant, Validator: validator, Now: func() time.Time { return now }}
	_, proxySrv := newTestProxy(t, ProxyConfig{
		Policy: policy, Audit: &recordingNetworkAudit{}, AllowLoopback: true,
	})
	client := testProxyClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 after the post-queue revocation", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
	if got := validator.calls.Load(); got != 2 {
		t.Fatalf("validator calls = %d, want 2", got)
	}
}
