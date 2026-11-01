package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func testProvider(name string, local bool, strength int, maxOut int, price int64, response string) *StaticProvider {
	return &StaticProvider{
		Meta: ProviderInfo{
			Name: name, Model: name + "-model", Local: local, Strength: strength,
			MaxOutputTokens: maxOut, InputPricePerMTok: price, OutputPricePerMTok: price,
		},
		Response: response,
	}
}

func testRequest(p Purpose, d DataClass, maxTokens int, budget int64) ModelRequest {
	return ModelRequest{
		TaskID: "task/p1/run-1", Purpose: p, DataClass: d, MaxTokens: maxTokens,
		Input:  []Message{{Role: "user", Content: "analyse this program"}},
		Budget: domain.Money{Amount: budget, Currency: "CNY"},
	}
}

func newTestRouter(t *testing.T, providers ...Provider) (*Router, *audit.MemoryStore) {
	t.Helper()
	aud := audit.NewMemoryStore()
	r, err := New(Options{Providers: providers, Auditor: aud, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return r, aud
}

func TestSensitiveDataNeverRoutesToRemote(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 2, 8000, 1, "local")
	r, _ := newTestRouter(t, remote, local)
	resp, err := r.Route(context.Background(), testRequest(PurposeAnalyze, DataSensitive, 1000, 100))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" || local.Calls != 1 || remote.Calls != 0 {
		t.Fatalf("provider = %s, local=%d remote=%d", resp.Provider, local.Calls, remote.Calls)
	}
}

func TestSensitiveWithoutLocalProviderFailsClosed(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	r, _ := newTestRouter(t, remote)
	if _, err := r.Route(context.Background(), testRequest(PurposeAnalyze, DataSensitive, 1000, 100)); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
	if remote.Calls != 0 {
		t.Fatal("remote provider was called for SENSITIVE data")
	}
}

func TestPurposePreference(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 3, 8000, 1, "local")
	r, _ := newTestRouter(t, local, remote)

	resp, err := r.Route(context.Background(), testRequest(PurposePlan, DataPublic, 1000, 100))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "remote" {
		t.Fatalf("PLAN provider = %s, want remote", resp.Provider)
	}

	resp, err = r.Route(context.Background(), testRequest(PurposeCode, DataPublic, 1000, 100))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" {
		t.Fatalf("CODE provider = %s, want local", resp.Provider)
	}
}

func TestBudgetRejectsBeforeCallingProvider(t *testing.T) {
	p := testProvider("expensive", true, 1, 8000, 1_000_000, "x") // 1 fen per token
	r, _ := newTestRouter(t, p)
	if _, err := r.Route(context.Background(), testRequest(PurposeCode, DataInternal, 1000, 1)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if p.Calls != 0 {
		t.Fatal("provider called despite a pre-call budget rejection")
	}
}

func TestFallbackAndAudit(t *testing.T) {
	broken := testProvider("broken", true, 1, 8000, 1, "")
	broken.Err = errors.New("provider down")
	good := testProvider("good", true, 1, 8000, 1, "ok")
	r, aud := newTestRouter(t, broken, good)

	resp, err := r.Route(context.Background(), testRequest(PurposeReport, DataInternal, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "good" || resp.Content != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	if len(r.CallLog().List()) != 2 {
		t.Fatalf("call log = %d, want 2 attempts", len(r.CallLog().List()))
	}
	events, err := aud.List(context.Background(), audit.Filter{Type: "model.call", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(events))
	}
}

func TestAuditContainsHashesNotPromptText(t *testing.T) {
	p := testProvider("local", true, 1, 8000, 1, "response")
	r, aud := newTestRouter(t, p)
	req := testRequest(PurposeAnalyze, DataInternal, 100, 100)
	req.Input = []Message{{Role: "user", Content: "SECRET-PROMPT-CONTENT"}}
	if _, err := r.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	events, err := aud.List(context.Background(), audit.Filter{Type: "model.call"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v %v", events, err)
	}
	payload := string(events[0].Payload)
	if strings.Contains(payload, "SECRET-PROMPT-CONTENT") {
		t.Fatal("audit payload leaked prompt content")
	}
	if !strings.Contains(payload, "prompt_hash") || !strings.Contains(payload, "response_hash") {
		t.Fatalf("audit payload lacks hashes: %s", payload)
	}
}

func TestProviderMaxOutputTokensFiltersCandidate(t *testing.T) {
	small := testProvider("small", true, 1, 100, 1, "x")
	r, _ := newTestRouter(t, small)
	if _, err := r.Route(context.Background(), testRequest(PurposeCode, DataInternal, 200, 100)); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
	if small.Calls != 0 {
		t.Fatal("provider with a lower output ceiling was called")
	}
}

func TestActualCostOverBudgetRejectsResponse(t *testing.T) {
	p := testProvider("local", true, 1, 8000, 1, "x")
	p.Usage = Usage{InputTokens: 10_000_000, OutputTokens: 10_000_000}
	r, _ := newTestRouter(t, p)
	// Estimate uses MaxTokens=1, so it fits; the provider reports much larger
	// usage than requested and the actual cost must be rejected.
	if _, err := r.Route(context.Background(), testRequest(PurposeCode, DataInternal, 1, 3)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
}

type failingAuditor struct{}

func (failingAuditor) Append(context.Context, audit.Event) (int64, error) {
	return 0, errors.New("audit down")
}

func TestAuditFailureSurfacesWithResponse(t *testing.T) {
	p := testProvider("local", true, 1, 8000, 1, "ok")
	r, err := New(Options{Providers: []Provider{p}, Auditor: failingAuditor{}, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.Route(context.Background(), testRequest(PurposeReport, DataInternal, 10, 100))
	if err == nil {
		t.Fatal("audit failure must surface")
	}
	if resp.Content != "ok" {
		t.Fatalf("response content = %q, want the completed call", resp.Content)
	}
}

func TestInvalidRequestAndDuplicateProvider(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("empty provider list = %v", err)
	}
	if _, err := New(Options{Providers: []Provider{testProvider("a", true, 1, 10, 1, "x"), testProvider("a", true, 1, 10, 1, "y")}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate provider = %v", err)
	}
	r, _ := newTestRouter(t, testProvider("a", true, 1, 100, 1, "x"))
	if _, err := r.Route(context.Background(), ModelRequest{Purpose: "NOPE", DataClass: DataPublic, MaxTokens: 1, Input: []Message{{Role: "user"}}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid purpose = %v", err)
	}
}

func BenchmarkRouterCandidates(b *testing.B) {
	providers := make([]Provider, 0, 100)
	for i := 0; i < 100; i++ {
		providers = append(providers, testProvider(fmt.Sprintf("p-%d", i), i%2 == 0, i%5, 16000, 1, "x"))
	}
	r, err := New(Options{Providers: providers, Now: func() time.Time { return testNow }})
	if err != nil {
		b.Fatal(err)
	}
	req := testRequest(PurposeAnalyze, DataInternal, 1000, 100)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = r.Candidates(req)
	}
}

func TestProvenanceRaisesDataClass(t *testing.T) {
	remote := testProvider("remote", false, 5, 100, 1, "remote")
	local := testProvider("local", true, 1, 100, 1, "local")
	r, _ := newTestRouter(t, remote, local)
	req := testRequest(PurposeAnalyze, DataPublic, 10, 0)
	req.Provenance = []DataClass{DataSensitive}
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" {
		t.Fatalf("sensitive provenance routed to %s", resp.Provider)
	}
}

func TestLocalClaimWithRemoteEndpointRejected(t *testing.T) {
	p := testProvider("fake-local", true, 1, 100, 1, "x")
	p.Meta.Endpoint = "https://api.example.com"
	if _, err := New(Options{Providers: []Provider{p}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestLoopbackEndpointIsLocal(t *testing.T) {
	p := testProvider("loopback", false, 1, 100, 1, "x")
	p.Meta.Endpoint = "http://127.0.0.1:8080"
	r, _ := newTestRouter(t, p, testProvider("remote", false, 5, 100, 1, "r"))
	req := testRequest(PurposeAnalyze, DataSensitive, 10, 0)
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "loopback" {
		t.Fatalf("loopback provider was not treated as local: %s", resp.Provider)
	}
}

func TestContextWindowAndCapabilitiesFilter(t *testing.T) {
	small := testProvider("small", true, 1, 100, 1, "s")
	small.Meta.MaxContextTokens = 10
	big := testProvider("big", true, 1, 10000, 1, "b")
	big.Meta.MaxContextTokens = 10000
	big.Meta.ToolCalling = true
	r, _ := newTestRouter(t, small, big)
	req := testRequest(PurposeCode, DataPublic, 100, 0)
	req.RequiredCapabilities = []Capability{CapabilityToolCalling}
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "big" {
		t.Fatalf("context/capability filter chose %s", resp.Provider)
	}
}

func TestFallbackChainBudgetIsCumulative(t *testing.T) {
	a := testProvider("a", true, 1, 100, 20000, "a")
	a.Err = errors.New("provider down")
	b := testProvider("b", true, 1, 100, 20000, "b")
	r, _ := newTestRouter(t, a, b)
	req := testRequest(PurposeCode, DataPublic, 100, 4) // each estimate ~3
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want cumulative ErrBudgetExceeded", err)
	}
}

func TestCallIDIdempotency(t *testing.T) {
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, _ := newTestRouter(t, p)
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-1"
	if _, err := r.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrDuplicateCall) {
		t.Fatalf("second call err = %v, want ErrDuplicateCall", err)
	}
	if p.Calls != 1 {
		t.Fatalf("provider called %d times", p.Calls)
	}
}

func TestInvalidProvenanceFailsClosed(t *testing.T) {
	req := testRequest(PurposeAnalyze, DataPublic, 10, 0)
	req.Provenance = []DataClass{"BOGUS"}
	if err := req.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
	if got := req.EffectiveDataClass(); got != DataSensitive {
		t.Fatalf("effective class = %q, want SENSITIVE", got)
	}
	r, _ := newTestRouter(t, testProvider("remote", false, 5, 100, 1, "x"))
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Route err = %v, want ErrInvalidRequest", err)
	}
}

func TestMaxDataClassCollapsesInvalidToSensitive(t *testing.T) {
	if got := MaxDataClass(DataPublic, DataClass("BOGUS")); got != DataSensitive {
		t.Fatalf("MaxDataClass = %q", got)
	}
	if got := MaxDataClass(DataClass("BOGUS"), DataClass("BOGUS")); got != DataSensitive {
		t.Fatalf("MaxDataClass invalid/invalid = %q", got)
	}
}

// oneShotProvider fails once (when Err is set) and succeeds afterwards. It
// models a transport that can prove ErrNotSent on the first attempt.
type oneShotProvider struct {
	Meta     ProviderInfo
	Err      error
	Response string
	Calls    int
}

func (p *oneShotProvider) Info() ProviderInfo { return p.Meta }

func (p *oneShotProvider) Complete(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	p.Calls++
	if p.Err != nil {
		err := p.Err
		p.Err = nil
		return ModelResponse{}, err
	}
	return ModelResponse{Content: p.Response, Model: p.Meta.Model, Provider: p.Meta.Name, Confidence: 1}, nil
}

// failingCompleteStore injects a persistence failure for one status transition.
type failingCompleteStore struct {
	*MemoryCallStore
	failStatus CallStatus
}

func (s *failingCompleteStore) Complete(ctx context.Context, id string, rec CallRecord) error {
	if rec.Status == s.failStatus {
		return errors.New("call store unavailable")
	}
	return s.MemoryCallStore.Complete(ctx, id, rec)
}

func callStatus(t *testing.T, store CallStore, id string) CallStatus {
	t.Helper()
	rec, ok, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("call %s not found", id)
	}
	return rec.Status
}

func TestCallStatusSuccessIsCompleted(t *testing.T) {
	store := NewMemoryCallStore()
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-completed"
	if _, err := r.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := callStatus(t, store, req.CallID); got != CallCompleted {
		t.Fatalf("status = %q, want COMPLETED", got)
	}
}

func TestCallStatusProviderErrorIsAmbiguous(t *testing.T) {
	store := NewMemoryCallStore()
	p := testProvider("p", true, 1, 100, 0, "ok")
	p.Err = errors.New("read timeout after send")
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-ambiguous"
	if _, err := r.Route(context.Background(), req); err == nil {
		t.Fatal("provider error must surface")
	}
	if got := callStatus(t, store, req.CallID); got != CallAmbiguous {
		t.Fatalf("status = %q, want AMBIGUOUS", got)
	}
	// A second attempt must not reach the provider again.
	p.Err = nil
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrCallAmbiguous) {
		t.Fatalf("retry err = %v, want ErrCallAmbiguous", err)
	}
	if p.Calls != 1 {
		t.Fatalf("provider called %d times, want 1", p.Calls)
	}
}

func TestCallStatusErrNotSentIsRetryable(t *testing.T) {
	store := NewMemoryCallStore()
	p := &oneShotProvider{
		Meta:     ProviderInfo{Name: "p", Model: "p-model", Local: true, MaxOutputTokens: 100},
		Err:      fmt.Errorf("dial failed: %w", domain.ErrNotSent),
		Response: "ok",
	}
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-retry"
	if _, err := r.Route(context.Background(), req); !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("first err = %v, want domain.ErrNotSent", err)
	}
	if got := callStatus(t, store, req.CallID); got != CallFailedBeforeSend {
		t.Fatalf("status = %q, want FAILED_BEFORE_SEND", got)
	}
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("retry after FAILED_BEFORE_SEND: %v", err)
	}
	if resp.Content != "ok" || p.Calls != 2 {
		t.Fatalf("retry resp=%+v calls=%d", resp, p.Calls)
	}
	if got := callStatus(t, store, req.CallID); got != CallCompleted {
		t.Fatalf("status after retry = %q, want COMPLETED", got)
	}
}

func TestCallStatusDuplicateCompletedRejected(t *testing.T) {
	store := NewMemoryCallStore()
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-duplicate"
	if _, err := r.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrDuplicateCall) {
		t.Fatalf("second call err = %v, want ErrDuplicateCall", err)
	}
	if p.Calls != 1 {
		t.Fatalf("provider called %d times, want 1", p.Calls)
	}
	if got := callStatus(t, store, req.CallID); got != CallCompleted {
		t.Fatalf("status = %q, want COMPLETED", got)
	}
}

func TestCallStatusStartWriteFailureSkipsProvider(t *testing.T) {
	store := &failingCompleteStore{MemoryCallStore: NewMemoryCallStore(), failStatus: CallStarted}
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-start-fail"
	if _, err := r.Route(context.Background(), req); err == nil {
		t.Fatal("STARTED persistence failure must surface")
	}
	if p.Calls != 0 {
		t.Fatalf("provider called %d times despite STARTED persistence failure", p.Calls)
	}
	if got := callStatus(t, store, req.CallID); got != CallReserved {
		t.Fatalf("status = %q, want RESERVED", got)
	}
}

func TestCallStatusCompleteWriteFailureSurfacesWithResponse(t *testing.T) {
	store := &failingCompleteStore{MemoryCallStore: NewMemoryCallStore(), failStatus: CallCompleted}
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, err := New(Options{Providers: []Provider{p}, CallStore: store, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	req.CallID = "call-complete-fail"
	resp, err := r.Route(context.Background(), req)
	if err == nil {
		t.Fatal("COMPLETED persistence failure must surface")
	}
	if resp.Content != "ok" {
		t.Fatalf("response content = %q, want the completed call", resp.Content)
	}
	if got := callStatus(t, store, req.CallID); got != CallStarted {
		t.Fatalf("status = %q, want STARTED (COMPLETED write failed)", got)
	}
}

func TestMemoryCallStoreReclaimOnlyFailedBeforeSend(t *testing.T) {
	store := NewMemoryCallStore()
	ctx := context.Background()
	if _, err := store.Reserve(ctx, "a", CallRecord{ID: "a", Status: CallStarted}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reclaim(ctx, "a"); err == nil {
		t.Fatal("reclaim of a STARTED call must fail")
	}
	if _, ok, _ := store.Get(ctx, "a"); !ok {
		t.Fatal("STARTED call was deleted by a refused reclaim")
	}
	if _, err := store.Reserve(ctx, "b", CallRecord{ID: "b", Status: CallFailedBeforeSend}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reclaim(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(ctx, "b"); ok {
		t.Fatal("FAILED_BEFORE_SEND call was not reclaimed")
	}
}
