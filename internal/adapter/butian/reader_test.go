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
)

type fakeBrowser struct {
	opened    []string
	evals     []string
	url       string
	responses map[string]string
	redirect  map[string]string
	openErr   error
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
	for _, k := range []string{"iframe", "slice(0,8)", "document.body"} {
		if v, ok := f.responses[k]; ok && strings.Contains(script, k) {
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
