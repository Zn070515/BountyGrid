package butian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

type fakeBrowser struct {
	opened    []string
	evals     []string
	url       string
	responses map[string]string
	// byURL overrides responses for the page currently open (Open sets url).
	byURL    map[string]map[string]string
	redirect map[string]string
	openErr  error
}

func (f *fakeBrowser) Open(ctx context.Context, url string) error {
	f.opened = append(f.opened, url)
	f.url = url
	if target, ok := f.redirect[url]; ok {
		f.url = target
	}
	return f.openErr
}
func (f *fakeBrowser) Reload(ctx context.Context) error           { return nil }
func (f *fakeBrowser) WaitLoad(ctx context.Context) error         { return nil }
func (f *fakeBrowser) WaitFn(ctx context.Context, _ string) error { return nil }
func (f *fakeBrowser) Eval(ctx context.Context, script string) ([]byte, error) {
	f.evals = append(f.evals, script)
	for _, k := range []string{"p=1", "p=2", "fetch(", "iframe", "slice(0,8)", "document.body", "querySelectorAll"} {
		if !strings.Contains(script, k) {
			continue
		}
		if m, ok := f.byURL[f.url]; ok {
			if v, ok := m[k]; ok {
				return []byte(v), nil
			}
		}
		if v, ok := f.responses[k]; ok {
			return []byte(v), nil
		}
	}
	return nil, fmt.Errorf("unexpected eval: %s", script)
}
func (f *fakeBrowser) GetURL(ctx context.Context) (string, error)   { return f.url, nil }
func (f *fakeBrowser) GetTitle(ctx context.Context) (string, error) { return "", nil }

func evalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s, err := json.Marshal(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return string(s)
}

func newTestReader(t *testing.T, b Browser) *Reader {
	t.Helper()
	r, err := NewReader(b, ReaderOptions{RawDir: t.TempDir(), Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSyncProgramsButian(t *testing.T) {
	f := &fakeBrowser{
		url: projectHallURL,
		responses: map[string]string{
			"slice(0,8)": evalJSON(t, srcChunk{Cards: []srcCard{sampleCard()}, Total: 1}),
		},
	}
	r := newTestReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 1 || batch.Programs[0].Name != "理想汽车" {
		t.Fatalf("programs = %+v", batch.Programs)
	}
	if batch.EstimatedTotal != 1 || batch.Estimated {
		t.Fatalf("estimate = %v/%v", batch.EstimatedTotal, batch.Estimated)
	}
	if len(batch.Evidence) != 1 || len(batch.Facts) == 0 {
		t.Fatalf("evidence/facts = %d/%d", len(batch.Evidence), len(batch.Facts))
	}
}

func TestSyncProgramsButianCaptcha(t *testing.T) {
	f := &fakeBrowser{responses: map[string]string{
		"slice(0,8)": evalJSON(t, srcChunk{Cards: nil, Total: 0, NoData: true}),
	}}
	r := newTestReader(t, f)
	if _, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{}); !errors.Is(err, adapter.ErrGated) {
		t.Fatalf("err = %v, want ErrGated", err)
	}
}

func TestSyncAccountButianConservative(t *testing.T) {
	center := "实习白帽 LV1\n未完成实战认证\n500 荣誉币\n0 积分\n实名认证 已认证\n银行卡未绑定"
	submit := "提交漏洞 合规考核未完成，请先完成本季度合规考核"
	f := &fakeBrowser{responses: map[string]string{
		"iframe":        evalJSON(t, center),
		"document.body": evalJSON(t, submit),
	}}
	r := newTestReader(t, f)
	snap, err := r.SyncAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Account.Capability.CanSubmit {
		t.Fatalf("CanSubmit must be false with incomplete compliance: %+v", snap.Account.Capability)
	}
	if snap.Account.PracticalCertification == nil || *snap.Account.PracticalCertification {
		t.Fatalf("practical = %v", snap.Account.PracticalCertification)
	}
	if snap.Account.IdentityVerified == nil || !*snap.Account.IdentityVerified {
		t.Fatalf("identity = %v", snap.Account.IdentityVerified)
	}
	if len(snap.Facts) == 0 || len(snap.Evidence) != 2 {
		t.Fatalf("facts/evidence = %d/%d", len(snap.Facts), len(snap.Evidence))
	}
	if err := snap.Account.Validate(); err != nil {
		t.Fatalf("account invalid: %v", err)
	}
}

func TestSyncAccountButianUnknownComplianceFailClosed(t *testing.T) {
	center := "实习白帽 LV1\n未完成实战认证\n实名认证 已认证"
	submit := "提交漏洞"
	f := &fakeBrowser{responses: map[string]string{
		"iframe":        evalJSON(t, center),
		"document.body": evalJSON(t, submit),
	}}
	r := newTestReader(t, f)
	snap, err := r.SyncAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Account.Capability.CanSubmit {
		t.Fatal("unknown compliance must fail closed")
	}
}

func TestSyncProgramNativeNameUsesStoredID(t *testing.T) {
	f := &fakeBrowser{
		url: projectHallURL,
		responses: map[string]string{
			"slice(0,8)":    evalJSON(t, srcChunk{Cards: []srcCard{sampleCard()}, Total: 1}),
			"document.body": evalJSON(t, "报名后可见"),
		},
	}
	r := newTestReader(t, f)
	snap, err := r.SyncProgram(context.Background(), "butian/理想汽车")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(snap.Program.ID), "butian/src/") {
		t.Fatalf("program id = %s", snap.Program.ID)
	}
	if snap.Program.NativeID != "理想汽车" {
		t.Fatalf("native id = %s", snap.Program.NativeID)
	}
	for _, fact := range snap.Facts {
		if fact.ProgramID == nil || *fact.ProgramID != snap.Program.ID {
			t.Fatalf("fact stored under wrong program: %+v (want %s)", fact.ProgramID, snap.Program.ID)
		}
	}
}

func TestSyncPublicWelfareButian(t *testing.T) {
	page1 := `{"status":1,"info":"ok","data":{"count":2,"current":1,"list":[{"company_id":"65657","company_name":"甲公司"},{"company_id":"65285","company_name":"乙公司"}]}}`
	f := &fakeBrowser{
		// Not on butian.net yet: the reader must open a public origin first.
		url: "",
		responses: map[string]string{
			"fetch(": evalJSON(t, map[string]any{
				"status": 200, "content_type": "application/json", "body": page1,
			}),
		},
	}
	r := newTestReader(t, f)
	batch, err := r.SyncPublicWelfare(context.Background(), adapter.SyncOptions{MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 2 {
		t.Fatalf("programs = %d", len(batch.Programs))
	}
	first := batch.Programs[0]
	if first.Type != domain.TypePublicWelfareSRC || first.NativeID != "65657" || first.Name != "甲公司" {
		t.Fatalf("program = %+v", first)
	}
	if first.Status.Status != domain.StatusRunning || first.Status.Confidence > 0.8 {
		t.Fatalf("status = %+v", first.Status)
	}
	if first.Scope.ContentsKnown || first.Scope.Visibility != domain.ScopeUnknown || first.Scope.Access != domain.ScopeAccessUnknown {
		t.Fatalf("scope must stay unknown: %+v", first.Scope)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("invalid program: %v", err)
	}
	// The API's count is total pages (2). Only page 1 was read, so the total
	// is an upper bound of pages x page size.
	if batch.ObservedCount != 2 || batch.EstimatedTotal != 4 || !batch.Estimated {
		t.Fatalf("counts = %+v", batch)
	}
	if len(batch.Evidence) != 1 || len(batch.Facts) == 0 {
		t.Fatalf("evidence/facts = %d/%d", len(batch.Evidence), len(batch.Facts))
	}
	for _, fact := range batch.Facts {
		if len(fact.EvidenceRefs) == 0 {
			t.Fatalf("fact without evidence: %+v", fact)
		}
	}
	if f.opened[len(f.opened)-1] != homeURL {
		t.Fatalf("opened = %v", f.opened)
	}
}

func TestSyncPublicWelfareExactTotalOnLastPage(t *testing.T) {
	page1 := `{"status":1,"info":"ok","data":{"count":2,"current":1,"list":[{"company_id":"1","company_name":"甲"}]}}`
	page2 := `{"status":1,"info":"ok","data":{"count":2,"current":2,"list":[{"company_id":"2","company_name":"乙"}]}}`
	f := &fakeBrowser{
		url: homeURL,
		responses: map[string]string{
			"p=1": evalJSON(t, map[string]any{"status": 200, "content_type": "application/json", "body": page1}),
			"p=2": evalJSON(t, map[string]any{"status": 200, "content_type": "application/json", "body": page2}),
		},
	}
	r := newTestReader(t, f)
	batch, err := r.SyncPublicWelfare(context.Background(), adapter.SyncOptions{MaxPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 2 || batch.Estimated || batch.EstimatedTotal != 2 {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestSyncPublicWelfareRejectsHTMLShell(t *testing.T) {
	f := &fakeBrowser{
		url: homeURL,
		responses: map[string]string{
			"fetch(": evalJSON(t, map[string]any{
				"status": 200, "content_type": "text/html; charset=utf-8", "body": "<!DOCTYPE html><html></html>",
			}),
		},
	}
	r := newTestReader(t, f)
	_, err := r.SyncPublicWelfare(context.Background(), adapter.SyncOptions{})
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestSyncGrowButian(t *testing.T) {
	rules := "欢迎回来\n报名时间：2026.8.25~2026.11.30\n活动时间：2026.9.1~2026.11.30\n" +
		"尚未加入校园团队 · 学生认证待完成\n立即报名"
	active := "2026中秋活动 2026/08/20 - 2026/09/03\n" +
		"2026GROW计划漏洞挖掘实战-第二期 已结束 2026/10/01 - 2026/10/31"
	f := &fakeBrowser{
		url: growURL,
		byURL: map[string]map[string]string{
			growURL: {
				"document.body":    evalJSON(t, rules),
				"querySelectorAll": evalJSON(t, map[string]bool{"clicked": true}),
			},
			activeCenterURL: {
				"document.body": evalJSON(t, active),
			},
		},
	}
	r := newTestReader(t, f)
	snap, err := r.SyncGrow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prog := snap.Program
	if prog.ID != "butian/grow" || prog.Type != domain.TypeCampusProgram {
		t.Fatalf("program = %+v", prog)
	}
	if prog.Status.Status != domain.StatusRunning {
		t.Fatalf("status = %+v", prog.Status)
	}
	if prog.Scope.Visibility != domain.ScopeGatedBySignup || prog.Scope.Access != domain.ScopeAccessNotGranted || prog.Scope.ContentsKnown {
		t.Fatalf("scope = %+v", prog.Scope)
	}
	kinds := map[domain.GateKind]bool{}
	for _, g := range prog.Eligibility.Gates {
		kinds[g.Kind] = true
		if len(g.Evidence) == 0 {
			t.Fatalf("gate without evidence: %+v", g)
		}
	}
	for _, want := range []domain.GateKind{domain.GateProgramSignup, domain.GateStudentVerification, domain.GateTeamMembership} {
		if !kinds[want] {
			t.Fatalf("missing gate %s: %+v", want, prog.Eligibility.Gates)
		}
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("invalid program: %v", err)
	}
	if len(snap.Evidence) != 2 || len(prog.Evidence) != 2 {
		t.Fatalf("evidence = %d/%d", len(snap.Evidence), len(prog.Evidence))
	}
	evs := map[domain.EvidenceID]bool{snap.Evidence[0].ID: true, snap.Evidence[1].ID: true}
	preds := map[string]bool{}
	for _, fact := range snap.Facts {
		preds[fact.Predicate] = true
		for _, ev := range fact.EvidenceRefs {
			if !evs[ev] {
				t.Fatalf("fact cites unknown evidence %s", ev)
			}
		}
	}
	for _, want := range []string{
		facts.PredRegistrationStart, facts.PredRegistrationEnd,
		facts.PredActivityStart, facts.PredActivityEnd,
		facts.PredPhaseStart, facts.PredPhaseEnd,
	} {
		if !preds[want] {
			t.Fatalf("missing fact %s: %v", want, preds)
		}
	}
}

func TestSyncGrowButianRejectsMissingRules(t *testing.T) {
	f := &fakeBrowser{
		url: growURL,
		byURL: map[string]map[string]string{
			growURL: {
				"document.body":    evalJSON(t, "欢迎回来\n立即报名"),
				"querySelectorAll": evalJSON(t, map[string]bool{"clicked": true}),
			},
			activeCenterURL: {"document.body": evalJSON(t, "没有活动")},
		},
	}
	r := newTestReader(t, f)
	if _, err := r.SyncGrow(context.Background()); err == nil {
		t.Fatal("expected an error when the rules modal has no dates")
	}
}

func TestAuthStatusButian(t *testing.T) {
	f := &fakeBrowser{url: centerURL}
	r := newTestReader(t, f)
	state, err := r.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthOK {
		t.Fatalf("state = %s err=%v", state, err)
	}
	f2 := &fakeBrowser{redirect: map[string]string{centerURL: "https://user.skyeye.qianxin.com/user/sign-in"}}
	r2 := newTestReader(t, f2)
	state, err = r2.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthRequired {
		t.Fatalf("state = %s err=%v", state, err)
	}
}

func TestSyncScopeButianGated(t *testing.T) {
	f := &fakeBrowser{url: projectHallURL, responses: map[string]string{
		"document.body": evalJSON(t, "报名后可见"),
	}}
	r := newTestReader(t, f)
	snap, err := r.SyncScope(context.Background(), "butian/src/abc")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Scope.Visibility != domain.ScopeGatedBySignup || snap.Scope.Access != domain.ScopeAccessNotGranted {
		t.Fatalf("scope = %+v", snap.Scope)
	}
}
