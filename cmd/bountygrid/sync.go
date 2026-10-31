package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/adapter/vulbox"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/sidecar"
)

type syncOptions struct {
	Programs    bool
	Account     bool
	ProgramID   string
	Pages       int
	PageSize    int
	RawDir      string
	Session     string
	State       string
	Timeout     time.Duration
	MinInterval time.Duration
	Close       bool
}

type syncOutput struct {
	Platform string               `json:"platform"`
	Auth     string               `json:"auth"`
	Programs *programsOutput      `json:"programs,omitempty"`
	Account  *accountOutput       `json:"account,omitempty"`
	Detail   *programDetailOutput `json:"program,omitempty"`
	Evidence []string             `json:"evidence,omitempty"`
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
	fs.BoolVar(&o.Programs, "programs", true, "read the project hall list")
	fs.BoolVar(&o.Account, "account", false, "read the account (requires a logged-in session)")
	fs.StringVar(&o.ProgramID, "program", "", "read one enterprise SRC by native id (e.g. 444); implies scope+policy")
	fs.IntVar(&o.Pages, "pages", 1, "maximum project-list pages (1-152)")
	fs.IntVar(&o.PageSize, "page-size", 50, "project-list page size (10-100)")
	fs.StringVar(&o.RawDir, "raw-dir", ".evidence/vulbox", "local directory for redacted raw evidence")
	fs.StringVar(&o.Session, "session", "vbprobe", "agent-browser session name (default matches probes/session/relogin.sh)")
	fs.StringVar(&o.State, "state", "", "optional saved browser state JSON; usually the running session already holds the login")
	fs.DurationVar(&o.Timeout, "timeout", 60*time.Second, "per-command timeout")
	fs.DurationVar(&o.MinInterval, "min-interval", 2*time.Second, "minimum spacing between browser commands")
	fs.BoolVar(&o.Close, "close", false, "close the browser session when done (default: keep the login alive)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if !o.Programs && !o.Account && o.ProgramID == "" {
		return o, errors.New("sync: choose -programs, -account and/or -program")
	}
	if o.Pages < 1 || o.Pages > 152 {
		return o, errors.New("sync: -pages must be between 1 and 152")
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
	reader, client, err := newVulboxReader(opts)
	if err != nil {
		return err
	}
	if opts.Close {
		defer func() { _ = client.Close(context.Background()) }()
	}
	return executeSync(ctx, reader, opts, os.Stdout)
}

func newVulboxReader(opts syncOptions) (*vulbox.Reader, *sidecar.Client, error) {
	client, err := sidecar.NewClient(sidecar.Config{
		Session:     opts.Session,
		StatePath:   opts.State,
		Timeout:     opts.Timeout,
		MinInterval: opts.MinInterval,
	})
	if err != nil {
		return nil, nil, err
	}
	reader, err := vulbox.NewReader(client, vulbox.ReaderOptions{RawDir: opts.RawDir})
	if err != nil {
		_ = client.Close(context.Background())
		return nil, nil, err
	}
	return reader, client, nil
}

func executeSync(ctx context.Context, reader adapter.Reader, opts syncOptions, w io.Writer) error {
	out := syncOutput{Platform: string(reader.Platform()), Auth: "UNCHECKED"}
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
	}
	if opts.ProgramID != "" {
		pid := domain.ProgramID("vulbox/" + opts.ProgramID)
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
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
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
