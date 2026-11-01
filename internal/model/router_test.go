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

// testPolicyResolver is a test adapter that echoes the request hints. It is not
// a production authority: these tests exercise routing, not policy storage.
func testPolicyResolver() PolicyResolver {
	return PolicyResolverFunc(func(ctx context.Context, req ModelRequest) (ModelPolicy, error) {
		return ModelPolicy{
			Egress: req.ModelEgress, Redacted: req.Redacted, Escalation: req.Escalation,
		}, nil
	})
}

func newTestRouter(t *testing.T, providers ...Provider) (*Router, *audit.MemoryStore) {
	t.Helper()
	aud := audit.NewMemoryStore()
	r, err := New(Options{
		Providers: providers, Auditor: aud, PolicyResolver: testPolicyResolver(),
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, aud
}

func TestSensitiveDataNeverRoutesToRemote(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 2, 8000, 1, "local")
	r, _ := newTestRouter(t, remote, local)
	req := testRequest(PurposeAnalyze, DataSensitive, 1000, 100)
	req.ModelEgress = domain.ModelEgressRemoteAllowed
	resp, err := r.Route(context.Background(), req)
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
	req := testRequest(PurposeAnalyze, DataSensitive, 1000, 100)
	req.ModelEgress = domain.ModelEgressRemoteAllowed
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
	if remote.Calls != 0 {
		t.Fatal("remote provider was called for SENSITIVE data")
	}
}

func TestDefaultRouteIsLocalFirst(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	small := testProvider("local-small", true, 1, 8000, 1, "small")
	large := testProvider("local-large", true, 5, 16000, 1, "large")
	r, _ := newTestRouter(t, remote, large, small)

	for _, purpose := range []Purpose{PurposePlan, PurposeAnalyze, PurposeCode, PurposeReport, PurposeEmbed} {
		req := testRequest(purpose, DataPublic, 1000, 100)
		req.ModelEgress = domain.ModelEgressRemoteAllowed
		resp, err := r.Route(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", purpose, err)
		}
		// No escalation: the smallest local model is the default for every
		// purpose, even PLAN/ANALYZE.
		if resp.Provider != "local-small" {
			t.Fatalf("%s provider = %s, want local-small", purpose, resp.Provider)
		}
	}
	if remote.Calls != 0 {
		t.Fatalf("remote called %d times without an escalation", remote.Calls)
	}
}

func TestEscalationPrefersStrongerRemote(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	small := testProvider("local-small", true, 1, 8000, 1, "small")
	large := testProvider("local-large", true, 5, 16000, 1, "large")

	for _, reason := range []EscalationReason{
		EscalationStalled, EscalationLowConfidence, EscalationHighValueTask, EscalationVerificationConflict,
	} {
		r, _ := newTestRouter(t, small, large, remote)
		req := testRequest(PurposeAnalyze, DataPublic, 1000, 100)
		req.ModelEgress = domain.ModelEgressRemoteAllowed
		req.Escalation = reason
		resp, err := r.Route(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		if resp.Provider != "remote" {
			t.Fatalf("%s provider = %s, want remote", reason, resp.Provider)
		}
	}
}

func TestEscalationCannotBypassEgressPolicy(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 1, 8000, 1, "local")

	for _, policy := range []domain.ModelEgressPolicy{
		domain.ModelEgressLocalOnly, domain.ModelEgressUnknown, "",
	} {
		r, _ := newTestRouter(t, remote, local)
		req := testRequest(PurposeAnalyze, DataPublic, 1000, 100)
		req.ModelEgress = policy
		req.Escalation = EscalationStalled
		resp, err := r.Route(context.Background(), req)
		if err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
		if resp.Provider != "local" {
			t.Fatalf("policy %q provider = %s, want local", policy, resp.Provider)
		}
		if remote.Calls != 0 {
			t.Fatalf("policy %q called remote %d times", policy, remote.Calls)
		}
	}
}

func TestRemoteRedactedOnlyRequiresRedaction(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 1, 8000, 1, "local")
	r, _ := newTestRouter(t, remote, local)

	// Escalation proves the policy, not the local-first default, is what keeps
	// the remote provider out.
	req := testRequest(PurposeAnalyze, DataInternal, 1000, 100)
	req.ModelEgress = domain.ModelEgressRemoteRedactedOnly
	req.Escalation = EscalationStalled
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" {
		t.Fatalf("unredacted provider = %s, want local", resp.Provider)
	}

	req.Redacted = true
	resp, err = r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "remote" {
		t.Fatalf("redacted provider = %s, want remote", resp.Provider)
	}

	req.DataClass = DataSensitive
	resp, err = r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" {
		t.Fatalf("redacted SENSITIVE provider = %s, want local", resp.Provider)
	}
}

func TestModelEgressCandidates(t *testing.T) {
	remote := testProvider("remote", false, 9, 16000, 1, "remote")
	local := testProvider("local", true, 1, 8000, 1, "local")
	r, _ := newTestRouter(t, remote, local)

	cases := []struct {
		name       string
		policy     domain.ModelEgressPolicy
		class      DataClass
		redact     bool
		escalation EscalationReason
		want       []string
	}{
		{"unknown defaults local", domain.ModelEgressUnknown, DataPublic, false, "", []string{"local"}},
		{"empty defaults local", "", DataPublic, false, "", []string{"local"}},
		{"local only", domain.ModelEgressLocalOnly, DataPublic, false, "", []string{"local"}},
		{"remote allowed without escalation", domain.ModelEgressRemoteAllowed, DataInternal, false, "", []string{"local"}},
		{"redacted only without flag", domain.ModelEgressRemoteRedactedOnly, DataPublic, false, EscalationStalled, []string{"local"}},
		{"redacted only with flag", domain.ModelEgressRemoteRedactedOnly, DataPublic, true, EscalationStalled, []string{"remote", "local"}},
		{"redacted only sensitive", domain.ModelEgressRemoteRedactedOnly, DataSensitive, true, EscalationStalled, []string{"local"}},
		{"remote allowed escalated", domain.ModelEgressRemoteAllowed, DataInternal, false, EscalationStalled, []string{"remote", "local"}},
		{"remote allowed sensitive", domain.ModelEgressRemoteAllowed, DataSensitive, false, EscalationStalled, []string{"local"}},
	}
	for _, tc := range cases {
		req := testRequest(PurposeAnalyze, tc.class, 1000, 100)
		req.ModelEgress = tc.policy
		req.Redacted = tc.redact
		req.Escalation = tc.escalation
		var got []string
		for _, c := range r.Candidates(req) {
			got = append(got, c.Name)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: candidates = %v, want %v", tc.name, got, tc.want)
		}
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
	req.ModelEgress = domain.ModelEgressRemoteAllowed
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

func TestStableCallID(t *testing.T) {
	req := testRequest(PurposeAnalyze, DataInternal, 100, 0)
	req.TaskID = "task/p1/run-1"
	req.StepID = "step-2"
	prompt := PromptHash(req)

	id := StableCallID(req.TaskID, req.StepID, req.Purpose, prompt)
	if id == "" || !strings.HasPrefix(id, "call/stable/") {
		t.Fatalf("id = %q", id)
	}
	if again := StableCallID(req.TaskID, req.StepID, req.Purpose, prompt); again != id {
		t.Fatalf("stable id changed: %s != %s", again, id)
	}
	for _, changed := range []string{
		StableCallID("task/p1/run-2", req.StepID, req.Purpose, prompt),
		StableCallID(req.TaskID, "step-3", req.Purpose, prompt),
		StableCallID(req.TaskID, req.StepID, PurposeCode, prompt),
		StableCallID(req.TaskID, req.StepID, req.Purpose, "other-hash"),
	} {
		if changed == id {
			t.Fatalf("StableCallID did not vary with its inputs")
		}
	}
}

func TestWorkerModeRequiresStableCallID(t *testing.T) {
	p := testProvider("p", true, 1, 100, 0, "ok")
	r, err := New(Options{
		Providers: []Provider{p}, RequireStableCallID: true,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeCode, DataPublic, 10, 0)
	if _, err := r.Route(context.Background(), req); !errors.Is(err, ErrStableCallIDRequired) {
		t.Fatalf("err = %v, want ErrStableCallIDRequired", err)
	}
	if p.Calls != 0 {
		t.Fatalf("provider called %d times without a stable call ID", p.Calls)
	}
	req.StepID = "step-1"
	req.CallID = StableCallID(req.TaskID, req.StepID, req.Purpose, PromptHash(req))
	if _, err := r.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if p.Calls != 1 {
		t.Fatalf("provider called %d times, want 1", p.Calls)
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

func (s *failingCompleteStore) Complete(ctx context.Context, id string, rec ModelCall) error {
	if rec.FinalStatus == s.failStatus {
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
	return rec.FinalStatus
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

func TestAmbiguousThenSuccessSplitsLogicalCallAndAttempts(t *testing.T) {
	store := NewMemoryCallStore()
	ambiguous := testProvider("a-ambiguous", true, 1, 1000, 1_000_000, "a")
	ambiguous.Err = errors.New("read timeout after send")
	good := testProvider("b-good", true, 2, 1000, 1_000_000, "ok")
	r, err := New(Options{
		Providers: []Provider{ambiguous, good}, CallStore: store,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeAnalyze, DataInternal, 100, 0)
	req.CallID = "call-ambiguous-then-ok"
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "b-good" || resp.Content != "ok" {
		t.Fatalf("resp = %+v", resp)
	}

	call, found, err := store.Get(context.Background(), req.CallID)
	if err != nil || !found {
		t.Fatalf("logical call found=%v err=%v", found, err)
	}
	if call.FinalStatus != CallCompleted {
		t.Fatalf("final status = %q, want COMPLETED", call.FinalStatus)
	}
	if !call.HasAmbiguousAttempt {
		t.Fatal("logical call did not flag the earlier ambiguous attempt")
	}
	wantCost := EstimateCost(ambiguous.Info(), req).Amount + ActualCost(good.Info(), resp.Usage).Amount
	if call.TotalCost.Amount != wantCost {
		t.Fatalf("total cost = %d, want %d (ambiguous estimate + completed actual)", call.TotalCost.Amount, wantCost)
	}

	attempts := store.Attempts(req.CallID)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Status != CallAmbiguous || attempts[0].ActualCost.Amount != 0 ||
		attempts[0].EstimatedCost.Amount != EstimateCost(ambiguous.Info(), req).Amount {
		t.Fatalf("first attempt = %+v", attempts[0])
	}
	if attempts[1].Status != CallCompleted || attempts[1].ActualCost.Amount != ActualCost(good.Info(), resp.Usage).Amount {
		t.Fatalf("second attempt = %+v", attempts[1])
	}
	if attempts[0].AttemptID == attempts[1].AttemptID {
		t.Fatal("attempt IDs must be unique")
	}
	if got := len(r.CallLog().List()); got != 2 {
		t.Fatalf("call log = %d, want 2", got)
	}
}

func TestFailedBeforeSendAttemptCostsNothing(t *testing.T) {
	store := NewMemoryCallStore()
	notSent := testProvider("a-not-sent", true, 1, 1000, 1_000_000, "a")
	notSent.Err = fmt.Errorf("dial failed: %w", domain.ErrNotSent)
	good := testProvider("b-good", true, 2, 1000, 1_000_000, "ok")
	r, err := New(Options{
		Providers: []Provider{notSent, good}, CallStore: store,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(PurposeAnalyze, DataInternal, 100, 0)
	req.CallID = "call-not-sent-then-ok"
	resp, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	call, _, err := store.Get(context.Background(), req.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if call.HasAmbiguousAttempt {
		t.Fatal("FAILED_BEFORE_SEND attempt must not mark the call ambiguous")
	}
	want := ActualCost(good.Info(), resp.Usage).Amount
	if call.TotalCost.Amount != want {
		t.Fatalf("total cost = %d, want only the completed actual cost %d", call.TotalCost.Amount, want)
	}
	attempts := store.Attempts(req.CallID)
	if len(attempts) != 2 || attempts[0].Status != CallFailedBeforeSend || attempts[0].ActualCost.Amount != 0 {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestMemoryCallStoreReclaimOnlyFailedBeforeSend(t *testing.T) {
	store := NewMemoryCallStore()
	ctx := context.Background()
	if _, err := store.Reserve(ctx, "a", ModelCall{ID: "a", FinalStatus: CallStarted}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reclaim(ctx, "a"); err == nil {
		t.Fatal("reclaim of a STARTED call must fail")
	}
	if _, ok, _ := store.Get(ctx, "a"); !ok {
		t.Fatal("STARTED call was deleted by a refused reclaim")
	}
	if _, err := store.Reserve(ctx, "b", ModelCall{ID: "b", FinalStatus: CallFailedBeforeSend}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reclaim(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(ctx, "b"); ok {
		t.Fatal("FAILED_BEFORE_SEND call was not reclaimed")
	}
}
