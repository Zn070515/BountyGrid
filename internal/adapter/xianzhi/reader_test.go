package xianzhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/sidecar"
)

type fakeBrowser struct {
	opened    []string
	evals     []string
	url       string
	pageText  map[string]string
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

func (f *fakeBrowser) WaitLoad(ctx context.Context) error            { return nil }
func (f *fakeBrowser) WaitFn(ctx context.Context, expr string) error { return nil }

func (f *fakeBrowser) Eval(ctx context.Context, script string) ([]byte, error) {
	f.evals = append(f.evals, script)
	if strings.Contains(script, "document.body") {
		if out, ok := f.pageText[f.url]; ok {
			return []byte(out), nil
		}
	}
	for k, v := range f.responses {
		if strings.Contains(script, k) {
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

func chunkResponse(t *testing.T, cards []taskCard, maxPage int) string {
	return evalJSON(t, taskChunk{Cards: cards, MaxPage: maxPage})
}

func TestSyncProgramsXianzhi(t *testing.T) {
	f := &fakeBrowser{responses: map[string]string{
		"slice(0,10)":  chunkResponse(t, []taskCard{sampleTask()}, 5),
		"slice(10,20)": chunkResponse(t, nil, 5),
	}}
	r := newTestReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 1 || batch.Programs[0].ID != "xianzhi/324" {
		t.Fatalf("programs = %+v", batch.Programs)
	}
	if len(batch.Evidence) != 1 || len(batch.Facts) == 0 {
		t.Fatalf("evidence/facts = %d/%d", len(batch.Evidence), len(batch.Facts))
	}
	if !batch.Estimated || batch.EstimatedTotal != 100 {
		t.Fatalf("estimate = %v/%v, want estimated 100 (5 pages*20)", batch.EstimatedTotal, batch.Estimated)
	}
	raw, err := os.ReadFile(batch.Evidence[0].RawRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "324") {
		t.Fatalf("raw evidence missing task id: %s", string(raw)[:200])
	}
}

func TestSyncProgramsAllPagesExact(t *testing.T) {
	f := &fakeBrowser{responses: map[string]string{
		"slice(0,10)":  chunkResponse(t, []taskCard{sampleTask()}, 1),
		"slice(10,20)": chunkResponse(t, nil, 1),
	}}
	r := newTestReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Estimated || batch.EstimatedTotal != 1 {
		t.Fatalf("estimate = %v/%v, want exact 1", batch.EstimatedTotal, batch.Estimated)
	}
}

func TestSyncScopeGated(t *testing.T) {
	detail := fmt.Sprintf(detailURLFormat, "324")
	f := &fakeBrowser{pageText: map[string]string{
		detail: evalJSON(t, "NotSignUp 报名后可见 未报名"),
	}}
	r := newTestReader(t, f)
	snap, err := r.SyncScope(context.Background(), "xianzhi/324")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Scope.Visibility != domain.ScopeGatedBySignup || snap.Scope.Access != domain.ScopeAccessNotGranted || snap.Scope.ContentsKnown {
		t.Fatalf("scope = %+v", snap.Scope)
	}
	if len(snap.Evidence) != 1 || len(snap.Facts) == 0 {
		t.Fatalf("evidence/facts = %d/%d", len(snap.Evidence), len(snap.Facts))
	}
}

func TestAuthStatusXianzhi(t *testing.T) {
	f := &fakeBrowser{}
	r := newTestReader(t, f)
	state, err := r.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthOK {
		t.Fatalf("state = %s err=%v, want OK", state, err)
	}

	f2 := &fakeBrowser{redirect: map[string]string{taskListURL: "https://account.aliyun.com/login/login.htm"}}
	r2 := newTestReader(t, f2)
	state, err = r2.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthRequired {
		t.Fatalf("state = %s err=%v, want REQUIRED", state, err)
	}
}

func TestAuthStatusToleratesNavigationTimeout(t *testing.T) {
	f := &fakeBrowser{openErr: sidecar.ErrNavigationTimeout}
	r := newTestReader(t, f)
	state, err := r.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthOK {
		t.Fatalf("state = %s err=%v, want OK", state, err)
	}
}

func TestSyncAccountXianzhi(t *testing.T) {
	f := &fakeBrowser{pageText: map[string]string{
		profileURL: evalJSON(t, "个人资料 实名认证 已认证"),
		financeURL: evalJSON(t, "积分累计 0 赏金累计 --"),
		vulnURL:    evalJSON(t, "提交漏洞总数 0 审核通过 0 提交情报 0"),
	}}
	r := newTestReader(t, f)
	snap, err := r.SyncAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Account.ID != "xianzhi/account" || snap.Account.IdentityVerified == nil || !*snap.Account.IdentityVerified {
		t.Fatalf("account = %+v", snap.Account)
	}
	if !snap.Account.Capability.CanSubmit {
		t.Fatalf("capability = %+v", snap.Account.Capability)
	}
	if len(snap.Evidence) != 3 || len(snap.Facts) == 0 {
		t.Fatalf("evidence/facts = %d/%d", len(snap.Evidence), len(snap.Facts))
	}
}

func TestGatedText(t *testing.T) {
	if !gatedText("参与该计划需要完成报名才能参与") {
		t.Fatal("expected gated text")
	}
	if gatedText("资产范围：*.example.com") {
		t.Fatal("did not expect gated text")
	}
}

func TestSyncScopeVisibleUnsupported(t *testing.T) {
	detail := fmt.Sprintf(detailURLFormat, "324")
	f := &fakeBrowser{pageText: map[string]string{detail: evalJSON(t, "资产范围：*.example.com")}}
	r := newTestReader(t, f)
	if _, err := r.SyncScope(context.Background(), "xianzhi/324"); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
