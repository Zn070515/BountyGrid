package harness

import (
	"context"
	"errors"
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
func (g *fakeGate) Authorize(ctx context.Context, id domain.DecisionID) error {
	g.authorized++
	return g.authErr
}

type fakeTool struct {
	spec  ToolSpec
	cost  domain.Money
	calls int
	err   error
}

func (t *fakeTool) Spec() ToolSpec     { return t.spec }
func (t *fakeTool) Cost() domain.Money { return t.cost }
func (t *fakeTool) Execute(ctx context.Context, tc ToolContext) (ToolResult, error) {
	t.calls++
	return ToolResult{Output: "ok"}, t.err
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
