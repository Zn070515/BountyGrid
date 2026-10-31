package vulbox

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
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/Zn070515/BountyGrid/internal/sidecar"
)

var testNow = time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC)

func sampleProject() apiProject {
	return apiProject{
		ID: "444", ProjectID: "444", Type: "7", Status: "0",
		TaskTitle: "联想集团安全应急响应中心", TaskDesc: "全球智能设备的领导厂商",
		TaskIsSrc: "1", TaskWebsite: "https://lsrc.vulbox.com",
		TaskStime: 1490198400, TaskEtime: 1924945200,
		BonusRange: apiBonusRange{
			LowMin: 25, LowMax: 150, MiddleMin: 100, MiddleMax: 400,
			HighMin: 200, HighMax: 950, SeriousMin: 350, SeriousMax: 1250,
		},
		IsJoin: true, DifficultName: "简单", Goto: "https://lsrc.vulbox.com",
	}
}

func TestMapProgramType(t *testing.T) {
	cases := map[string]domain.ProgramType{
		"0": domain.TypeGeneralVulnProgram,
		"1": domain.TypePremiumProject,
		"5": domain.TypeCrowdTest,
		"6": domain.TypeOfflineJob,
		"7": domain.TypeEnterpriseSRC,
		"8": domain.TypePublicWelfareSRC,
	}
	for in, want := range cases {
		got, err := mapProgramType(in)
		if err != nil || got != want {
			t.Fatalf("type %s = %s (err=%v), want %s", in, got, err, want)
		}
	}
	if _, err := mapProgramType("99"); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("unknown type err = %v, want ErrUnsupported", err)
	}
}

func TestMapProgramStatus(t *testing.T) {
	cases := map[string]domain.ProgramStatus{
		"0": domain.StatusRunning,
		"1": domain.StatusPaused,
		"2": domain.StatusUnknown,
		"3": domain.StatusEnded,
		"9": domain.StatusUnknown,
	}
	for in, want := range cases {
		if got := mapProgramStatus(in); got != want {
			t.Fatalf("status %s = %s, want %s", in, got, want)
		}
	}
}

func TestProgramFromProjectJoined(t *testing.T) {
	p := sampleProject()
	prog, fs, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.ID != "vulbox/444" || prog.Platform != domain.PlatformVulbox {
		t.Fatalf("program = %+v", prog)
	}
	if prog.Type != domain.TypeEnterpriseSRC || prog.Status.Status != domain.StatusRunning {
		t.Fatalf("type/status = %s/%s", prog.Type, prog.Status.Status)
	}
	if prog.Eligibility.Decision != domain.Eligible {
		t.Fatalf("eligibility = %s, want ELIGIBLE", prog.Eligibility.Decision)
	}
	if prog.Scope.Visibility != domain.ScopeUnknown || prog.Scope.Access != domain.ScopeAccessUnknown || prog.Scope.ContentsKnown {
		t.Fatalf("scope must stay unknown until SyncScope: %+v", prog.Scope)
	}
	if prog.Reward.Min == nil || prog.Reward.Min.Amount != 25 || prog.Reward.Max == nil || prog.Reward.Max.Amount != 1250 {
		t.Fatalf("reward = %+v", prog.Reward)
	}
	if prog.Automation.ActiveHTTP != domain.CapUnknown {
		t.Fatalf("automation must fail closed: %+v", prog.Automation)
	}
	if len(prog.Status.Inputs) == 0 || len(prog.Evidence) == 0 {
		t.Fatal("program lost fact/evidence provenance")
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program is not domain-valid: %v", err)
	}
	if len(fs) < 8 {
		t.Fatalf("facts = %d, want at least 8", len(fs))
	}
	seenStatus := false
	for _, f := range fs {
		if len(f.EvidenceRefs) == 0 || f.ObservedAt.IsZero() {
			t.Fatalf("fact lost evidence/time: %+v", f)
		}
		if f.Predicate == facts.PredProgramStatus {
			seenStatus = true
			if f.Value != "RUNNING" {
				t.Fatalf("status fact = %v", f.Value)
			}
		}
	}
	if !seenStatus {
		t.Fatal("missing canonical program.status fact")
	}

	// Fact IDs must be deterministic for idempotent re-extraction.
	_, fs2, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fs {
		if fs[i].ID != fs2[i].ID {
			t.Fatalf("fact id changed: %s vs %s", fs[i].ID, fs2[i].ID)
		}
	}
}

func TestProgramFromProjectUnjoinedRequiresHuman(t *testing.T) {
	p := sampleProject()
	p.IsJoin = false
	prog, _, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Eligibility.Decision != domain.HumanActionNeeded {
		t.Fatalf("eligibility = %s, want HUMAN_ACTION_REQUIRED", prog.Eligibility.Decision)
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program is not domain-valid: %v", err)
	}
}

func TestProgramFromProjectOrdinaryUnknownEligibility(t *testing.T) {
	p := sampleProject()
	p.Type = "0"
	p.IsJoin = false
	p.IsApply = false
	prog, _, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Eligibility.Decision != domain.EligibilityUnknown {
		t.Fatalf("eligibility = %s, want UNKNOWN", prog.Eligibility.Decision)
	}
	if err := prog.Validate(); err != nil {
		t.Fatalf("program is not domain-valid: %v", err)
	}
}

func TestProgramFromProjectRejectsUnknownType(t *testing.T) {
	p := sampleProject()
	p.Type = "99"
	if _, _, err := programFromProject(p, "ev1", testNow); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestProgramFromProjectMissingEndDateIsNot1970(t *testing.T) {
	p := sampleProject()
	p.TaskStime, p.TaskEtime = 0, 0
	_, fs, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Predicate == facts.PredProgramStart || f.Predicate == facts.PredProgramEnd {
			t.Fatalf("missing platform date became a fact: %s=%v", f.Predicate, f.Value)
		}
	}
}

func TestProgramFromProjectPausedIsNotRunning(t *testing.T) {
	p := sampleProject()
	p.Status = "1"
	prog, fs, err := programFromProject(p, "ev1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Status.Status != domain.StatusPaused {
		t.Fatalf("status = %s, want PAUSED", prog.Status.Status)
	}
	found := false
	for _, f := range fs {
		if f.Predicate == facts.PredProgramPaused && f.Value == true {
			found = true
		}
	}
	if !found {
		t.Fatal("missing paused fact")
	}
}

// ---------- Reader transport tests ----------

type fakeBrowser struct {
	opened    []string
	evals     []string
	url       string
	title     string
	pageText  map[string]string
	responses map[string]string
	redirect  map[string]string
	openErr   error
	evalErr   error
}

func (f *fakeBrowser) Open(ctx context.Context, url string) error {
	f.opened = append(f.opened, url)
	f.url = url
	if target, ok := f.redirect[url]; ok {
		f.url = target
	}
	return f.openErr
}

func (f *fakeBrowser) WaitLoad(ctx context.Context) error { return nil }

func (f *fakeBrowser) Eval(ctx context.Context, script string) ([]byte, error) {
	f.evals = append(f.evals, script)
	if f.evalErr != nil {
		return nil, f.evalErr
	}
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
func (f *fakeBrowser) GetTitle(ctx context.Context) (string, error) { return f.title, nil }

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

func listResponse(t *testing.T, projects []apiProject) string {
	t.Helper()
	resp := projectListResponse{Code: 200, Message: "success"}
	resp.Data.CurrentPage = 1
	resp.Data.LastPage = 152
	resp.Data.PerPage = 10
	resp.Data.Total = 1513
	resp.Data.Data = projects
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return evalJSON(t, map[string]any{"status": 200, "body": string(body)})
}

func newReader(t *testing.T, b Browser) *Reader {
	t.Helper()
	r, err := NewReader(b, ReaderOptions{
		RawDir: t.TempDir(),
		Now:    func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSyncProgramsPublicList(t *testing.T) {
	phone := sampleProject()
	phone.TaskDesc = "contact 13800138000"
	paused := sampleProject()
	paused.ID, paused.ProjectID = "4017", "4017"
	paused.Type, paused.Status = "1", "1"
	paused.TaskTitle = "第4017期 业务安全测试"
	paused.IsJoin = false
	paused.Goto = "https://user.vulbox.com/projectHall/projectDetail/4017"

	f := &fakeBrowser{responses: map[string]string{
		"page=1&page_size=10": listResponse(t, []apiProject{phone, paused}),
	}}
	r := newReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{PageSize: 10, MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 2 || batch.ObservedCount != 2 {
		t.Fatalf("programs = %d/%d", len(batch.Programs), batch.ObservedCount)
	}
	if batch.EstimatedTotal != 1513 || !batch.Estimated {
		t.Fatalf("estimated = %v/%v", batch.EstimatedTotal, batch.Estimated)
	}
	if len(batch.Evidence) != 1 {
		t.Fatalf("evidence = %d, want 1", len(batch.Evidence))
	}
	if batch.Programs[1].Status.Status != domain.StatusPaused {
		t.Fatalf("second status = %s", batch.Programs[1].Status.Status)
	}
	for _, f := range batch.Facts {
		if len(f.EvidenceRefs) == 0 {
			t.Fatalf("fact without evidence: %+v", f)
		}
	}
	raw, err := os.ReadFile(batch.Evidence[0].RawRef)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "13800138000") {
		t.Fatal("raw evidence was not redacted")
	}
	if !strings.Contains(string(raw), "[REDACTED:phone]") {
		t.Fatalf("expected redaction marker, got %s", string(raw)[:200])
	}
	foundGET := false
	for _, s := range f.evals {
		if strings.Contains(s, `method:"GET"`) {
			foundGET = true
		}
	}
	if !foundGET {
		t.Fatal("project list must use a read-only GET")
	}
}

func TestSyncProgramsToleratesNavigationTimeout(t *testing.T) {
	f := &fakeBrowser{
		openErr: sidecar.ErrNavigationTimeout,
		responses: map[string]string{
			"page=1&page_size=10": listResponse(t, []apiProject{sampleProject()}),
		},
	}
	r := newReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{PageSize: 10, MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 1 {
		t.Fatalf("programs = %d", len(batch.Programs))
	}
}

func TestSyncProgramsSkipsUnsupportedType(t *testing.T) {
	p := sampleProject()
	p.ID, p.ProjectID = "99", "99"
	p.Type = "99"
	f := &fakeBrowser{responses: map[string]string{
		"page=1&page_size=10": listResponse(t, []apiProject{p}),
	}}
	r := newReader(t, f)
	batch, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{PageSize: 10, MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Programs) != 0 || len(batch.Unsupported) != 1 || batch.Unsupported[0] != "99" {
		t.Fatalf("programs=%d unsupported=%v", len(batch.Programs), batch.Unsupported)
	}
}

func TestSyncProgramsRejectsAPIError(t *testing.T) {
	f := &fakeBrowser{responses: map[string]string{
		"page=1&page_size=10": evalJSON(t, map[string]any{
			"status": 200,
			"body":   `{"code":400,"msg":"page size必须大于等于10"}`,
		}),
	}}
	r := newReader(t, f)
	if _, err := r.SyncPrograms(context.Background(), adapter.SyncOptions{PageSize: 10, MaxPages: 1}); err == nil {
		t.Fatal("API error should surface")
	}
}

func TestAuthStatus(t *testing.T) {
	f := &fakeBrowser{url: "https://user.vulbox.com/home"}
	r := newReader(t, f)
	state, err := r.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthOK {
		t.Fatalf("user center = %s err=%v, want OK", state, err)
	}

	f2 := &fakeBrowser{
		redirect: map[string]string{"https://user.vulbox.com/home": "https://www.vulbox.com/"},
		pageText: map[string]string{
			"https://www.vulbox.com/": evalJSON(t, "登录后更精彩 微信扫码关注即可登录"),
		},
	}
	r2 := newReader(t, f2)
	state, err = r2.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthRequired {
		t.Fatalf("redirected = %s err=%v, want REQUIRED", state, err)
	}
}

func TestAuthStatusToleratesNavigationTimeout(t *testing.T) {
	f := &fakeBrowser{url: "https://user.vulbox.com/home", openErr: sidecar.ErrNavigationTimeout}
	r := newReader(t, f)
	state, err := r.AuthStatus(context.Background())
	if err != nil || state != adapter.AuthOK {
		t.Fatalf("state = %s err=%v, want OK", state, err)
	}
}

func TestSyncAccountToleratesNavigationTimeout(t *testing.T) {
	home := "信誉优秀 | 100 分\n当前等级:\n见习白帽\n经验值:\n9\n下一等级:\n初级白帽\n" +
		"赛季战况\nS3\n赛季积分\n0\n赛季排名\n9999+\n账户余额\n0 元\n提成金\n0 元\n" +
		"我的券\n0 张\n待审阅\n0\n处理中\n0\n已打款\n0"
	profile := "个人中心\n已实名\n手机已绑定\n邮箱已绑定\n微信已绑定\n银行卡未绑定"
	joined := "我的项目\n联想集团安全应急响应中心\n已加入\n嘉立创安全应急响应中心\n已加入\n共 2 条数据"
	f := &fakeBrowser{
		url:     "https://user.vulbox.com/home",
		openErr: sidecar.ErrNavigationTimeout,
		pageText: map[string]string{
			"https://user.vulbox.com/home":                  evalJSON(t, home),
			"https://user.vulbox.com/personCenter":          evalJSON(t, profile),
			"https://user.vulbox.com/projectHall/myProject": evalJSON(t, joined),
		},
	}
	r := newReader(t, f)
	snap, err := r.SyncAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Facts) == 0 {
		t.Fatal("no facts")
	}
}

func srcReader(t *testing.T) (*Reader, *fakeBrowser) {
	t.Helper()
	f := &fakeBrowser{
		responses: map[string]string{
			"type=7&status=0": listResponse(t, []apiProject{sampleProject()}),
		},
		pageText: map[string]string{
			"https://lsrc.vulbox.com": evalJSON(t, srcScopeFixture),
		},
	}
	return newReader(t, f), f
}

func TestSyncScopeEnterpriseSRC(t *testing.T) {
	r, _ := srcReader(t)
	snap, err := r.SyncScope(context.Background(), "vulbox/444")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Scope.Visibility != domain.ScopePublic || snap.Scope.Access != domain.ScopeAccessGranted || !snap.Scope.ContentsKnown {
		t.Fatalf("scope = %+v", snap.Scope)
	}
	if len(snap.Scope.Include) != 9 || len(snap.Scope.Exclude) != 1 {
		t.Fatalf("include/exclude = %d/%d", len(snap.Scope.Include), len(snap.Scope.Exclude))
	}
	if len(snap.Evidence) != 1 {
		t.Fatalf("evidence = %d", len(snap.Evidence))
	}
	seenVisibility := false
	for _, f := range snap.Facts {
		if len(f.EvidenceRefs) == 0 {
			t.Fatalf("fact without evidence: %+v", f)
		}
		if f.Predicate == facts.PredScopeVisibility && f.Value == "PUBLIC" {
			seenVisibility = true
		}
	}
	if !seenVisibility {
		t.Fatal("missing PUBLIC scope visibility fact")
	}
}

func TestSyncPolicyEnterpriseSRC(t *testing.T) {
	r, _ := srcReader(t)
	snap, err := r.SyncPolicy(context.Background(), "vulbox/444")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Automation.FileUpload != domain.CapForbidden {
		t.Fatalf("file upload = %s, want FORBIDDEN", snap.Automation.FileUpload)
	}
	if snap.Automation.AIAnalysis != domain.CapConditional {
		t.Fatalf("ai analysis = %s, want CONDITIONAL", snap.Automation.AIAnalysis)
	}
	if !snap.Automation.HumanVerificationRequired {
		t.Fatal("human verification must be required")
	}
	if len(snap.Facts) == 0 {
		t.Fatal("policy facts missing")
	}
}

func TestSyncProgramEnterpriseSRC(t *testing.T) {
	r, _ := srcReader(t)
	snap, err := r.SyncProgram(context.Background(), "vulbox/444")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Program.Scope.Visibility != domain.ScopePublic || !snap.Program.Scope.ContentsKnown {
		t.Fatalf("program scope = %+v", snap.Program.Scope)
	}
	if snap.Program.Eligibility.Decision != domain.Eligible {
		t.Fatalf("eligibility = %s", snap.Program.Eligibility.Decision)
	}
	if err := snap.Program.Validate(); err != nil {
		t.Fatalf("program invalid: %v", err)
	}
	for _, f := range snap.Facts {
		if f.Predicate == facts.PredScopeVisibility && f.Value == "UNKNOWN" {
			t.Fatal("stale UNKNOWN scope fact survived the merge")
		}
	}
}

func TestParseAccountText(t *testing.T) {
	text := "信誉优秀 | 100 分\n当前等级:\n见习白帽\n经验值:\n9\n下一等级:\n初级白帽\n" +
		"赛季战况\nS3\n赛季积分\n0\n赛季排名\n9999+\n账户余额\n0 元\n提成金\n0 元\n" +
		"我的券\n0 张\n待审阅\n0\n处理中\n0\n已打款\n0"
	got := parseAccountText(text)
	if got.Level != "见习白帽" || got.Exp != 9 || got.Reputation != 100 {
		t.Fatalf("account = %+v", got)
	}
	if got.Season != "S3" || got.SeasonPoints != 0 || got.SeasonRank != "9999+" {
		t.Fatalf("season = %+v", got)
	}
}

func TestParseIdentityText(t *testing.T) {
	identity, bank := parseIdentityText("个人中心\n已实名\n手机已绑定\n邮箱已绑定\n微信已绑定\n银行卡未绑定")
	if identity == nil || !*identity {
		t.Fatalf("identity = %v", identity)
	}
	if bank == nil || *bank {
		t.Fatalf("bank = %v, want false", bank)
	}
	identity, bank = parseIdentityText("个人中心\n银行卡已绑定")
	if identity != nil {
		t.Fatalf("identity = %v, want nil", identity)
	}
	if bank == nil || !*bank {
		t.Fatalf("bank = %v, want true", bank)
	}
	identity, _ = parseIdentityText("个人中心\n实名认证 已认证")
	if identity == nil || !*identity {
		t.Fatalf("identity = %v, want true", identity)
	}
}

func TestParseJoinedProjects(t *testing.T) {
	text := "我的项目\n联想集团安全应急响应中心\n已加入\n嘉立创安全应急响应中心\n已加入\n共 2 条数据"
	names, count := parseJoinedProjects(text)
	if len(names) != 2 || count != 2 {
		t.Fatalf("names=%v count=%d", names, count)
	}
}

func TestSyncAccount(t *testing.T) {
	home := "信誉优秀 | 100 分\n当前等级:\n见习白帽\n经验值:\n9\n下一等级:\n初级白帽\n" +
		"赛季战况\nS3\n赛季积分\n0\n赛季排名\n9999+\n账户余额\n0 元\n提成金\n0 元\n" +
		"我的券\n0 张\n待审阅\n0\n处理中\n0\n已打款\n0"
	profile := "个人中心\n已实名\n手机已绑定\n邮箱已绑定\n微信已绑定\n银行卡未绑定"
	joined := "我的项目\n联想集团安全应急响应中心\n已加入\n嘉立创安全应急响应中心\n已加入\n共 2 条数据"
	f := &fakeBrowser{pageText: map[string]string{
		"https://user.vulbox.com/home":                  evalJSON(t, home),
		"https://user.vulbox.com/personCenter":          evalJSON(t, profile),
		"https://user.vulbox.com/projectHall/myProject": evalJSON(t, joined),
	}}
	f.url = "https://user.vulbox.com/home"
	r := newReader(t, f)
	snap, err := r.SyncAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Account.ID != "vulbox/account" || snap.Account.Platform != domain.PlatformVulbox {
		t.Fatalf("account = %+v", snap.Account)
	}
	if snap.Account.IdentityVerified == nil || !*snap.Account.IdentityVerified {
		t.Fatalf("identity = %v", snap.Account.IdentityVerified)
	}
	if snap.Account.BankBound == nil || *snap.Account.BankBound {
		t.Fatalf("bank = %v", snap.Account.BankBound)
	}
	if !snap.Account.Capability.CanSubmit || snap.Account.Capability.CanPayout {
		t.Fatalf("capability = %+v", snap.Account.Capability)
	}
	if err := snap.Account.Validate(); err != nil {
		t.Fatalf("account is not domain-valid: %v", err)
	}
	if len(snap.Evidence) != 3 {
		t.Fatalf("evidence = %d, want 3", len(snap.Evidence))
	}
	if len(snap.Facts) == 0 {
		t.Fatal("no account facts")
	}
	seenJoined := false
	for _, f := range snap.Facts {
		if len(f.EvidenceRefs) == 0 {
			t.Fatalf("fact without evidence: %+v", f)
		}
		if f.Predicate == "vulbox.account.joined_projects" {
			seenJoined = true
		}
	}
	if !seenJoined {
		t.Fatal("missing joined projects fact")
	}
}

func TestSyncAccountRequiresAuth(t *testing.T) {
	f := &fakeBrowser{
		redirect: map[string]string{"https://user.vulbox.com/home": "https://www.vulbox.com/"},
		pageText: map[string]string{
			"https://www.vulbox.com/": evalJSON(t, "登录"),
		},
	}
	r := newReader(t, f)
	if _, err := r.SyncAccount(context.Background()); !errors.Is(err, adapter.ErrAuthRequired) {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
}
