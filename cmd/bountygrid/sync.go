package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/adapter/butian"
	"github.com/Zn070515/BountyGrid/internal/adapter/vulbox"
	"github.com/Zn070515/BountyGrid/internal/adapter/xianzhi"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/sidecar"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type syncOptions struct {
	Platform      string
	Programs      bool
	PublicWelfare bool
	Grow          bool
	Account       bool
	ProgramID     string
	Pages         int
	PageSize      int
	RawDir        string
	Session       string
	State         string
	Persist       bool
	Timeout       time.Duration
	MinInterval   time.Duration
	Close         bool
}

// snapshotWriter is the PostgreSQL subset used by sync -persist.
type snapshotWriter interface {
	PutEvidence(ctx context.Context, e domain.Evidence) error
	PutFacts(ctx context.Context, fs []domain.ObservedFact) error
	UpsertAccount(ctx context.Context, a domain.Account) error
	UpsertProgram(ctx context.Context, p domain.Program) error
}

type syncOutput struct {
	Platform      string               `json:"platform"`
	Auth          string               `json:"auth"`
	Persisted     bool                 `json:"persisted"`
	Programs      *programsOutput      `json:"programs,omitempty"`
	PublicWelfare *programsOutput      `json:"public_welfare,omitempty"`
	Account       *accountOutput       `json:"account,omitempty"`
	Detail        *programDetailOutput `json:"program,omitempty"`
	Grow          *programDetailOutput `json:"grow,omitempty"`
	Evidence      []string             `json:"evidence,omitempty"`
}

type programDetailOutput struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	Type                      string   `json:"type"`
	Status                    string   `json:"status"`
	Eligibility               string   `json:"eligibility"`
	ScopeVisibility           string   `json:"scope_visibility"`
	ScopeAccess               string   `json:"scope_access"`
	ContentsKnown             bool     `json:"contents_known"`
	Include                   []string `json:"include,omitempty"`
	Exclude                   []string `json:"exclude,omitempty"`
	ExcludeNotes              []string `json:"exclude_notes,omitempty"`
	FileUpload                string   `json:"file_upload"`
	AIAnalysis                string   `json:"ai_analysis"`
	HumanVerificationRequired bool     `json:"human_verification_required"`
	Facts                     int      `json:"facts"`
}

type programsOutput struct {
	Count          int              `json:"count"`
	EstimatedTotal int              `json:"estimated_total"`
	Estimated      bool             `json:"estimated"`
	Unsupported    []string         `json:"unsupported,omitempty"`
	Facts          int              `json:"facts"`
	Programs       []programSummary `json:"programs"`
}

type programSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Eligibility string `json:"eligibility"`
}

type accountOutput struct {
	ID               string  `json:"id"`
	Level            string  `json:"level,omitempty"`
	LevelExp         int     `json:"level_exp,omitempty"`
	Reputation       float64 `json:"reputation_score,omitempty"`
	SeasonRank       string  `json:"season_rank,omitempty"`
	IdentityVerified *bool   `json:"identity_verified,omitempty"`
	BankBound        *bool   `json:"bank_bound,omitempty"`
	CanSubmit        bool    `json:"can_submit"`
	CanPayout        bool    `json:"can_payout"`
	JoinedProjects   int     `json:"joined_projects"`
	Facts            int     `json:"facts"`
}

func parseSyncOptions(args []string) (syncOptions, error) {
	var o syncOptions
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Platform, "platform", "vulbox", "platform: vulbox, xianzhi or butian")
	fs.BoolVar(&o.Programs, "programs", true, "read the project/task list")
	fs.BoolVar(&o.PublicWelfare, "public-welfare", false, "butian only: read the public-welfare SRC vendor list (-pages limits the pages)")
	fs.BoolVar(&o.Grow, "grow", false, "butian only: read the GROW campus program rules and activity phase")
	fs.BoolVar(&o.Account, "account", false, "read the account (requires a logged-in session)")
	fs.StringVar(&o.ProgramID, "program", "", "read one program/task by native id (e.g. vulbox 444, xianzhi 324, butian SRC name); implies scope+policy")
	fs.IntVar(&o.Pages, "pages", 1, "maximum list pages (1-200; butian public welfare has 200)")
	fs.IntVar(&o.PageSize, "page-size", 50, "Vulbox list page size (10-100)")
	fs.StringVar(&o.RawDir, "raw-dir", "", "local directory for redacted raw evidence (default .evidence/<platform>)")
	fs.StringVar(&o.Session, "session", "", "agent-browser session (default vbprobe/xzprobe by platform)")
	fs.StringVar(&o.State, "state", "", "optional saved browser state JSON; usually the running session already holds the login")
	fs.BoolVar(&o.Persist, "persist", false, "write Evidence/Facts/Account/Program to PostgreSQL")
	fs.DurationVar(&o.Timeout, "timeout", 60*time.Second, "per-command timeout")
	fs.DurationVar(&o.MinInterval, "min-interval", 2*time.Second, "minimum spacing between browser commands")
	fs.BoolVar(&o.Close, "close", false, "close the browser session when done (default: keep the login alive)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch o.Platform {
	case "vulbox":
		if o.Session == "" {
			o.Session = "vbprobe"
		}
	case "xianzhi":
		if o.Session == "" {
			o.Session = "xzprobe"
		}
	case "butian":
		if o.Session == "" {
			o.Session = "btprobe"
		}
	default:
		return o, errors.New("sync: -platform must be vulbox, xianzhi or butian")
	}
	if (o.PublicWelfare || o.Grow) && o.Platform != "butian" {
		return o, errors.New("sync: -public-welfare and -grow are only supported for butian")
	}
	if o.RawDir == "" {
		o.RawDir = ".evidence/" + o.Platform
	}
	if !o.Programs && !o.PublicWelfare && !o.Grow && !o.Account && o.ProgramID == "" {
		return o, errors.New("sync: choose -programs, -public-welfare, -grow, -account and/or -program")
	}
	if o.Pages < 1 || o.Pages > 200 {
		return o, errors.New("sync: -pages must be between 1 and 200")
	}
	if o.PageSize < 10 || o.PageSize > 100 {
		return o, errors.New("sync: -page-size must be between 10 and 100")
	}
	if o.Session == "" {
		return o, errors.New("sync: -session is empty")
	}
	return o, nil
}

func runSync(ctx context.Context, args []string) error {
	opts, err := parseSyncOptions(args)
	if err != nil {
		return err
	}
	reader, client, err := newReader(opts)
	if err != nil {
		return err
	}
	if opts.Close {
		defer func() { _ = client.Close(context.Background()) }()
	}
	var writer snapshotWriter
	if opts.Persist {
		d, err := dsn()
		if err != nil {
			return err
		}
		pool, err := postgres.Open(ctx, d)
		if err != nil {
			return err
		}
		defer pool.Close()
		writer = postgres.NewRepo(pool)
	}
	return executeSync(ctx, reader, opts, os.Stdout, writer)
}

func newReader(opts syncOptions) (adapter.Reader, *sidecar.Client, error) {
	client, err := sidecar.NewClient(sidecar.Config{
		Session:     opts.Session,
		StatePath:   opts.State,
		Timeout:     opts.Timeout,
		MinInterval: opts.MinInterval,
	})
	if err != nil {
		return nil, nil, err
	}
	var reader adapter.Reader
	switch opts.Platform {
	case "vulbox":
		reader, err = vulbox.NewReader(client, vulbox.ReaderOptions{RawDir: opts.RawDir})
	case "xianzhi":
		reader, err = xianzhi.NewReader(client, xianzhi.ReaderOptions{RawDir: opts.RawDir})
	case "butian":
		reader, err = butian.NewReader(client, butian.ReaderOptions{RawDir: opts.RawDir})
	default:
		err = fmt.Errorf("sync: unsupported platform %q", opts.Platform)
	}
	if err != nil {
		_ = client.Close(context.Background())
		return nil, nil, err
	}
	return reader, client, nil
}

func executeSync(ctx context.Context, reader adapter.Reader, opts syncOptions, w io.Writer, writer snapshotWriter) error {
	out := syncOutput{Platform: string(reader.Platform()), Auth: "UNCHECKED", Persisted: writer != nil}
	if opts.Account {
		auth, err := reader.AuthStatus(ctx)
		if err != nil {
			return err
		}
		out.Auth = string(auth)
		if auth != adapter.AuthOK {
			return adapter.ErrAuthRequired
		}
	}
	if opts.Programs {
		batch, err := reader.SyncPrograms(ctx, adapter.SyncOptions{PageSize: opts.PageSize, MaxPages: opts.Pages})
		if err != nil {
			return err
		}
		out.Programs = summarizePrograms(batch)
		for _, ev := range batch.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		if writer != nil {
			if err := persistPrograms(ctx, writer, batch); err != nil {
				return err
			}
		}
	}
	if opts.PublicWelfare {
		pw, ok := reader.(adapter.PublicWelfareReader)
		if !ok {
			return fmt.Errorf("sync: platform %s does not expose public welfare", reader.Platform())
		}
		batch, err := pw.SyncPublicWelfare(ctx, adapter.SyncOptions{PageSize: opts.PageSize, MaxPages: opts.Pages})
		if err != nil {
			return err
		}
		out.PublicWelfare = summarizePrograms(batch)
		for _, ev := range batch.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		if writer != nil {
			if err := persistPrograms(ctx, writer, batch); err != nil {
				return err
			}
		}
	}
	if opts.Grow {
		gr, ok := reader.(adapter.GrowReader)
		if !ok {
			return fmt.Errorf("sync: platform %s does not expose a campus program", reader.Platform())
		}
		snap, err := gr.SyncGrow(ctx)
		if err != nil {
			return err
		}
		out.Grow = summarizeProgramSnapshot(snap)
		for _, ev := range snap.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		if writer != nil {
			if err := persistProgramSnapshot(ctx, writer, snap); err != nil {
				return err
			}
		}
	}
	if opts.Account {
		snap, err := reader.SyncAccount(ctx)
		if err != nil {
			return err
		}
		out.Account = summarizeAccount(snap)
		for _, ev := range snap.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		if writer != nil {
			if err := persistAccount(ctx, writer, snap); err != nil {
				return err
			}
		}
	}
	if opts.ProgramID != "" {
		platform := opts.Platform
		if platform == "" {
			platform = "vulbox"
		}
		pid := domain.ProgramID(platform + "/" + opts.ProgramID)
		snap, err := reader.SyncProgram(ctx, pid)
		if err != nil {
			return err
		}
		policy, err := reader.SyncPolicy(ctx, pid)
		if err != nil {
			return err
		}
		out.Detail = summarizeProgramDetail(snap, policy)
		for _, ev := range snap.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		for _, ev := range policy.Evidence {
			out.Evidence = append(out.Evidence, string(ev.ID))
		}
		if writer != nil {
			prog := snap.Program
			prog.Automation = policy.Automation
			prog.Constraints = policy.Constraints
			evs, err := uniqueEvidence(append(append([]domain.Evidence{}, snap.Evidence...), policy.Evidence...))
			if err != nil {
				return err
			}
			fs, err := uniqueFacts(append(append([]domain.ObservedFact{}, snap.Facts...), policy.Facts...))
			if err != nil {
				return err
			}
			if err := persistEvidenceFacts(ctx, writer, evs, fs); err != nil {
				return err
			}
			if err := prog.Validate(); err != nil {
				return fmt.Errorf("sync: persist program: %w", err)
			}
			if err := writer.UpsertProgram(ctx, prog); err != nil {
				return fmt.Errorf("sync: persist program %s: %w", prog.ID, err)
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func persistPrograms(ctx context.Context, w snapshotWriter, b adapter.ProgramBatch) error {
	if err := persistEvidenceFacts(ctx, w, b.Evidence, b.Facts); err != nil {
		return err
	}
	for _, p := range b.Programs {
		if err := w.UpsertProgram(ctx, p); err != nil {
			return fmt.Errorf("sync: persist program %s: %w", p.ID, err)
		}
	}
	return nil
}

func persistProgramSnapshot(ctx context.Context, w snapshotWriter, snap adapter.ProgramSnapshot) error {
	if err := persistEvidenceFacts(ctx, w, snap.Evidence, snap.Facts); err != nil {
		return err
	}
	prog := snap.Program
	if err := prog.Validate(); err != nil {
		return fmt.Errorf("sync: persist program: %w", err)
	}
	if err := w.UpsertProgram(ctx, prog); err != nil {
		return fmt.Errorf("sync: persist program %s: %w", prog.ID, err)
	}
	return nil
}

func persistAccount(ctx context.Context, w snapshotWriter, snap adapter.AccountSnapshot) error {
	if err := persistEvidenceFacts(ctx, w, snap.Evidence, snap.Facts); err != nil {
		return err
	}
	if err := w.UpsertAccount(ctx, snap.Account); err != nil {
		return fmt.Errorf("sync: persist account %s: %w", snap.Account.ID, err)
	}
	return nil
}

func persistEvidenceFacts(ctx context.Context, w snapshotWriter, evs []domain.Evidence, fs []domain.ObservedFact) error {
	uniqueEvs, err := uniqueEvidence(evs)
	if err != nil {
		return err
	}
	for _, ev := range uniqueEvs {
		if err := w.PutEvidence(ctx, ev); err != nil {
			return fmt.Errorf("sync: persist evidence %s: %w", ev.ID, err)
		}
	}
	uniqueFs, err := uniqueFacts(fs)
	if err != nil {
		return err
	}
	if len(uniqueFs) > 0 {
		if err := w.PutFacts(ctx, uniqueFs); err != nil {
			return fmt.Errorf("sync: persist facts: %w", err)
		}
	}
	return nil
}

// uniqueEvidence deduplicates exact repeats but rejects an ID reused for a
// different capture. Silently keeping the first would hide an extractor bug.
func uniqueEvidence(evs []domain.Evidence) ([]domain.Evidence, error) {
	seen := make(map[domain.EvidenceID]domain.Evidence, len(evs))
	out := make([]domain.Evidence, 0, len(evs))
	for _, ev := range evs {
		if prev, ok := seen[ev.ID]; ok {
			if prev.ContentHash != ev.ContentHash || prev.RawRef != ev.RawRef ||
				!prev.CapturedAt.Truncate(time.Microsecond).Equal(ev.CapturedAt.Truncate(time.Microsecond)) {
				return nil, fmt.Errorf("sync: evidence %s has conflicting content", ev.ID)
			}
			continue
		}
		seen[ev.ID] = ev
		out = append(out, ev)
	}
	return out, nil
}

// uniqueFacts mirrors the Fact Store contract: same ID + same content is an
// idempotent repeat; same ID + different content is an error.
func uniqueFacts(fs []domain.ObservedFact) ([]domain.ObservedFact, error) {
	seen := make(map[domain.FactID]domain.ObservedFact, len(fs))
	out := make([]domain.ObservedFact, 0, len(fs))
	for _, f := range fs {
		if prev, ok := seen[f.ID]; ok {
			if !prev.Equal(f) {
				return nil, fmt.Errorf("sync: fact %s has conflicting content", f.ID)
			}
			continue
		}
		seen[f.ID] = f
		out = append(out, f)
	}
	return out, nil
}

func summarizePrograms(b adapter.ProgramBatch) *programsOutput {
	out := &programsOutput{
		Count:          len(b.Programs),
		EstimatedTotal: b.EstimatedTotal,
		Estimated:      b.Estimated,
		Unsupported:    b.Unsupported,
		Facts:          len(b.Facts),
	}
	for _, p := range b.Programs {
		out.Programs = append(out.Programs, programSummary{
			ID: string(p.ID), Name: p.Name, Type: string(p.Type),
			Status: string(p.Status.Status), Eligibility: string(p.Eligibility.Decision),
		})
	}
	return out
}

func summarizeAccount(a adapter.AccountSnapshot) *accountOutput {
	acct := a.Account
	out := &accountOutput{
		ID:               string(acct.ID),
		Level:            acct.Level,
		IdentityVerified: acct.IdentityVerified,
		BankBound:        acct.BankBound,
		CanSubmit:        acct.Capability.CanSubmit,
		CanPayout:        acct.Capability.CanPayout,
		JoinedProjects:   factInt(a.Facts, "vulbox.account.joined_projects"),
		Facts:            len(a.Facts),
	}
	if acct.LevelExp != nil {
		out.LevelExp = *acct.LevelExp
	}
	if acct.ReputationScore != nil {
		out.Reputation = *acct.ReputationScore
	}
	if acct.Rank != nil {
		out.SeasonRank = *acct.Rank
	}
	return out
}

// summarizeProgramSnapshot reports a program that carries its own automation
// policy (no separate policy snapshot).
func summarizeProgramSnapshot(p adapter.ProgramSnapshot) *programDetailOutput {
	return summarizeProgramDetail(p, adapter.PolicySnapshot{Automation: p.Program.Automation})
}

func summarizeProgramDetail(p adapter.ProgramSnapshot, pol adapter.PolicySnapshot) *programDetailOutput {
	out := &programDetailOutput{
		ID:                        string(p.Program.ID),
		Name:                      p.Program.Name,
		Type:                      string(p.Program.Type),
		Status:                    string(p.Program.Status.Status),
		Eligibility:               string(p.Program.Eligibility.Decision),
		ScopeVisibility:           string(p.Scope.Visibility),
		ScopeAccess:               string(p.Scope.Access),
		ContentsKnown:             p.Scope.ContentsKnown,
		FileUpload:                string(pol.Automation.FileUpload),
		AIAnalysis:                string(pol.Automation.AIAnalysis),
		HumanVerificationRequired: pol.Automation.HumanVerificationRequired,
		Facts:                     len(p.Facts) + len(pol.Facts),
	}
	for _, a := range p.Scope.Include {
		out.Include = append(out.Include, a.Value)
	}
	for _, a := range p.Scope.Exclude {
		out.Exclude = append(out.Exclude, a.Value)
	}
	out.ExcludeNotes = factStrings(p.Facts, "vulbox.scope.exclude_notes")
	return out
}

func factStrings(fs []domain.ObservedFact, pred string) []string {
	for _, f := range fs {
		if f.Predicate != pred {
			continue
		}
		if v, ok := f.Value.([]string); ok {
			return v
		}
	}
	return nil
}

func factInt(fs []domain.ObservedFact, pred string) int {
	for _, f := range fs {
		if f.Predicate != pred {
			continue
		}
		switch v := f.Value.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}
