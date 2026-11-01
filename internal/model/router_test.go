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
