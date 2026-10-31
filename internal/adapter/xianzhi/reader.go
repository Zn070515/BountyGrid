package xianzhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/evidence"
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/Zn070515/BountyGrid/internal/sidecar"
	accountstate "github.com/Zn070515/BountyGrid/internal/state"
)

type Browser interface {
	Open(ctx context.Context, url string) error
	Reload(ctx context.Context) error
	WaitLoad(ctx context.Context) error
	WaitFn(ctx context.Context, expr string) error
	Eval(ctx context.Context, script string) ([]byte, error)
	GetURL(ctx context.Context) (string, error)
	GetTitle(ctx context.Context) (string, error)
}

type ReaderOptions struct {
	RawDir      string
	Now         func() time.Time
	TaskListURL string
}

type Reader struct {
	browser     Browser
	rawDir      string
	now         func() time.Time
	taskListURL string
	openedList  bool
}

func NewReader(b Browser, opts ReaderOptions) (*Reader, error) {
	if b == nil {
		return nil, fmt.Errorf("xianzhi: browser is nil")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TaskListURL == "" {
		opts.TaskListURL = taskListURL
	}
	return &Reader{browser: b, rawDir: opts.RawDir, now: opts.Now, taskListURL: opts.TaskListURL}, nil
}

func (r *Reader) Platform() domain.Platform { return domain.PlatformXianzhi }

func (r *Reader) AuthStatus(ctx context.Context) (adapter.AuthState, error) {
	if err := r.openList(ctx); err != nil {
		return adapter.AuthUnknown, err
	}
	u, err := r.browser.GetURL(ctx)
	if err != nil {
		return adapter.AuthUnknown, fmt.Errorf("xianzhi: auth url: %w", err)
	}
	if strings.Contains(u, "account.aliyun.com") {
		return adapter.AuthRequired, nil
	}
	if strings.Contains(u, "yundun.console.aliyun.com") || strings.Contains(u, "xianzhi.aliyun.com") {
		return adapter.AuthOK, nil
	}
	return adapter.AuthUnknown, nil
}

func (r *Reader) openList(ctx context.Context) error {
	if r.openedList {
		return nil
	}
	if err := r.browser.Open(ctx, r.taskListURL); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return fmt.Errorf("xianzhi: open task list: %w", err)
	}
	// Same-URL hash navigation may be a no-op; force a fresh data fetch.
	if err := r.browser.Reload(ctx); err != nil {
		return fmt.Errorf("xianzhi: reload task list: %w", err)
	}
	if err := r.browser.WaitLoad(ctx); err != nil {
		return fmt.Errorf("xianzhi: wait task list: %w", err)
	}
	// The SPA renders cards after the console API responds; wait for at least
	// one card before extracting (best-effort timeout).
	_ = r.browser.WaitFn(ctx, "document.querySelectorAll('.card-item').length > 0")
	r.openedList = true
	return nil
}

func (r *Reader) SyncPrograms(ctx context.Context, opts adapter.SyncOptions) (adapter.ProgramBatch, error) {
	if err := r.openList(ctx); err != nil {
		return adapter.ProgramBatch{}, err
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}
	var batch adapter.ProgramBatch
	maxPageSeen := 0
	for page := 1; page <= maxPages; page++ {
		chunk, err := r.syncPage(ctx, page)
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		at := r.now().UTC()
		raw, err := json.Marshal(chunk)
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		ev, err := r.capture(domain.SourceProgram, "task-list", fmt.Sprintf("page-%d", page),
			r.taskListURL+"#page="+strconv.Itoa(page), "Xianzhi 任务列表", string(raw), at)
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		batch.Evidence = append(batch.Evidence, ev)
		for _, card := range chunk.Cards {
			prog, fs, err := taskProgram(card, ev.ID, at)
			if errors.Is(err, adapter.ErrUnsupported) {
				batch.Unsupported = append(batch.Unsupported, strconv.FormatInt(card.ID, 10))
				continue
			}
			if err != nil {
				return adapter.ProgramBatch{}, err
			}
			batch.Programs = append(batch.Programs, prog)
			batch.Facts = append(batch.Facts, fs...)
		}
		if chunk.MaxPage > maxPageSeen {
			maxPageSeen = chunk.MaxPage
		}
		if len(chunk.Cards) == 0 || (chunk.MaxPage > 0 && page >= chunk.MaxPage) {
			break
		}
	}
	batch.ObservedCount = len(batch.Programs)
	if maxPageSeen > 0 && maxPages >= maxPageSeen {
		batch.EstimatedTotal = len(batch.Programs)
		batch.Estimated = false
	} else if maxPageSeen > 0 {
		// The UI renders 20 cards per page; this is an estimate for a partial sync.
		batch.EstimatedTotal = maxPageSeen * 20
		batch.Estimated = true
	}
	return batch, nil
}

func (r *Reader) syncPage(ctx context.Context, page int) (taskChunk, error) {
	if page > 1 {
		out, err := r.browser.Eval(ctx, clickPageJS(page))
		if err != nil {
			return taskChunk{}, fmt.Errorf("xianzhi: click page %d: %w", page, err)
		}
		var ack struct {
			OK bool `json:"ok"`
		}
		if raw, err := unwrapEval(out); err == nil {
			_ = json.Unmarshal(raw, &ack)
		}
		if !ack.OK {
			return taskChunk{}, fmt.Errorf("xianzhi: page %d is not available", page)
		}
	}
	var combined taskChunk
	for _, offset := range []int{0, 10} {
		out, err := r.browser.Eval(ctx, extractCardsJS(offset, 10))
		if err != nil {
			return taskChunk{}, fmt.Errorf("xianzhi: extract page %d: %w", page, err)
		}
		raw, err := unwrapEval(out)
		if err != nil {
			return taskChunk{}, err
		}
		var chunk taskChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return taskChunk{}, fmt.Errorf("xianzhi: decode task chunk: %w", err)
		}
		combined.Cards = append(combined.Cards, chunk.Cards...)
		if chunk.MaxPage > combined.MaxPage {
			combined.MaxPage = chunk.MaxPage
		}
	}
	return combined, nil
}

func extractCardsJS(offset, limit int) string {
	return fmt.Sprintf(`(() => {
  const cards=[...document.querySelectorAll('.card-item')];
  const out=cards.slice(%d,%d).map(c=>{
    const parts=(c.innerText||'').replace(/\n+/g,'|').split('|');
    const val=k=>{const i=parts.indexOf(k+'：'); return (i>=0 && i+1<parts.length)?parts[i+1].trim():''};
    const k=Object.keys(c).find(x=>x.startsWith('__reactFiber'));
    let f=k?c[k]:null,p=null;
    for(let i=0;i<8&&f;i++){const mp=f.memoizedProps||{};if(mp.task){p=mp.task;break;}f=f.return;}
    if(!p) return {missing:true,title:parts[0]||''};
    return {id:p.id,name:p.name||'',status:p.status,type:p.type,registrationBegin:p.registrationBegin||0,registrationEnd:p.registrationEnd||0,taskBegin:p.taskBegin||0,taskEnd:p.taskEnd||0,registrationLimit:p.registrationLimit||0,bounty:val('赏金范围'),buttons:[...c.querySelectorAll('button')].map(b=>(b.innerText||'').trim()),title:parts[0]||''};
  });
  const pages=[...document.querySelectorAll('.cs-pagination-item')].map(x=>parseInt((x.innerText||'').trim(),10)).filter(Number.isFinite);
  return JSON.stringify({cards:out,maxPage:pages.length?Math.max(...pages):0});
})()`, offset, offset+limit)
}

func clickPageJS(page int) string {
	return fmt.Sprintf(`(async()=>{
  const b=[...document.querySelectorAll('.cs-pagination-item')].find(x=>(x.innerText||'').trim()===String(%d));
  if(!b) return JSON.stringify({ok:false});
  const before=(document.querySelector('.card-item')||{}).innerText||'';
  b.click();
  for(let i=0;i<80;i++){await new Promise(r=>setTimeout(r,100)); const now=(document.querySelector('.card-item')||{}).innerText||''; if(now&&now!==before) break;}
  return JSON.stringify({ok:true});
})()`, page)
}

func (r *Reader) SyncAccount(ctx context.Context) (adapter.AccountSnapshot, error) {
	at := r.now().UTC()
	profileText, profileEv, err := r.readAnyPage(ctx, profileURL, "account-profile", domain.SourceAccount, "Xianzhi 个人资料", at)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}
	financeText, financeEv, err := r.readAnyPage(ctx, financeURL, "account-finance", domain.SourceAccount, "Xianzhi 财务管理", at)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}
	vulnText, vulnEv, err := r.readAnyPage(ctx, vulnURL, "account-vulns", domain.SourceAccount, "Xianzhi 漏洞管理", at)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}

	identity := parseIdentity(profileText)
	points := parseNumber(financeText, `积分[^\d]{0,20}(\d+)`)
	bounty := parseFloat(financeText, `(?:赏金累计|累计赏金|赏金总额)[^\d]{0,20}([\d.]+)`)
	submitted := parseNumber(vulnText, `提交漏洞[^\d]{0,20}(\d+)`)
	approved := parseNumber(vulnText, `审核通过[^\d]{0,20}(\d+)`)
	intel := parseNumber(vulnText, `提交情报[^\d]{0,20}(\d+)`)

	acct := domain.Account{
		ID: "xianzhi/account", Platform: domain.PlatformXianzhi, Registered: true,
		IdentityVerified: identity, ObservedAt: at,
	}
	var gates []domain.EligibilityGate
	if identity != nil {
		satisfied := domain.GateSatisfied
		if !*identity {
			satisfied = domain.GateUnsatisfied
		}
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateIdentityVerified, Required: true,
			Satisfied: satisfied, Blocking: true, Evidence: []domain.EvidenceID{profileEv.ID},
		})
	}
	acct.Gates = gates
	acct.Capability = accountstate.DeriveAccountCapability(gates, identity, nil, nil, at)

	fs := []domain.ObservedFact{}
	add := func(ev domain.EvidenceID, pred string, value any) {
		fs = append(fs, accountFact(ev, acct.ID, pred, value, at))
	}
	if identity != nil {
		add(profileEv.ID, facts.PredIdentityVerified, *identity)
	}
	if strings.Contains(financeText, "积分") {
		add(financeEv.ID, "xianzhi.account.points", points)
	}
	if strings.Contains(financeText, "赏金") {
		add(financeEv.ID, "xianzhi.account.bounty_cny", bounty)
	}
	if strings.Contains(vulnText, "提交漏洞") {
		add(vulnEv.ID, "xianzhi.account.submitted_vulns", submitted)
	}
	if strings.Contains(vulnText, "审核通过") {
		add(vulnEv.ID, "xianzhi.account.approved_vulns", approved)
	}
	if strings.Contains(vulnText, "提交情报") {
		add(vulnEv.ID, "xianzhi.account.submitted_intel", intel)
	}
	if err := acct.Validate(); err != nil {
		return adapter.AccountSnapshot{}, fmt.Errorf("xianzhi: invalid account: %w", err)
	}
	return adapter.AccountSnapshot{
		Account:  acct,
		Facts:    fs,
		Evidence: []domain.Evidence{profileEv, financeEv, vulnEv},
	}, nil
}

func (r *Reader) SyncScope(ctx context.Context, id domain.ProgramID) (adapter.ScopeSnapshot, error) {
	native := strings.TrimPrefix(string(id), "xianzhi/")
	at := r.now().UTC()
	url := fmt.Sprintf(detailURLFormat, native)
	text, ev, err := r.readAnyPage(ctx, url, "program-scope", domain.SourceProgram, "Xianzhi 任务详情", at)
	if err != nil {
		return adapter.ScopeSnapshot{}, err
	}
	if gatedText(text) {
		scope := domain.Scope{
			Visibility:    domain.ScopeGatedBySignup,
			Access:        domain.ScopeAccessNotGranted,
			ContentsKnown: false,
			ObservedAt:    at,
			Evidence:      []domain.EvidenceID{ev.ID},
		}
		if err := scope.Validate(); err != nil {
			return adapter.ScopeSnapshot{}, fmt.Errorf("xianzhi: invalid gated scope: %w", err)
		}
		fs := []domain.ObservedFact{
			taskFact(ev.ID, id, facts.PredScopeVisibility, string(domain.ScopeGatedBySignup), "", at),
			taskFact(ev.ID, id, "xianzhi.scope.source", url, "", at),
		}
		return adapter.ScopeSnapshot{Scope: scope, Facts: fs, Evidence: []domain.Evidence{ev}}, nil
	}
	return adapter.ScopeSnapshot{}, fmt.Errorf("xianzhi: scope is visible but its DOM parser is not verified yet: %w", adapter.ErrUnsupported)
}

func gatedText(text string) bool {
	for _, marker := range []string{"NotSignUp", "报名后可见", "报名后", "未报名", "请先报名", "暂无权限", "需要完成报名", "完成报名才能参与"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (r *Reader) SyncPolicy(ctx context.Context, id domain.ProgramID) (adapter.PolicySnapshot, error) {
	native := strings.TrimPrefix(string(id), "xianzhi/")
	at := r.now().UTC()
	url := fmt.Sprintf(detailURLFormat, native)
	_, ev, err := r.readAnyPage(ctx, url, "program-policy", domain.SourcePolicy, "Xianzhi 任务规则", at)
	if err != nil {
		return adapter.PolicySnapshot{}, err
	}
	fs := []domain.ObservedFact{taskFact(ev.ID, id, "xianzhi.policy.source", url, "", at)}
	return adapter.PolicySnapshot{
		Automation:  conservativeAutomation(),
		Constraints: domain.Constraints{},
		Facts:       fs,
		Evidence:    []domain.Evidence{ev},
	}, nil
}

func (r *Reader) SyncProgram(ctx context.Context, id domain.ProgramID) (adapter.ProgramSnapshot, error) {
	batch, err := r.SyncPrograms(ctx, adapter.SyncOptions{MaxPages: 5})
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	var prog *domain.Program
	for i := range batch.Programs {
		if batch.Programs[i].ID == id {
			prog = &batch.Programs[i]
			break
		}
	}
	if prog == nil {
		return adapter.ProgramSnapshot{}, fmt.Errorf("xianzhi: task %s not found: %w", id, adapter.ErrUnsupported)
	}
	scope, err := r.SyncScope(ctx, id)
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	prog.Scope = scope.Scope
	if err := prog.Validate(); err != nil {
		return adapter.ProgramSnapshot{}, fmt.Errorf("xianzhi: invalid program %s after scope merge: %w", id, err)
	}
	var fs []domain.ObservedFact
	for _, f := range batch.Facts {
		if f.ProgramID != nil && *f.ProgramID == id {
			fs = append(fs, f)
		}
	}
	fs = append(fs, scope.Facts...)
	evs := append(append([]domain.Evidence{}, batch.Evidence...), scope.Evidence...)
	return adapter.ProgramSnapshot{Program: *prog, Scope: scope.Scope, Facts: fs, Evidence: evs}, nil
}

func (r *Reader) readAnyPage(ctx context.Context, url, source string, sourceType domain.SourceType, title string, at time.Time) (string, domain.Evidence, error) {
	if err := r.browser.Open(ctx, url); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return "", domain.Evidence{}, fmt.Errorf("xianzhi: open %s: %w", url, err)
	}
	if err := r.browser.WaitLoad(ctx); err != nil {
		return "", domain.Evidence{}, fmt.Errorf("xianzhi: wait %s: %w", url, err)
	}
	finalURL, err := r.browser.GetURL(ctx)
	if err != nil {
		return "", domain.Evidence{}, fmt.Errorf("xianzhi: url %s: %w", url, err)
	}
	if strings.Contains(finalURL, "account.aliyun.com") {
		return "", domain.Evidence{}, adapter.ErrAuthRequired
	}
	text, err := r.pageText(ctx)
	if err != nil {
		return "", domain.Evidence{}, err
	}
	ev, err := r.capture(sourceType, source, "capture", finalURL, title, text, at)
	if err != nil {
		return "", domain.Evidence{}, err
	}
	return text, ev, nil
}

func (r *Reader) pageText(ctx context.Context) (string, error) {
	out, err := r.browser.Eval(ctx, "(()=>document.body?document.body.innerText:'')()")
	if err != nil {
		return "", fmt.Errorf("xianzhi: page text: %w", err)
	}
	raw, err := unwrapEval(out)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw), nil
	}
	return s, nil
}

func (r *Reader) capture(sourceType domain.SourceType, source, suffix, url, title, body string, at time.Time) (domain.Evidence, error) {
	if r.rawDir == "" {
		return domain.Evidence{}, fmt.Errorf("xianzhi: raw evidence dir is required")
	}
	id := domain.EvidenceID(fmt.Sprintf("xianzhi/%s/%s/%s/%d", source, suffix, at.Format("20060102"), at.UnixNano()))
	rawRef := filepath.Join(r.rawDir, safeName(string(id))+".json")
	ev := domain.Evidence{
		ID: id, Platform: domain.PlatformXianzhi, SourceType: sourceType,
		Transport: domain.TransportBrowserPage, URL: url, PageTitle: title,
		CapturedAt: at, RawRef: rawRef,
	}
	prepared, redacted, _, err := evidence.Prepare(ev, body)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("xianzhi: prepare evidence: %w", err)
	}
	if err := os.MkdirAll(r.rawDir, 0o700); err != nil {
		return domain.Evidence{}, fmt.Errorf("xianzhi: create raw dir: %w", err)
	}
	if err := os.WriteFile(rawRef, []byte(redacted), 0o600); err != nil {
		return domain.Evidence{}, fmt.Errorf("xianzhi: write raw evidence: %w", err)
	}
	return prepared, nil
}

func safeName(s string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "?", "_", "&", "_", "=", "_")
	return repl.Replace(s)
}

func unwrapEval(b []byte) ([]byte, error) {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return []byte(s), nil
	}
	if json.Valid(b) {
		return b, nil
	}
	return nil, fmt.Errorf("xianzhi: unexpected eval output")
}

func parseIdentity(text string) *bool {
	switch {
	case strings.Contains(text, "未实名"):
		v := false
		return &v
	case strings.Contains(text, "已实名"), strings.Contains(text, "实名认证") && strings.Contains(text, "已认证"):
		v := true
		return &v
	default:
		return nil
	}
}

func parseNumber(text, pattern string) int {
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(m[1])
	return v
}

func parseFloat(text, pattern string) float64 {
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func accountFact(ev domain.EvidenceID, accountID domain.AccountID, pred string, value any, at time.Time) domain.ObservedFact {
	return domain.ObservedFact{
		ID:               domain.FactID(fmt.Sprintf("%s/%s", ev, strings.ReplaceAll(pred, ".", "_"))),
		Subject:          domain.SubjectAccount,
		Platform:         domain.PlatformXianzhi,
		AccountID:        &accountID,
		Predicate:        pred,
		Value:            value,
		ObservedAt:       at,
		EvidenceRefs:     []domain.EvidenceID{ev},
		Confidence:       1,
		ExtractorVersion: extractor,
	}
}

var _ adapter.Reader = (*Reader)(nil)
