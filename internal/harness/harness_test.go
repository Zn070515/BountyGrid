package harness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/capability"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
)

var testNow = time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)

type fakeGate struct {
	decision   domain.ActionDecision
	evalErr    error
	authErr    error
	evaluated  int
	authorized int
}

func (g *fakeGate) Evaluate(ctx context.Context, in gateway.EvaluateInput) (domain.ActionDecision, error) {
	g.evaluated++
	if g.evalErr != nil {
		return domain.ActionDecision{}, g.evalErr
	}
	d := g.decision
	d.Action = in.Request.Action
	d.RequestID = in.Request.ID
	d.ProgramID = in.Request.ProgramID
	return d, nil
}
func (g *fakeGate) AuthorizeWith(ctx context.Context, id domain.DecisionID, ai gateway.AuthorizeInput) error {
	g.authorized++
	return g.authErr
}

type fakeTool struct {
	spec     ToolSpec
	cost     domain.Money
	calls    int
	err      error
	evidence []domain.EvidenceID
}

func (t *fakeTool) Spec() ToolSpec     { return t.spec }
func (t *fakeTool) Cost() domain.Money { return t.cost }
func (t *fakeTool) Execute(ctx context.Context, tc ToolContext) (ToolResult, error) {
	t.calls++
	return ToolResult{Output: "ok", Evidence: t.evidence}, t.err
}

type fakeAuditor struct {
	events []audit.Event
	err    error
}

func (a *fakeAuditor) Append(ctx context.Context, e audit.Event) (int64, error) {
	if a.err != nil {
		return 0, a.err
	}
	a.events = append(a.events, e)
	return int64(len(a.events)), nil
}

func testRequest(action domain.Action) domain.ActionRequest {
	return domain.ActionRequest{
		ID: "r1", Actor: domain.ActorRef{Kind: domain.ActorHarness, ID: "h1"},
		AccountID: "acc1", ProgramID: "p1", Action: action, RequestedAt: testNow,
	}
}

func testHarness(tool *fakeTool, gate *fakeGate, aud *fakeAuditor, budget *Budget) *Harness {
	reg := NewRegistry()
	if err := reg.Register(tool); err != nil {
		panic(err)
	}
	h := New(reg, gate, aud, &Sandbox{}, budget)
	dir, err := os.MkdirTemp("", "bounty-harness-*")
	if err != nil {
		panic(err)
	}
	if err := h.SetWorkspace(dir); err != nil {
		panic(err)
	}
	h.SetClock(func() time.Time { return testNow })
	return h
}

func TestHarnessAllowsAndExecutes(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "view_rules", RequiredAction: domain.ActionViewRules}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	aud := &fakeAuditor{}
	h := testHarness(tool, gate, aud, &Budget{MaxCost: domain.Money{Amount: 100, Currency: "CNY"}})
	d, res, err := h.Execute(context.Background(), "view_rules", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != domain.DecisionAllow || res.Output != "ok" || tool.calls != 1 || gate.authorized != 1 {
		t.Fatalf("d=%s res=%+v calls=%d auth=%d", d.Decision, res, tool.calls, gate.authorized)
	}
	if len(aud.events) != 1 || aud.events[0].Type != "tool.executed" {
		t.Fatalf("audit = %+v", aud.events)
	}
}

func TestHarnessDenyNeverExecutes(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "view_rules", RequiredAction: domain.ActionViewRules}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionDeny}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	_, _, err := h.Execute(context.Background(), "view_rules", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow})
	var denied DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want DeniedError", err)
	}
	if tool.calls != 0 || gate.authorized != 0 {
		t.Fatalf("tool executed on DENY: calls=%d auth=%d", tool.calls, gate.authorized)
	}
}

func TestHarnessAuthorizeFailureNeverExecutes(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "view_rules", RequiredAction: domain.ActionViewRules}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}, authErr: errors.New("expired")}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	if _, _, err := h.Execute(context.Background(), "view_rules", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("authorize failure should surface")
	}
	if tool.calls != 0 {
		t.Fatal("tool executed after authorize failure")
	}
}

func TestHarnessRejectsActionMismatch(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "view_rules", RequiredAction: domain.ActionViewRules}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	if _, _, err := h.Execute(context.Background(), "view_rules", gateway.EvaluateInput{Request: testRequest(domain.ActionReportSubmit), Now: testNow}); err == nil {
		t.Fatal("action mismatch should be rejected")
	}
	if gate.evaluated != 0 || tool.calls != 0 {
		t.Fatal("mismatched request reached the gate/tool")
	}
}

func TestHarnessBudgetStop(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "costly", RequiredAction: domain.ActionViewRules}, cost: domain.Money{Amount: 10, Currency: "CNY"}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, &Budget{MaxCost: domain.Money{Amount: 1, Currency: "CNY"}})
	if _, _, err := h.Execute(context.Background(), "costly", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("budget limit should stop execution")
	}
	if tool.calls != 0 {
		t.Fatal("tool executed over budget")
	}
}

func TestHarnessDestructiveDisabledByDefault(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "danger", RequiredAction: domain.ActionViewRules, Destructive: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	if _, _, err := h.Execute(context.Background(), "danger", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("destructive tool should be disabled by default")
	}
	if gate.evaluated != 0 {
		t.Fatal("destructive tool reached the gate")
	}
}

func TestHarnessRequiresIdempotencyKeyForSideEffects(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	_, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow})
	if err == nil {
		t.Fatal("a side-effect tool without an idempotency key must be rejected")
	}
	if tool.calls != 0 || gate.evaluated != 0 {
		t.Fatal("the tool/gate ran before the idempotency check")
	}
}

func TestHarnessReplaysCompletedExecution(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-1"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	_, res, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("tool ran %d times, want 1 (replay must not execute)", tool.calls)
	}
	if res.Output != "ok" {
		t.Fatalf("replayed output = %q", res.Output)
	}
}

func TestHarnessCompletesAuditWithoutReexecuting(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	aud := &fakeAuditor{err: errors.New("audit down")}
	h := testHarness(tool, gate, aud, nil)
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-2"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err == nil {
		t.Fatal("audit failure should surface")
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	aud.err = nil
	if _, res, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err != nil {
		t.Fatal(err)
	} else if res.Output != "ok" {
		t.Fatalf("output = %q", res.Output)
	}
	if tool.calls != 1 {
		t.Fatalf("retry re-executed the tool: calls=%d", tool.calls)
	}
	if len(aud.events) != 1 || aud.events[0].Type != "tool.executed" {
		t.Fatalf("audit events = %+v", aud.events)
	}
}

func TestHarnessUnsettledReceiptBlocksRepeat(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	store := NewMemoryReceiptStore()
	if err := store.CreateReceipt(context.Background(), domain.ExecutionReceipt{
		ID: "rcpt/idem-3", IdempotencyKey: "idem-3", DecisionID: "d1",
		ToolName: "writer", Action: domain.ActionViewRules, ProgramID: "p1",
		Status: domain.ReceiptStarted, StartedAt: testNow, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	h.SetReceiptStore(store)
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-3"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err == nil {
		t.Fatal("an unsettled execution must not be repeated")
	}
	if tool.calls != 0 {
		t.Fatal("tool ran for an unsettled receipt")
	}
}

func TestHarnessRejectsKeyReusedForAnotherProgram(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-cross"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	other := req
	other.ProgramID = "p2"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: other, Now: testNow}); err == nil {
		t.Fatal("the same idempotency key must not replay another program's result")
	}
	if tool.calls != 1 {
		t.Fatalf("cross-program replay executed the tool: calls=%d", tool.calls)
	}
	if gate.evaluated != 1 {
		t.Fatalf("cross-program replay reached the gate: evaluated=%d", gate.evaluated)
	}
}

func TestHarnessReplayPreservesEvidence(t *testing.T) {
	ev := domain.EvidenceID("ev-replay")
	tool := &fakeTool{
		spec:     ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true},
		evidence: []domain.EvidenceID{ev},
	}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-evidence"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	_, res, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Evidence) != 1 || res.Evidence[0] != ev {
		t.Fatalf("replayed evidence = %v, want [%s]", res.Evidence, ev)
	}
}

type raceReceiptStore struct {
	base   *MemoryReceiptStore
	inner  domain.ExecutionReceipt
	seeded bool
}

func (s *raceReceiptStore) GetReceipt(ctx context.Context, key string) (domain.ExecutionReceipt, error) {
	if !s.seeded {
		return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
	}
	return s.base.GetReceipt(ctx, key)
}

func (s *raceReceiptStore) CreateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	_ = s.base.CreateReceipt(ctx, s.inner)
	s.seeded = true
	return domain.ErrReceiptExists
}

func (s *raceReceiptStore) UpdateReceipt(ctx context.Context, r domain.ExecutionReceipt) error {
	return s.base.UpdateReceipt(ctx, r)
}

func raceStore(inner domain.ExecutionReceipt) *raceReceiptStore {
	return &raceReceiptStore{base: NewMemoryReceiptStore(), inner: inner}
}

func TestHarnessReceiptRaceStartedRefusesExecution(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	h.SetReceiptStore(raceStore(domain.ExecutionReceipt{
		ID: "rcpt/idem-race", IdempotencyKey: "idem-race", DecisionID: "d1",
		ToolName: "writer", Action: domain.ActionViewRules, AccountID: "acc1", ProgramID: "p1",
		ParametersHash: domain.CanonicalParametersHash(nil),
		Status:         domain.ReceiptStarted, StartedAt: testNow, UpdatedAt: testNow,
	}))
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-race"
	if _, _, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow}); err == nil {
		t.Fatal("a STARTED receipt discovered during the create race must not report success")
	}
	if tool.calls != 0 {
		t.Fatalf("tool executed despite an unsettled receipt: calls=%d", tool.calls)
	}
}

func TestHarnessReceiptRaceCompletedReplays(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "writer", RequiredAction: domain.ActionViewRules, WritesFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil)
	h.SetReceiptStore(raceStore(domain.ExecutionReceipt{
		ID: "rcpt/idem-race2", IdempotencyKey: "idem-race2", DecisionID: "d1",
		ToolName: "writer", Action: domain.ActionViewRules, AccountID: "acc1", ProgramID: "p1",
		ParametersHash: domain.CanonicalParametersHash(nil), Output: "recorded",
		Status: domain.ReceiptCompleted, StartedAt: testNow, UpdatedAt: testNow,
	}))
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-race2"
	_, res, err := h.Execute(context.Background(), "writer", gateway.EvaluateInput{Request: req, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "recorded" || tool.calls != 0 {
		t.Fatalf("completed race replay = %q calls=%d", res.Output, tool.calls)
	}
}

func TestHarnessNetworkToolNeedsAllowlist(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "net", RequiredAction: domain.ActionViewRules, NetworkEgress: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	h := testHarness(tool, gate, &fakeAuditor{}, nil) // empty sandbox
	req := testRequest(domain.ActionViewRules)
	req.IdempotencyKey = "idem-net"
	if _, _, err := h.Execute(context.Background(), "net", gateway.EvaluateInput{Request: req, Now: testNow}); err == nil {
		t.Fatal("network tool without an allowlist must be rejected")
	}
	if tool.calls != 0 || gate.evaluated != 0 {
		t.Fatal("network tool ran without an allowlist")
	}
}

func TestEgressGuardsEveryRequest(t *testing.T) {
	e := NewEgress(&Sandbox{AllowedHosts: []string{"api.example.com"}})
	if err := e.CheckURL("https://api.example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckURL("https://evil.test/v1"); err == nil {
		t.Fatal("unlisted host must be denied")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://evil.test", nil)
	if _, err := e.Do(context.Background(), req); err == nil {
		t.Fatal("Do must apply the same check as CheckURL")
	}
}

func TestWorkspaceConfinesTraversal(t *testing.T) {
	dir := t.TempDir()
	ws, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	if err := ws.WriteFile("note.txt", []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ws.ReadFile("note.txt")
	if err != nil || string(got) != "ok" {
		t.Fatalf("read = %q %v", got, err)
	}
	if _, err := ws.ReadFile("../escape.txt"); err == nil {
		t.Fatal("workspace must reject paths outside the root")
	}
	if err := ws.WriteFile("../escape.txt", []byte("x"), 0o600); err == nil {
		t.Fatal("workspace must reject writes outside the root")
	}
}

func TestHarnessFileToolRequiresWorkspace(t *testing.T) {
	tool := &fakeTool{spec: ToolSpec{Name: "file", RequiredAction: domain.ActionViewRules, ReadsFiles: true}}
	gate := &fakeGate{decision: domain.ActionDecision{ID: "d1", Decision: domain.DecisionAllow}}
	reg := NewRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	h := New(reg, gate, &fakeAuditor{}, &Sandbox{}, nil)
	h.SetClock(func() time.Time { return testNow })
	if _, _, err := h.Execute(context.Background(), "file", gateway.EvaluateInput{Request: testRequest(domain.ActionViewRules), Now: testNow}); err == nil {
		t.Fatal("file tool without a workspace must be rejected")
	}
	if tool.calls != 0 || gate.evaluated != 0 {
		t.Fatal("file tool ran without a workspace")
	}
}

func TestEgressResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()
	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true})
	e.SetMaxBytes(4)
	if _, _, err := e.GetBytes(context.Background(), srv.URL); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize body err = %v, want ErrResponseTooLarge", err)
	}
	e.SetMaxBytes(10)
	body, status, err := e.GetBytes(context.Background(), srv.URL)
	if err != nil || status != 200 || len(body) != 10 {
		t.Fatalf("exact-size body = %q/%d/%v", body, status, err)
	}
}

func TestSandboxCheckURL(t *testing.T) {
	s := &Sandbox{AllowedHosts: []string{"api.example.com", "*.example.org"}}
	if err := s.CheckURL("https://api.example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckURL("https://a.example.org/x"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckURL("https://example.org/x"); err == nil {
		t.Fatal("wildcard must not allow the apex")
	}
	if err := s.CheckURL("https://evil.test/x"); err == nil {
		t.Fatal("unlisted host should be denied")
	}
	if err := s.CheckURL("file:///etc/passwd"); err == nil {
		t.Fatal("non-http scheme should be denied")
	}
}

func TestRegistryRejectsDuplicateAndEmpty(t *testing.T) {
	r := NewRegistry()
	tool := &fakeTool{spec: ToolSpec{Name: "t", RequiredAction: domain.ActionViewRules}}
	if err := r.Register(tool); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(tool); err == nil {
		t.Fatal("duplicate should fail")
	}
	if err := r.Register(&fakeTool{spec: ToolSpec{RequiredAction: domain.ActionViewRules}}); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := r.Register(&fakeTool{spec: ToolSpec{Name: "bad"}}); err == nil {
		t.Fatal("invalid action should fail")
	}
}

func TestBuiltinRegistry(t *testing.T) {
	r := NewBuiltinRegistry()
	for _, name := range []string{"view_rules", "read_scope", "report_draft"} {
		if _, err := r.Get(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func networkTestHarness(tool Tool, decision domain.ActionDecision) *Harness {
	reg := NewRegistry()
	if err := reg.Register(tool); err != nil {
		panic(err)
	}
	return New(reg, &fakeGate{decision: decision}, &fakeAuditor{},
		&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}, nil)
}

func allowDecisionFor(action domain.Action) domain.ActionDecision {
	exp := testNow.Add(time.Hour)
	return domain.ActionDecision{
		ID: "d1", Decision: domain.DecisionAllow, Action: action, ProgramID: "p1",
		DecidedAt: testNow, ExpiresAt: &exp,
	}
}

type failingNetworkTool struct {
	fakeTool
	err error
}

func (t *failingNetworkTool) Execute(ctx context.Context, tc ToolContext) (ToolResult, error) {
	t.calls++
	return ToolResult{}, t.err
}

func TestExecutionGrantEnforcesMethodAndTarget(t *testing.T) {
	e := NewEgress(&Sandbox{AllowedHosts: []string{"*.example.com"}, AllowLoopback: true})
	g := e.WithGrant(ExecutionGrant{
		AllowedMethods: map[string]struct{}{"GET": {}},
		AssetTarget:    "api.example.com",
	})
	postReq, _ := http.NewRequest(http.MethodPost, "https://api.example.com/x", nil)
	if _, err := g.Do(context.Background(), postReq); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("POST err = %v, want ErrGrantDenied", err)
	}
	otherReq, _ := http.NewRequest(http.MethodGet, "https://other.example.com/x", nil)
	if _, err := g.Do(context.Background(), otherReq); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("off-asset err = %v, want ErrGrantDenied", err)
	}
}

func TestDailyLimitSharedAcrossGrants(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	e := NewEgress(&Sandbox{AllowedHosts: []string{"127.0.0.1"}, AllowLoopback: true}).AllowUnvalidatedGrantsForTests()
	grant := ExecutionGrant{
		ProgramID:      "p1",
		AllowedMethods: map[string]struct{}{http.MethodGet: {}},
		DailyLimit:     1,
	}
	get := func(eg *Egress) error {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
		if err != nil {
			return err
		}
		resp, err := eg.Do(context.Background(), req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}

	// Two separate executions for the same program share one daily budget.
	if err := get(e.WithGrant(grant)); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if err := get(e.WithGrant(grant)); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("second grant err = %v, want ErrGrantDenied", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}

	// A different program keeps its own budget.
	other := grant
	other.ProgramID = "p2"
	if err := get(e.WithGrant(other)); err != nil {
		t.Fatalf("other program grant: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestRPSSharedAcrossGrants(t *testing.T) {
	e := NewEgress(&Sandbox{})
	rps := 0.01 // one request per 100 seconds: the first acquire holds the shared slot
	grant := ExecutionGrant{AccountID: "acc1", ProgramID: "p1", MaxRPS: &rps}

	first := e.WithGrant(grant)
	release, err := first.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release() // the RPS slot remains scheduled on the shared limiter

	// A second execution for the same account/program shares the RPS timeline.
	second := e.WithGrant(grant)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire err = %v, want deadline exceeded from the shared RPS slot", err)
	}

	// A different program gets a separate limiter and is not blocked. If the
	// second execution above had used a fresh per-execution limiter it would
	// also have acquired immediately (its schedule starts at now), so the
	// deadline above proves the limiter is genuinely shared.
	otherGrant := grant
	otherGrant.ProgramID = "p2"
	other := e.WithGrant(otherGrant)
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	releaseOther, err := other.acquire(otherCtx)
	if err != nil {
		t.Fatalf("other program acquire: %v", err)
	}
	releaseOther()
}

func TestConcurrencySharedAcrossGrants(t *testing.T) {
	e := NewEgress(&Sandbox{})
	maxConc := 1
	grant := ExecutionGrant{AccountID: "acc1", ProgramID: "p1", MaxConcurrency: &maxConc}

	first := e.WithGrant(grant)
	releaseFirst, err := first.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// The second execution shares the in-flight slot, so it must block.
	second := e.WithGrant(grant)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire err = %v, want deadline exceeded from the shared concurrency slot", err)
	}

	// A different program has its own limiter.
	otherGrant := grant
	otherGrant.ProgramID = "p2"
	other := e.WithGrant(otherGrant)
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	releaseOther, err := other.acquire(otherCtx)
	if err != nil {
		t.Fatalf("other program acquire: %v", err)
	}
	releaseOther()

	// Releasing the first slot unblocks the second grant.
	releaseFirst()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	releaseSecond, err := second.acquire(ctx2)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	releaseSecond()
}

func TestExecutionGrantPathUsesSegmentAwareMatching(t *testing.T) {
	const target = "https://api.example.com/api"
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"exact", "/api", true},
		{"descendant", "/api/users", true},
		{"prefix sibling apix", "/apix", false},
		{"prefix sibling api2", "/api2", false},
		{"prefix sibling api-admin", "/api-admin", false},
		{"encoded traversal", "/api/%2e%2e/admin", false},
		{"literal traversal", "/api/../admin", false},
		{"encoded slash traversal", "/api%2f..%2fadmin", false},
		{"backslash traversal", `/api\..\admin`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse("https://api.example.com" + tc.path)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.path, err)
			}
			g := ExecutionGrant{AssetTarget: target}
			err = g.AllowsTarget(u)
			if got := err == nil; got != tc.want {
				t.Fatalf("allowsTarget(%q) allowed=%v err=%v, want allowed=%v", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestExecutionGrantAllowedPathPrefixes(t *testing.T) {
	g := ExecutionGrant{AssetTarget: "api.example.com", AllowedPathPrefixes: []string{"/api"}}
	cases := []struct {
		path string
		want bool
	}{
		{"/api", true},
		{"/api/users", true},
		{"/apix", false},
		{"/api-admin", false},
		{"/api/%2e%2e/admin", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			u, err := url.Parse("https://api.example.com" + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			err = g.AllowsTarget(u)
			if got := err == nil; got != tc.want {
				t.Fatalf("allowsTarget(%q) allowed=%v err=%v, want allowed=%v", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestBuildGrantBindsAccountExpiryLeaseAndRevisions(t *testing.T) {
	exp := testNow.Add(time.Minute)
	decision := domain.ActionDecision{
		ID: "d1", ProgramID: "p1", ExpiresAt: &exp,
		ScopeRevision: "scope-r1", PolicyRevision: "policy-r1",
		Request: domain.ActionRequest{AccountID: "acc1"},
	}
	g := buildGrant(decision, domain.ActionRequest{AccountID: "fallback"}, testNow)
	if g.AccountID != "acc1" {
		t.Fatalf("grant account = %q, want acc1", g.AccountID)
	}
	if !g.ExpiresAt.Equal(exp) {
		t.Fatalf("grant expiry = %v, want %v", g.ExpiresAt, exp)
	}
	if g.ScopeRevision != "scope-r1" || g.PolicyRevision != "policy-r1" {
		t.Fatalf("grant revisions = %q/%q, want scope-r1/policy-r1", g.ScopeRevision, g.PolicyRevision)
	}
	// The default lease is one minute and is capped by the decision expiry.
	if !g.LeaseUntil.Equal(testNow.Add(capability.DefaultGrantLease)) {
		t.Fatalf("grant lease = %v, want %v", g.LeaseUntil, testNow.Add(capability.DefaultGrantLease))
	}

	decision.Request.AccountID = ""
	g = buildGrant(decision, domain.ActionRequest{AccountID: "fallback"}, testNow)
	if g.AccountID != "fallback" {
		t.Fatalf("grant account fallback = %q, want fallback", g.AccountID)
	}

	// A shorter decision expiry wins over the default lease.
	short := testNow.Add(10 * time.Second)
	decision.ExpiresAt = &short
	g = buildGrant(decision, domain.ActionRequest{AccountID: "fallback"}, testNow)
	if !g.LeaseUntil.Equal(short) {
		t.Fatalf("grant lease = %v, want decision expiry %v", g.LeaseUntil, short)
	}
}

func TestExecutionGrantAssetTargetHostPatterns(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"exact host", "example.com", "example.com", true},
		{"exact rejects subdomain", "example.com", "www.example.com", false},
		{"single wildcard matches subdomain", "*.example.com", "www.example.com", true},
		{"single wildcard rejects apex", "*.example.com", "example.com", false},
		{"double wildcard matches apex", "**.example.com", "example.com", true},
		{"double wildcard matches subdomain", "**.example.com", "www.example.com", true},
		{"double wildcard rejects other domain", "**.example.com", "example.org", false},
		{"trailing dot pattern", "example.com.", "example.com", true},
		{"trailing dot host", "example.com", "example.com.", true},
		{"case insensitive", "EXAMPLE.COM", "example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := ExecutionGrant{AssetTarget: tc.pattern}
			err := g.AllowsTarget(&url.URL{Scheme: "https", Host: tc.host, Path: "/"})
			if got := err == nil; got != tc.want {
				t.Fatalf("allowsTarget(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
			}
		})
	}
}

func TestPublicAddressFilter(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fc00::1", "0.0.0.0"} {
		if isPublicAddr(netip.MustParseAddr(host)) {
			t.Fatalf("%s must not be public", host)
		}
	}
	if !isPublicAddr(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("8.8.8.8 must be public")
	}
	if _, err := resolvePublicIP(context.Background(), "127.0.0.1", false); !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("loopback resolve err = %v", err)
	}
}

func TestNetworkErrorBecomesAmbiguousUnlessNotSent(t *testing.T) {
	tool := &failingNetworkTool{
		fakeTool: fakeTool{spec: ToolSpec{Name: "net", RequiredAction: domain.ActionActiveHTTP, NetworkEgress: true}},
		err:      errors.New("connection reset after write"),
	}
	h := networkTestHarness(tool, allowDecisionFor(domain.ActionActiveHTTP))
	in := gateway.EvaluateInput{Request: testRequest(domain.ActionActiveHTTP), Now: testNow}
	in.Request.IdempotencyKey = "idem-ambiguous"
	in.Request.Method = http.MethodGet
	if _, _, err := h.Execute(context.Background(), "net", in); err == nil {
		t.Fatal("expected tool error")
	}
	if _, _, err := h.Execute(context.Background(), "net", in); err == nil ||
		!strings.Contains(err.Error(), "reconcile manually") {
		t.Fatalf("ambiguous retry err = %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("ambiguous receipt was retried: calls=%d", tool.calls)
	}
}

func TestNotSentErrorIsRetryable(t *testing.T) {
	tool := &failingNetworkTool{
		fakeTool: fakeTool{spec: ToolSpec{Name: "net", RequiredAction: domain.ActionActiveHTTP, NetworkEgress: true}},
		err:      fmt.Errorf("dial failed: %w", domain.ErrNotSent),
	}
	h := networkTestHarness(tool, allowDecisionFor(domain.ActionActiveHTTP))
	in := gateway.EvaluateInput{Request: testRequest(domain.ActionActiveHTTP), Now: testNow}
	in.Request.IdempotencyKey = "idem-not-sent"
	in.Request.Method = http.MethodGet
	if _, _, err := h.Execute(context.Background(), "net", in); err == nil {
		t.Fatal("expected first failure")
	}
	// The second attempt may run again because the first proved nothing was sent.
	if _, _, err := h.Execute(context.Background(), "net", in); err == nil {
		t.Fatal("expected second failure")
	}
	if tool.calls != 2 {
		t.Fatalf("not-sent receipt was not retried: calls=%d", tool.calls)
	}
}

func TestBudgetReserveIsAtomic(t *testing.T) {
	b := &Budget{MaxRequests: 1}
	var ok int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := b.Reserve(domain.Money{}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
				r.Commit()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("reserved %d slots, want 1", ok)
	}
}
