package vulbox

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

// Browser is the read-only subset of the sidecar the reader needs.
type Browser interface {
	Open(ctx context.Context, url string) error
	WaitLoad(ctx context.Context) error
	Eval(ctx context.Context, script string) ([]byte, error)
	GetURL(ctx context.Context) (string, error)
	GetTitle(ctx context.Context) (string, error)
}

type ReaderOptions struct {
	RawDir      string
	Now         func() time.Time
	ListPageURL string
}

type Reader struct {
	browser     Browser
	rawDir      string
	now         func() time.Time
	listPageURL string
	openedList  bool
}

func NewReader(b Browser, opts ReaderOptions) (*Reader, error) {
	if b == nil {
		return nil, fmt.Errorf("vulbox: browser is nil")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ListPageURL == "" {
		opts.ListPageURL = listPageURL
	}
	return &Reader{browser: b, rawDir: opts.RawDir, now: opts.Now, listPageURL: opts.ListPageURL}, nil
}

func (r *Reader) Platform() domain.Platform { return domain.PlatformVulbox }

// AuthStatus opens the user center and reports whether the saved session is
// still valid. It never attempts to log in.
func (r *Reader) AuthStatus(ctx context.Context) (adapter.AuthState, error) {
	if err := r.browser.Open(ctx, userHomeURL); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return adapter.AuthUnknown, fmt.Errorf("vulbox: auth open: %w", err)
	}
	if err := r.browser.WaitLoad(ctx); err != nil {
		return adapter.AuthUnknown, fmt.Errorf("vulbox: auth wait: %w", err)
	}
	finalURL, err := r.browser.GetURL(ctx)
	if err != nil {
		return adapter.AuthUnknown, fmt.Errorf("vulbox: auth url: %w", err)
	}
	if strings.Contains(finalURL, "user.vulbox.com") {
		return adapter.AuthOK, nil
	}
	return adapter.AuthRequired, nil
}

// SyncPrograms reads the project hall list through the page's own observed
// GET. It never clicks into a project or submits anything.
func (r *Reader) SyncPrograms(ctx context.Context, opts adapter.SyncOptions) (adapter.ProgramBatch, error) {
	pageSize := opts.PageSize
	if pageSize < 10 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}
	if err := r.openList(ctx); err != nil {
		return adapter.ProgramBatch{}, err
	}

	var batch adapter.ProgramBatch
	for page := 1; page <= maxPages; page++ {
		apiURL := fmt.Sprintf("%s/web/project/projects?type=-1&status=0&flag=0&search=&page=%d&page_size=%d&direction=desc&order_by=normal&limit=",
			apiBase, page, pageSize)
		body, status, err := r.fetchJSON(ctx, apiURL)
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		if status != 200 {
			return adapter.ProgramBatch{}, fmt.Errorf("vulbox: project list HTTP %d", status)
		}
		var resp projectListResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return adapter.ProgramBatch{}, fmt.Errorf("vulbox: decode project list: %w", err)
		}
		if resp.Code != 200 {
			return adapter.ProgramBatch{}, fmt.Errorf("vulbox: project list code %d: %s", resp.Code, resp.Message)
		}
		at := r.now().UTC()
		ev, err := r.capture(domain.SourceProgram, "program-list", fmt.Sprintf("page-%d", page),
			apiBase+"/web/project/projects", "Vulbox 项目大厅", string(body), at)
		if err != nil {
			return adapter.ProgramBatch{}, err
		}
		batch.Evidence = append(batch.Evidence, ev)
		for _, p := range resp.Data.Data {
			if p.ID == "" {
				continue
			}
			prog, fs, err := programFromProject(p, ev.ID, at)
			if errors.Is(err, adapter.ErrUnsupported) {
				batch.Unsupported = append(batch.Unsupported, string(p.ID))
				continue
			}
			if err != nil {
				return adapter.ProgramBatch{}, err
			}
			batch.Programs = append(batch.Programs, prog)
			batch.Facts = append(batch.Facts, fs...)
		}
		batch.ObservedCount = len(batch.Programs)
		batch.EstimatedTotal = resp.Data.Total
		// The platform count is derived from pagination/category sums; keep it
		// marked as estimated even when a single page is fetched.
		batch.Estimated = true
		if resp.Data.CurrentPage >= resp.Data.LastPage || len(resp.Data.Data) == 0 {
			break
		}
	}
	return batch, nil
}

// SyncAccount reads the user center, personal center and joined projects. It
// requires an authenticated session and never attempts to log in. The first
// readPage call detects the login redirect, so callers do not need a separate
// AuthStatus round-trip.
func (r *Reader) SyncAccount(ctx context.Context) (adapter.AccountSnapshot, error) {
	at := r.now().UTC()

	homeText, homeEv, err := r.readPage(ctx, userHomeURL, "account-home", domain.SourceAccount, "Vulbox 控制台", at, true)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}
	profileText, profileEv, err := r.readPage(ctx, profileURL, "account-profile", domain.SourceAccount, "Vulbox 个人中心", at, true)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}
	joinedText, joinedEv, err := r.readPage(ctx, myProjectsURL, "account-projects", domain.SourceAccount, "Vulbox 我的项目", at, true)
	if err != nil {
		return adapter.AccountSnapshot{}, err
	}

	home := parseAccountText(homeText)
	identity, bank := parseIdentityText(profileText)
	names, joinedCount := parseJoinedProjects(joinedText)

	acct := domain.Account{
		ID:               "vulbox/account",
		Platform:         domain.PlatformVulbox,
		Registered:       true,
		IdentityVerified: identity,
		Level:            home.Level,
		BankBound:        bank,
		ObservedAt:       at,
	}
	if home.Exp > 0 {
		v := home.Exp
		acct.LevelExp = &v
	}
	if home.Reputation > 0 {
		v := float64(home.Reputation)
		acct.ReputationScore = &v
		acct.ReputationMetric = "信誉值"
	}
	if home.SeasonRank != "" {
		v := home.SeasonRank
		acct.Rank = &v
	}

	var gates []domain.EligibilityGate
	if identity != nil {
		satisfied := domain.GateSatisfied
		if !*identity {
			satisfied = domain.GateUnsatisfied
		}
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateIdentityVerified, Required: true,
			Satisfied: satisfied, Blocking: true,
			Evidence: []domain.EvidenceID{profileEv.ID},
		})
	}
	if bank != nil && !*bank {
		gates = append(gates, domain.EligibilityGate{
			Kind: domain.GateBankBinding, Required: true,
			Satisfied: domain.GateUnsatisfied, Blocking: true,
			Evidence: []domain.EvidenceID{profileEv.ID},
		})
	}
	acct.Gates = gates
	acct.Capability = accountstate.DeriveAccountCapability(gates, identity, nil, bank, at)

	fs := accountFacts(acct, home, homeText, names, joinedCount, homeEv.ID, profileEv.ID, joinedEv.ID, at)
	if err := acct.Validate(); err != nil {
		return adapter.AccountSnapshot{}, fmt.Errorf("vulbox: invalid account: %w", err)
	}
	return adapter.AccountSnapshot{
		Account:  acct,
		Facts:    fs,
		Evidence: []domain.Evidence{homeEv, profileEv, joinedEv},
	}, nil
}

func accountFacts(a domain.Account, home accountPage, homeText string, names []string, joinedCount int,
	homeEv, profileEv, joinedEv domain.EvidenceID, at time.Time) []domain.ObservedFact {
	var out []domain.ObservedFact
	add := func(ev domain.EvidenceID, pred string, v any, unit string) {
		out = append(out, domain.ObservedFact{
			ID:               domain.FactID(fmt.Sprintf("%s/%s", ev, strings.ReplaceAll(pred, ".", "_"))),
			Subject:          domain.SubjectAccount,
			Platform:         domain.PlatformVulbox,
			AccountID:        &a.ID,
			Predicate:        pred,
			Value:            v,
			Unit:             unit,
			ObservedAt:       at,
			EvidenceRefs:     []domain.EvidenceID{ev},
			Confidence:       1,
			ExtractorVersion: extractor,
		})
	}
	if home.Level != "" {
		add(homeEv, "vulbox.account.level", home.Level, "")
	}
	if home.Exp > 0 {
		add(homeEv, "vulbox.account.exp", home.Exp, "")
	}
	if home.Reputation > 0 {
		add(homeEv, "vulbox.account.reputation", home.Reputation, "")
	}
	if home.Season != "" {
		add(homeEv, "vulbox.account.season", home.Season, "")
	}
	if strings.Contains(homeText, "赛季积分") {
		add(homeEv, "vulbox.account.season_points", home.SeasonPoints, "")
	}
	if home.SeasonRank != "" {
		add(homeEv, "vulbox.account.season_rank", home.SeasonRank, "")
	}
	if strings.Contains(homeText, "账户余额") {
		add(homeEv, "vulbox.account.balance_cny", home.BalanceCNY, "")
	}
	if strings.Contains(homeText, "待审阅") {
		add(homeEv, "vulbox.account.pending_review", home.PendingReview, "")
	}
	if a.IdentityVerified != nil {
		add(profileEv, facts.PredIdentityVerified, *a.IdentityVerified, "")
	}
	if a.BankBound != nil {
		add(profileEv, facts.PredBankBound, *a.BankBound, "")
	}
	add(joinedEv, "vulbox.account.joined_projects", joinedCount, "")
	if len(names) > 0 {
		add(joinedEv, "vulbox.account.joined_project_names", names, "")
	}
	return out
}

func (r *Reader) openList(ctx context.Context) error {
	if r.openedList {
		return nil
	}
	if err := r.browser.Open(ctx, r.listPageURL); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return fmt.Errorf("vulbox: open project list: %w", err)
	}
	if err := r.browser.WaitLoad(ctx); err != nil {
		return fmt.Errorf("vulbox: wait project list: %w", err)
	}
	r.openedList = true
	return nil
}

func (r *Reader) fetchJSON(ctx context.Context, apiURL string) ([]byte, int, error) {
	script := fmt.Sprintf(`(async()=>{const r=await fetch(%q,{method:"GET",credentials:"include"});const t=await r.text();return JSON.stringify({status:r.status,body:t});})()`, apiURL)
	out, err := r.browser.Eval(ctx, script)
	if err != nil {
		return nil, 0, fmt.Errorf("vulbox: observed GET %s: %w", apiURL, err)
	}
	raw, err := unwrapEval(out)
	if err != nil {
		return nil, 0, err
	}
	var env struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, 0, fmt.Errorf("vulbox: decode observed GET: %w", err)
	}
	return []byte(env.Body), env.Status, nil
}

// unwrapEval accepts agent-browser output that is either a JSON string
// containing JSON or a bare JSON object.
func unwrapEval(b []byte) ([]byte, error) {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return []byte(s), nil
	}
	if json.Valid(b) {
		return b, nil
	}
	return nil, fmt.Errorf("vulbox: unexpected eval output")
}

func (r *Reader) pageText(ctx context.Context) (string, error) {
	out, err := r.browser.Eval(ctx, "(()=>document.body?document.body.innerText:'')()")
	if err != nil {
		return "", fmt.Errorf("vulbox: page text: %w", err)
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

func (r *Reader) closeOverlays(ctx context.Context) {
	// Promotional drawers may open after navigation. Wait for them, close them,
	// then wait until the profile identity text is visible (best-effort).
	script := `(async()=>{
		let clicked=0;
		for(let i=0;i<60;i++){
			const closers=[...document.querySelectorAll('.anticon-close, .mask.close, .drawer .close, .ant-modal-close')];
			if(closers.length){
				for(const e of closers){ try{ e.click(); clicked++; }catch(_){} }
				break;
			}
			await new Promise(r=>setTimeout(r,100));
		}
		for(let i=0;i<50;i++){
			if((document.body.innerText||'').includes('已实名')) break;
			await new Promise(r=>setTimeout(r,100));
		}
		return JSON.stringify({clicked});
	})()`
	_, _ = r.browser.Eval(ctx, script)
}

func (r *Reader) readPage(ctx context.Context, url, source string, sourceType domain.SourceType, title string, at time.Time, requireUserCenter bool) (string, domain.Evidence, error) {
	if err := r.browser.Open(ctx, url); err != nil && !errors.Is(err, sidecar.ErrNavigationTimeout) {
		return "", domain.Evidence{}, fmt.Errorf("vulbox: open %s: %w", url, err)
	}
	if err := r.browser.WaitLoad(ctx); err != nil {
		return "", domain.Evidence{}, fmt.Errorf("vulbox: wait %s: %w", url, err)
	}
	// User-center pages open promotional drawers over the profile. Dismiss
	// them read-only so identity/joined data is not hidden behind the overlay.
	r.closeOverlays(ctx)
	finalURL, err := r.browser.GetURL(ctx)
	if err != nil {
		return "", domain.Evidence{}, fmt.Errorf("vulbox: url %s: %w", url, err)
	}
	if requireUserCenter && !strings.Contains(finalURL, "user.vulbox.com") {
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

func (r *Reader) capture(sourceType domain.SourceType, source, suffix, url, title, body string, at time.Time) (domain.Evidence, error) {
	if r.rawDir == "" {
		return domain.Evidence{}, fmt.Errorf("vulbox: raw evidence dir is required")
	}
	id := domain.EvidenceID(fmt.Sprintf("vulbox/%s/%s/%s/%d", source, suffix, at.Format("20060102"), at.UnixNano()))
	rawRef := filepath.Join(r.rawDir, safeName(string(id))+".json")
	ev := domain.Evidence{
		ID:         id,
		Platform:   domain.PlatformVulbox,
		SourceType: sourceType,
		Transport:  domain.TransportBrowserObservedAPI,
		URL:        url,
		PageTitle:  title,
		CapturedAt: at,
		RawRef:     rawRef,
	}
	prepared, redacted, _, err := evidence.Prepare(ev, body)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("vulbox: prepare evidence: %w", err)
	}
	if err := os.MkdirAll(r.rawDir, 0o700); err != nil {
		return domain.Evidence{}, fmt.Errorf("vulbox: create raw dir: %w", err)
	}
	if err := os.WriteFile(rawRef, []byte(redacted), 0o600); err != nil {
		return domain.Evidence{}, fmt.Errorf("vulbox: write raw evidence: %w", err)
	}
	return prepared, nil
}

func safeName(s string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "?", "_", "&", "_", "=", "_")
	return repl.Replace(s)
}

func (r *Reader) findProject(ctx context.Context, nativeID string) (apiProject, error) {
	if err := r.ensureListHost(ctx); err != nil {
		return apiProject{}, err
	}
	apiURL := fmt.Sprintf("%s/web/project/projects?type=7&status=0&flag=0&search=&page=1&page_size=100&direction=desc&order_by=normal&limit=", apiBase)
	body, status, err := r.fetchJSON(ctx, apiURL)
	if err != nil {
		return apiProject{}, err
	}
	if status != 200 {
		return apiProject{}, fmt.Errorf("vulbox: enterprise SRC list HTTP %d", status)
	}
	var resp projectListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return apiProject{}, fmt.Errorf("vulbox: decode enterprise SRC list: %w", err)
	}
	if resp.Code != 200 {
		return apiProject{}, fmt.Errorf("vulbox: enterprise SRC list code %d: %s", resp.Code, resp.Message)
	}
	for _, p := range resp.Data.Data {
		if string(p.ID) == nativeID {
			return p, nil
		}
	}
	return apiProject{}, fmt.Errorf("vulbox: project %s is not an enterprise SRC in the current list: %w", nativeID, adapter.ErrUnsupported)
}

func (r *Reader) ensureListHost(ctx context.Context) error {
	if u, err := r.browser.GetURL(ctx); err == nil {
		if !strings.Contains(u, "www.vulbox.com") && !strings.Contains(u, "user.vulbox.com") {
			r.openedList = false
		}
	}
	return r.openList(ctx)
}

func (r *Reader) SyncScope(ctx context.Context, id domain.ProgramID) (adapter.ScopeSnapshot, error) {
	native := strings.TrimPrefix(string(id), "vulbox/")
	p, err := r.findProject(ctx, native)
	if err != nil {
		return adapter.ScopeSnapshot{}, err
	}
	at := r.now().UTC()
	url := projectURL(p)
	text, ev, err := r.readPage(ctx, url, "program-scope", domain.SourceProgram, p.TaskTitle, at, false)
	if err != nil {
		return adapter.ScopeSnapshot{}, err
	}
	include, exclude, notes, found := parseSRCScope(text)
	if !found || len(include) == 0 {
		if strings.Contains(text, "保密承诺书") || strings.Contains(text, "NDA") {
			scope := domain.Scope{
				Visibility:    domain.ScopeGatedByNDA,
				Access:        domain.ScopeAccessNotGranted,
				ContentsKnown: false,
				ObservedAt:    at,
				Evidence:      []domain.EvidenceID{ev.ID},
			}
			if err := scope.Validate(); err != nil {
				return adapter.ScopeSnapshot{}, fmt.Errorf("vulbox: invalid gated scope: %w", err)
			}
			fs := []domain.ObservedFact{projectFact(ev.ID, id, facts.PredScopeVisibility, string(domain.ScopeGatedByNDA), "", at)}
			return adapter.ScopeSnapshot{Scope: scope, Facts: fs, Evidence: []domain.Evidence{ev}}, nil
		}
		return adapter.ScopeSnapshot{}, fmt.Errorf("vulbox: asset scope not parseable for %s: %w", id, adapter.ErrUnsupported)
	}
	scope := domain.Scope{
		Visibility:    domain.ScopePublic,
		Access:        domain.ScopeAccessGranted,
		ContentsKnown: true,
		Include:       scopeAssets(id, include, true, ev.ID),
		Exclude:       scopeAssets(id, exclude, false, ev.ID),
		ObservedAt:    at,
		Evidence:      []domain.EvidenceID{ev.ID},
	}
	if err := scope.Validate(); err != nil {
		return adapter.ScopeSnapshot{}, fmt.Errorf("vulbox: invalid scope for %s: %w", id, err)
	}
	fs := []domain.ObservedFact{
		projectFact(ev.ID, id, facts.PredScopeVisibility, string(domain.ScopePublic), "", at),
		projectFact(ev.ID, id, "vulbox.scope.include", include, "", at),
		projectFact(ev.ID, id, "vulbox.scope.exclude", exclude, "", at),
		projectFact(ev.ID, id, "vulbox.scope.source", url, "", at),
	}
	if len(notes) > 0 {
		fs = append(fs, projectFact(ev.ID, id, "vulbox.scope.exclude_notes", notes, "", at))
	}
	return adapter.ScopeSnapshot{Scope: scope, Facts: fs, Evidence: []domain.Evidence{ev}}, nil
}

func scopeAssets(pid domain.ProgramID, patterns []string, inScope bool, evID domain.EvidenceID) []domain.Asset {
	native := strings.TrimPrefix(string(pid), "vulbox/")
	out := make([]domain.Asset, 0, len(patterns))
	for _, pat := range patterns {
		out = append(out, domain.Asset{
			ID:        domain.AssetID("vulbox/scope/" + native + "/" + pat),
			Platform:  domain.PlatformVulbox,
			ProgramID: pid,
			Kind:      domain.AssetDomain,
			Value:     pat,
			InScope:   inScope,
			Source:    evID,
		})
	}
	return out
}

func (r *Reader) SyncPolicy(ctx context.Context, id domain.ProgramID) (adapter.PolicySnapshot, error) {
	native := strings.TrimPrefix(string(id), "vulbox/")
	p, err := r.findProject(ctx, native)
	if err != nil {
		return adapter.PolicySnapshot{}, err
	}
	at := r.now().UTC()
	text, ev, err := r.readPage(ctx, projectURL(p), "program-policy", domain.SourcePolicy, p.TaskTitle, at, false)
	if err != nil {
		return adapter.PolicySnapshot{}, err
	}
	sp := parseSRCPolicy(text)
	auto := conservativeAutomation()
	fs := []domain.ObservedFact{projectFact(ev.ID, id, "vulbox.policy.source", projectURL(p), "", at)}
	if sp.FileUpload != "" {
		auto.FileUpload = sp.FileUpload
		fs = append(fs, projectFact(ev.ID, id, "vulbox.policy.file_upload", string(sp.FileUpload), "", at))
	}
	if sp.AIAnalysis != "" {
		auto.AIAnalysis = sp.AIAnalysis
		fs = append(fs, projectFact(ev.ID, id, "vulbox.policy.ai_analysis", string(sp.AIAnalysis), "", at))
	}
	if sp.HumanVerificationRequired {
		auto.HumanVerificationRequired = true
	}
	return adapter.PolicySnapshot{
		Automation:  auto,
		Constraints: domain.Constraints{},
		Facts:       fs,
		Evidence:    []domain.Evidence{ev},
	}, nil
}

func (r *Reader) SyncProgram(ctx context.Context, id domain.ProgramID) (adapter.ProgramSnapshot, error) {
	native := strings.TrimPrefix(string(id), "vulbox/")
	p, err := r.findProject(ctx, native)
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	at := r.now().UTC()
	scopeSnap, err := r.SyncScope(ctx, id)
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	evID := scopeSnap.Evidence[0].ID
	prog, pFacts, err := programFromProject(p, evID, at)
	if err != nil {
		return adapter.ProgramSnapshot{}, err
	}
	prog.Scope = scopeSnap.Scope
	if err := prog.Validate(); err != nil {
		return adapter.ProgramSnapshot{}, fmt.Errorf("vulbox: invalid program %s after scope merge: %w", id, err)
	}
	filtered := pFacts[:0]
	for _, f := range pFacts {
		if f.Predicate != facts.PredScopeVisibility {
			filtered = append(filtered, f)
		}
	}
	pFacts = append(filtered, scopeSnap.Facts...)
	return adapter.ProgramSnapshot{
		Program:  prog,
		Scope:    scopeSnap.Scope,
		Facts:    pFacts,
		Evidence: scopeSnap.Evidence,
	}, nil
}

var _ adapter.Reader = (*Reader)(nil)
