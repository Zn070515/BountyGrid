package model

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func TestCallerHintsCannotWidenResolvedPolicy(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 1, 8000, 1, "local")
	r, err := New(Options{
		Providers: []Provider{remote, local},
		PolicyResolver: PolicyResolverFunc(func(context.Context, ModelRequest) (ModelPolicy, error) {
			return ModelPolicy{Egress: domain.ModelEgressLocalOnly}, nil
		}),
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeAnalyze, DataInternal, 1000, 100)
	req.ModelEgress = domain.ModelEgressRemoteAllowed
	req.Redacted = true
	req.Escalation = EscalationStalled
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" || remote.Calls != 0 {
		t.Fatalf("provider = %s remote_calls = %d, want local/0", resp.Provider, remote.Calls)
	}
}

func TestNoEscalationNeverFallsBackToThirdParty(t *testing.T) {
	local := testProvider("local", true, 1, 8000, 1, "local")
	local.Err = errors.New("local model crashed")
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	r, _ := newTestRouter(t, local, remote)
	req := testRequest(PurposeAnalyze, DataInternal, 1000, 100)
	req.ModelEgress = domain.ModelEgressRemoteAllowed
	// No escalation: remote must not be an implicit fallback.
	if _, err := r.Route(context.Background(), req); err == nil {
		t.Fatal("local failure must surface")
	}
	if remote.Calls != 0 || local.Calls != 1 {
		t.Fatalf("local=%d remote=%d, want 1/0", local.Calls, remote.Calls)
	}
}

func TestPolicyResolverErrorFailsClosed(t *testing.T) {
	boom := errors.New("policy store unavailable")
	local := testProvider("local", true, 1, 8000, 1, "local")
	r, err := New(Options{
		Providers: []Provider{local},
		PolicyResolver: PolicyResolverFunc(func(context.Context, ModelRequest) (ModelPolicy, error) {
			return ModelPolicy{}, boom
		}),
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataInternal, 100, 0)
	if _, err := r.Route(context.Background(), req); !errors.Is(err, boom) {
		t.Fatalf("route err = %v, want resolver error", err)
	}
	if local.Calls != 0 {
		t.Fatalf("provider called %d times on resolver failure", local.Calls)
	}
	if got := r.Candidates(req); len(got) != 0 {
		t.Fatalf("candidates = %v, want none on resolver failure", got)
	}
}

func TestSensitiveDataNeverRoutesToLocalNetwork(t *testing.T) {
	lan := &StaticProvider{Meta: ProviderInfo{
		Name: "lan", Model: "lan-model", Trust: TrustLocalNetwork,
		Endpoint: "http://10.0.0.5:8000", MaxOutputTokens: 8000, Strength: 5,
	}}
	r, err := New(Options{
		Providers:       []Provider{lan},
		TrustedNetworks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")},
		Now:             func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeAnalyze, DataSensitive, 1000, 100)
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("SENSITIVE route err = %v, want ErrNoProvider", err)
	}
	if lan.Calls != 0 {
		t.Fatalf("LAN provider called %d times for SENSITIVE data", lan.Calls)
	}

	req.DataClass = DataInternal
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "lan" || lan.Calls != 1 {
		t.Fatalf("provider = %s calls = %d, want lan/1", resp.Provider, lan.Calls)
	}
}

func TestLocalNetworkProviderRequiresTrustedCIDR(t *testing.T) {
	lan := func(endpoint string) Provider {
		return &StaticProvider{Meta: ProviderInfo{
			Name: "lan", Model: "lan-model", Trust: TrustLocalNetwork,
			Endpoint: endpoint, MaxOutputTokens: 100,
		}}
	}
	cases := []struct {
		name    string
		info    Provider
		trusted []netip.Prefix
		wantErr bool
	}{
		{"no trusted networks", lan("http://10.0.0.5:8000"), nil, true},
		{"outside trusted networks", lan("http://10.0.0.5:8000"),
			[]netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}, true},
		{"inside trusted networks", lan("http://10.0.0.5:8000"),
			[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, false},
		{"hostname not accepted", lan("http://model.lan:8000"),
			[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, true},
		{"in-process with public endpoint", &StaticProvider{Meta: ProviderInfo{
			Name: "bad", Model: "m", Trust: TrustInProcess, Endpoint: "http://10.0.0.5:8000",
			MaxOutputTokens: 100,
		}}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{Providers: []Provider{tc.info}, TrustedNetworks: tc.trusted})
			if tc.wantErr && err == nil {
				t.Fatal("configuration was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("configuration rejected: %v", err)
			}
		})
	}
}

// blockingProvider signals when a call has entered Complete and waits for the
// test to release it. It always proves ErrNotSent.
type blockingProvider struct {
	Meta     ProviderInfo
	calls    atomic.Int64
	entered  chan struct{}
	releases chan chan struct{}
}

func (p *blockingProvider) Info() ProviderInfo { return p.Meta }

func (p *blockingProvider) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	p.calls.Add(1)
	release := make(chan struct{})
	p.entered <- struct{}{}
	p.releases <- release
	<-release
	return ModelResponse{}, fmt.Errorf("dial failed: %w", domain.ErrNotSent)
}

func TestConcurrentRetryCannotReclaimMidChainCall(t *testing.T) {
	store := NewMemoryCallStore()
	p := &blockingProvider{
		Meta:     ProviderInfo{Name: "p", Model: "p-model", Local: true, MaxOutputTokens: 100},
		entered:  make(chan struct{}, 1),
		releases: make(chan chan struct{}, 1),
	}
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-concurrent"

	first := make(chan error, 1)
	go func() {
		_, err := r.Route(context.Background(), req)
		first <- err
	}()
	<-p.entered

	// While the chain is in flight, the logical call must be STARTED, never
	// FAILED_BEFORE_SEND, and a concurrent retry must be refused without
	// reaching the provider again.
	if got := callStatus(t, store, req.CallID); got != CallStarted {
		t.Fatalf("mid-chain status = %q, want STARTED", got)
	}
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrCallAmbiguous) {
		t.Fatalf("concurrent retry err = %v, want ErrCallAmbiguous", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.calls.Load())
	}

	close(<-p.releases)
	if err := <-first; !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("first route err = %v, want domain.ErrNotSent", err)
	}
	if got := callStatus(t, store, req.CallID); got != CallFailedBeforeSend {
		t.Fatalf("terminal status = %q, want FAILED_BEFORE_SEND", got)
	}

	// Only after the terminal FAILED_BEFORE_SEND may a retry reclaim the call.
	retry := make(chan error, 1)
	go func() {
		_, err := r.Route(context.Background(), req)
		retry <- err
	}()
	<-p.entered
	close(<-p.releases)
	if err := <-retry; !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("retry err = %v, want domain.ErrNotSent", err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("provider calls after retry = %d, want 2", p.calls.Load())
	}
}

func TestMemoryCallStoreRejectsTerminalOverwrite(t *testing.T) {
	store := NewMemoryCallStore()
	ctx := context.Background()
	if _, err := store.Reserve(ctx, "c", ModelCall{ID: "c", FinalStatus: CallReserved}); err != nil {
		t.Fatal(err)
	}
	completed := ModelCall{ID: "c", FinalStatus: CallCompleted}
	if err := store.Complete(ctx, "c", completed); err != nil {
		t.Fatal(err)
	}
	for _, status := range []CallStatus{CallAmbiguous, CallFailedBeforeSend, CallCompleted} {
		rec := ModelCall{ID: "c", FinalStatus: status}
		if err := store.Complete(ctx, "c", rec); !errors.Is(err, ErrInvalidCallTransition) {
			t.Fatalf("terminal -> %s err = %v, want ErrInvalidCallTransition", status, err)
		}
	}
	if got := callStatus(t, store, "c"); got != CallCompleted {
		t.Fatalf("status = %q, want the recorded COMPLETED", got)
	}
}
