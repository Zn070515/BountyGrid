package butian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	RawDir string
	Now    func() time.Time
}

type Reader struct {
	browser Browser
	rawDir  string
	now     func() time.Time
}

func NewReader(b Browser, opts ReaderOptions) (*Reader, error) {
	if b == nil {
		return nil, fmt.Errorf("butian: browser is nil")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Reader{browser: b, rawDir: opts.RawDir, now: opts.Now}, nil
}

func (r *Reader) Platform() domain.Platform { return domain.PlatformButian }

func (r *Reader) AuthStatus(ctx context.Context) (adapter.AuthState, error) {
	if err := r.browser.Open(ctx, centerURL); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return adapter.AuthUnknown, fmt.Errorf("butian: auth open: %w", err)
	}
	_ = r.browser.WaitLoad(ctx)
	u, err := r.browser.GetURL(ctx)
	if err != nil {
		return adapter.AuthUnknown, fmt.Errorf("butian: auth url: %w", err)
	}
	if strings.Contains(u, "user.skyeye.qianxin.com") || u == "https://www.butian.net" || u == "https://www.butian.net/" {
		return adapter.AuthRequired, nil
	}
	if strings.Contains(u, "/WhiteHat/Center") {
		return adapter.AuthOK, nil
	}
	return adapter.AuthUnknown, nil
}

func (r *Reader) SyncAccount(ctx context.Context) (adapter.AccountSnapshot, error) {
	at := r.now().UTC()
	centerText, centerEv, err := r.readCenter(ctx, at)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}
	submitText, submitEv, err := r.readSubmit(ctx, at)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}

	a := parseAccountText(centerText)
	a.Compliance = parseCompliance(submitText)

	acct := domain.Account{
		ID: "butian/account", Platform: domain.PlatformButian, Registered: true,
		IdentityVerified: a.Identity, ComplianceExamCompleted: a.Compliance,
		PracticalCertification: a.Practical, BankBound: a.BankBound,
		Level: a.Level, ObservedAt: at,
	}
	if a.Practical != nil && !*a.Practical {
		// The field is informative; the gate is program-scoped below.
	}

	var gates []domain.EligibilityGate
	if a.Identity != nil {
		satisfied := domain.GateSatisfied
		if !*a.Identity {
			satisfied = domain.GateUnsatisfied
		}
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateIdentityVerified, Required: true,
			Satisfied: satisfied, Blocking: true, Evidence: []domain.EvidenceID{centerEv.ID},
		})
	}
	complianceGate := domain.EligibilityGate{
		Kind: domain.GateComplianceExam, Required: true, Blocking: true,
		Evidence: []domain.EvidenceID{submitEv.ID},
	}
	switch {
	case a.Compliance == nil:
		complianceGate.Satisfied = domain.GateUnknown
		gates = append(gates, complianceGate)
	case *a.Compliance:
		complianceGate.Satisfied = domain.GateSatisfied
		gates = append(gates, complianceGate)
	default:
		complianceGate.Satisfied = domain.GateUnsatisfied
		gates = append(gates, complianceGate)
	}
	if a.BankBound != nil && !*a.BankBound {
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateBankBinding, Required: true,
			Satisfied: domain.GateUnsatisfied, Blocking: true,
			Evidence: []domain.EvidenceID{centerEv.ID},
		})
	}
	acct.Gates = gates
	acct.Capability = accountstate.DeriveAccountCapability(gates, a.Identity, a.Compliance, a.BankBound, at)
	// Unknown compliance must fail closed: the platform requires the quarterly
	// exam before any submission.
	if a.Compliance == nil || !*a.Compliance {
		acct.Capability.CanSubmit = false
	}

	fs := []domain.ObservedFact{}
	add := func(ev domain.EvidenceID, pred string, value any) {
		fs = append(fs, accountFact(ev, acct.ID, pred, value, at))
	}
	if a.Identity != nil {
		add(centerEv.ID, facts.PredIdentityVerified, *a.Identity)
	}
	if a.Practical != nil {
		add(centerEv.ID, facts.PredPracticalCert, *a.Practical)
	}
	if a.Compliance != nil {
		add(submitEv.ID, facts.PredComplianceExam, *a.Compliance)
	}
	if a.Level != "" {
		add(centerEv.ID, "butian.account.level", a.Level)
	}
	if a.HonorCoins > 0 {
		add(centerEv.ID, "butian.account.honor_coins", a.HonorCoins)
	}
	if a.Points > 0 {
		add(centerEv.ID, "butian.account.points", a.Points)
	}
	if a.Bounty > 0 {
		add(centerEv.ID, "butian.account.bounty_cny", a.Bounty)
	}
	if a.Submitted > 0 {
		add(centerEv.ID, "butian.account.submitted_vulns", a.Submitted)
	}
	if a.Pending > 0 {
		add(centerEv.ID, "butian.account.pending_review", a.Pending)
	}
	if a.Rank != "" {
		add(centerEv.ID, "butian.account.rank", a.Rank)
	}
	if a.BankBound != nil {
		add(centerEv.ID, facts.PredBankBound, *a.BankBound)
	}
	if err := acct.Validate(); err != nil {
		return adapter.AccountSnapshot{}, fmt.Errorf("butian: invalid account: %w", err)
	}
	return adapter.AccountSnapshot{
		Account:  acct,
		Facts:    fs,
		Evidence: []domain.Evidence{centerEv, submitEv},
	}, nil
}

func (r *Reader) SyncPrograms(ctx context.Context, opts adapter.SyncOptions) (adapter.ProgramBatch, error) {
	cards, total, noData, err := r.extractCards(ctx)
	if err != nil {
		return adapter.ProgramBatch{}, err
	}
	if total == 0 || len(cards) == 0 || noData {
		// A fresh session may not be on the project hall yet; open it once.
		// Never reload: the user's human captcha solution is page-local.
		if u, _ := r.browser.GetURL(ctx); !strings.Contains(u, "/Reward/plan/2") {
			if err := r.browser.Open(ctx, projectHallURL); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
				return adapter.ProgramBatch{}, fmt.Errorf("butian: open project hall: %w", err)
			}
			_ = r.browser.WaitLoad(ctx)
			_ = r.browser.WaitFn(ctx, "document.querySelectorAll('.plan-exclusive-src .company-card').length > 0")
			cards, total, noData, err = r.extractCards(ctx)
			if err != nil {
				return adapter.ProgramBatch{}, err
			}
		}
	}
	if total == 0 || len(cards) == 0 || noData {
		return adapter.ProgramBatch{}, fmt.Errorf("butian: exclusive SRC list is captcha-protected; complete the captcha in the browser and retry: %w", adapter.ErrGated)
	}
	at := r.now().UTC()
	raw, err := json.Marshal(srcChunk{Cards: cards, Total: total})
	if err != nil {
		return adapter.ProgramBatch{}, err
	}
	ev, err := r.capture(domain.SourceProgram, "exclusive-src", "list", projectHallURL, "Butian 专属SRC", string(raw), at)
	if err != nil {
		return adapter.ProgramBatch{}, err
	}
	var batch adapter.ProgramBatch
	batch.Evidence = []domain.Evidence{ev}
	for _, card := range cards {
		prog, fs, err := cardProgram(card, ev.ID, at)
		if errors.Is(err, adapter.ErrUnsupported) {
			batch.Unsupported = append(batch.Unsupported, card.Name)
			continue
		}
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		batch.Programs = append(batch.Programs, prog)
		batch.Facts = append(batch.Facts, fs...)
	}
	batch.ObservedCount = len(batch.Programs)
	batch.EstimatedTotal = total
	batch.Estimated = false
	return batch, nil
}

func (r *Reader) extractCards(ctx context.Context) ([]srcCard, int, bool, error) {
	var all []srcCard
	total := 0
	noData := false
	for offset := 0; offset <= 100; offset += 8 {
		out, err := r.browser.Eval(ctx, extractCardsJS(offset, 8))
		if err != nil {
			return nil, 0, false, fmt.Errorf("butian: extract exclusive SRC cards: %w", err)
		}
		raw, err := unwrapEval(out)
		if err != nil {
			return nil, 0, false, err
		}
		var chunk srcChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return nil, 0, false, fmt.Errorf("butian: decode exclusive SRC cards: %w", err)
		}
		if offset == 0 {
			total = chunk.Total
			noData = chunk.NoData
		}
		if len(chunk.Cards) == 0 {
			break
		}
		all = append(all, chunk.Cards...)
		if total > 0 && offset+8 >= total {
			break
		}
	}
	return all, total, noData, nil
}

func extractCardsJS(offset, limit int) string {
	return fmt.Sprintf(`(() => {
  const cards=[...document.querySelectorAll('.plan-exclusive-src .company-card')];
  const out=cards.slice(%d,%d).map(c=>{
    const name=((c.querySelector('.name')||{}).textContent||'').trim();
    const text=(c.textContent||'').replace(/\s+/g,' ').trim();
    return {name,text:text.slice(0,220),buttons:[...c.querySelectorAll('button')].map(b=>(b.textContent||'').trim())};
  });
  return JSON.stringify({cards:out,total:cards.length,noData:(document.body.textContent||'').includes('暂无数据')});
})()`, offset, offset+limit)
}

func (r *Reader) SyncScope(ctx context.Context, id domain.ProgramID) (adapter.ScopeSnapshot, error) {
	at := r.now().UTC()
	url := projectHallURL
	text, ev, err := r.captureCurrentPage(ctx, "program-scope", domain.SourceProgram, "Butian 专属SRC", url, at)
	if err != nil {
		return adapter.ScopeSnapshot{}, err
	}
	_ = text
	scope := domain.Scope{
		Visibility: domain.ScopeGatedBySignup, Access: domain.ScopeAccessNotGranted,
		ContentsKnown: false, ObservedAt: at, Evidence: []domain.EvidenceID{ev.ID},
	}
	if err := scope.Validate(); err != nil {
		return adapter.ScopeSnapshot{}, fmt.Errorf("butian: invalid gated scope: %w", err)
	}
	fs := []domain.ObservedFact{
		cardFact(ev.ID, id, facts.PredScopeVisibility, string(domain.ScopeGatedBySignup), "", at),
		cardFact(ev.ID, id, "butian.scope.source", url, "", at),
	}
	return adapter.ScopeSnapshot{Scope: scope, Facts: fs, Evidence: []domain.Evidence{ev}}, nil
}

func (r *Reader) SyncPolicy(ctx context.Context, id domain.ProgramID) (adapter.PolicySnapshot, error) {
	at := r.now().UTC()
	_, ev, err := r.captureCurrentPage(ctx, "program-policy", domain.SourcePolicy, "Butian 专属SRC", projectHallURL, at)
	if err != nil {
		return adapter.PolicySnapshot{}, err
	}
	fs := []domain.ObservedFact{cardFact(ev.ID, id, "butian.policy.source", projectHallURL, "", at)}
	return adapter.PolicySnapshot{
		Automation: conservativeAutomation(), Constraints: domain.Constraints{},
		Facts: fs, Evidence: []domain.Evidence{ev},
	}, nil
}

func (r *Reader) SyncProgram(ctx context.Context, id domain.ProgramID) (adapter.ProgramSnapshot, error) {
	batch, err := r.SyncPrograms(ctx, adapter.SyncOptions{})
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
		return adapter.ProgramSnapshot{}, fmt.Errorf("butian: exclusive SRC %s not found: %w", id, adapter.ErrUnsupported)
	}
	scope, err := r.SyncScope(ctx, id)
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	prog.Scope = scope.Scope
	if err := prog.Validate(); err != nil {
		return adapter.ProgramSnapshot{}, fmt.Errorf("butian: invalid program %s after scope merge: %w", id, err)
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

func (r *Reader) readCenter(ctx context.Context, at time.Time) (string, domain.Evidence, error) {
	return r.readPage(ctx, centerURL, "account-center", domain.SourceAccount, "Butian 个人空间", at, true)
}

func (r *Reader) readSubmit(ctx context.Context, at time.Time) (string, domain.Evidence, error) {
	return r.readPage(ctx, submitURL, "account-submit-gate", domain.SourceAccount, "Butian 提交漏洞", at, false)
}

func (r *Reader) readPage(ctx context.Context, url, source string, sourceType domain.SourceType, title string, at time.Time, iframe bool) (string, domain.Evidence, error) {
	if err := r.browser.Open(ctx, url); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return "", domain.Evidence{}, fmt.Errorf("butian: open %s: %w", url, err)
	}
	_ = r.browser.WaitLoad(ctx)
	u, err := r.browser.GetURL(ctx)
	if err != nil {
		return "", domain.Evidence{}, fmt.Errorf("butian: url %s: %w", url, err)
	}
	if strings.Contains(u, "user.skyeye.qianxin.com") {
		return "", domain.Evidence{}, adapter.ErrAuthRequired
	}
	script := "(()=>document.body?(document.body.innerText||document.body.textContent||''):'')()"
	if iframe {
		script = `(() => { const f=document.querySelector('iframe'); try { if(f&&f.contentDocument&&f.contentDocument.body) return f.contentDocument.body.innerText||f.contentDocument.body.textContent||''; } catch(e){} return document.body?(document.body.innerText||document.body.textContent||''):''; })()`
	}
	text, err := r.evalString(ctx, script)
	if err != nil {
		return "", domain.Evidence{}, err
	}
	ev, err := r.capture(sourceType, source, "capture", u, title, text, at)
	if err != nil {
		return "", domain.Evidence{}, err
	}
	return text, ev, nil
}

func (r *Reader) captureCurrentPage(ctx context.Context, source string, sourceType domain.SourceType, title, url string, at time.Time) (string, domain.Evidence, error) {
	u, err := r.browser.GetURL(ctx)
	if err != nil {
		return "", domain.Evidence{}, fmt.Errorf("butian: current url: %w", err)
	}
	text, err := r.evalString(ctx, "(()=>document.body?(document.body.innerText||document.body.textContent||''):'')()")
	if err != nil {
		return "", domain.Evidence{}, err
	}
	ev, err := r.capture(sourceType, source, "capture", u, title, text, at)
	if err != nil {
		return "", domain.Evidence{}, err
	}
	return text, ev, nil
}

func (r *Reader) evalString(ctx context.Context, script string) (string, error) {
	out, err := r.browser.Eval(ctx, script)
	if err != nil {
		return "", fmt.Errorf("butian: eval: %w", err)
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
		return domain.Evidence{}, fmt.Errorf("butian: raw evidence dir is required")
	}
	id := domain.EvidenceID(fmt.Sprintf("butian/%s/%s/%s/%d", source, suffix, at.Format("20060102"), at.UnixNano()))
	rawRef := filepath.Join(r.rawDir, safeName(string(id))+".json")
	ev := domain.Evidence{
		ID: id, Platform: domain.PlatformButian, SourceType: sourceType,
		Transport: domain.TransportBrowserPage, URL: url, PageTitle: title,
		CapturedAt: at, RawRef: rawRef,
	}
	prepared, redacted, _, err := evidence.Prepare(ev, body)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("butian: prepare evidence: %w", err)
	}
	if err := os.MkdirAll(r.rawDir, 0o700); err != nil {
		return domain.Evidence{}, fmt.Errorf("butian: create raw dir: %w", err)
	}
	if err := os.WriteFile(rawRef, []byte(redacted), 0o600); err != nil {
		return domain.Evidence{}, fmt.Errorf("butian: write raw evidence: %w", err)
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
	return nil, fmt.Errorf("butian: unexpected eval output")
}

var _ adapter.Reader = (*Reader)(nil)
