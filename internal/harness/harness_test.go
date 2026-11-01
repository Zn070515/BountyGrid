package harness

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
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
