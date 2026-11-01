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

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/ledger"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type ledgerOptions struct {
	File      string
	Persist   bool
	Summary   bool
	Reconcile bool
	Platform  string
	Program   string
	Since     string
	Until     string
}

type ledgerMoney struct {
	Amount   int64  `json:"amount_cents"`
	Currency string `json:"currency"`
}

func (m ledgerMoney) toDomain() domain.Money {
	return domain.Money{Amount: m.Amount, Currency: m.Currency}
}
func moneyPtr(m *ledgerMoney) *domain.Money {
	if m == nil {
		return nil
	}
	v := m.toDomain()
	return &v
}

type ledgerFile struct {
	Platform     domain.Platform      `json:"platform"`
	ProgramID    domain.ProgramID     `json:"program_id"`
	FindingID    *domain.FindingID    `json:"finding_id,omitempty"`
	SubmissionID *domain.SubmissionID `json:"submission_id,omitempty"`
	Kind         domain.LedgerKind    `json:"kind"`
	Amount       ledgerMoney          `json:"amount"`
	Status       string               `json:"status,omitempty"`
	OccurredAt   *time.Time           `json:"occurred_at,omitempty"`
	ExternalRef  string               `json:"external_ref,omitempty"`
	ReversesID   string               `json:"reverses_id,omitempty"`
	Notes        string               `json:"notes,omitempty"`
	Evidence     []domain.EvidenceID  `json:"evidence"`

	PlatformBalance *ledgerMoney `json:"platform_balance,omitempty"`
	BankAmount      *ledgerMoney `json:"bank_amount,omitempty"`
	Since           *time.Time   `json:"since,omitempty"`
	Until           *time.Time   `json:"until,omitempty"`
}

func (f ledgerFile) toEntry() domain.LedgerEntry {
	e := domain.LedgerEntry{
		Platform: f.Platform, ProgramID: f.ProgramID, FindingID: f.FindingID,
		SubmissionID: f.SubmissionID, Kind: f.Kind, Amount: f.Amount.toDomain(),
		Status: f.Status, ExternalRef: f.ExternalRef, ReversesID: f.ReversesID,
		Notes: f.Notes, Evidence: f.Evidence,
	}
	if f.OccurredAt != nil {
		e.OccurredAt = f.OccurredAt.UTC()
	}
	return e
}

type ledgerAppendOutput struct {
	Entry     domain.LedgerEntry `json:"entry"`
	Persisted bool               `json:"persisted"`
}

type ledgerRepo struct{ repo *postgres.Repo }

func (l ledgerRepo) GetLedgerEntry(ctx context.Context, id string) (domain.LedgerEntry, bool, error) {
	e, err := l.repo.GetLedgerEntry(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return domain.LedgerEntry{}, false, nil
	}
	if err != nil {
		return domain.LedgerEntry{}, false, err
	}
	return e, true, nil
}
func (l ledgerRepo) PutLedgerEntry(ctx context.Context, e domain.LedgerEntry) error {
	return l.repo.PutLedgerEntry(ctx, e)
}

// RecordLedgerEntryWithAudit lets ledger.Service write the entry and its audit
// event atomically and re-check reversals under the store's advisory lock.
// Without this method the wrapper falls back to the non-atomic, unlocked path.
func (l ledgerRepo) RecordLedgerEntryWithAudit(ctx context.Context, e domain.LedgerEntry, ev audit.Event) error {
	return l.repo.RecordLedgerEntryWithAudit(ctx, e, ev)
}
func (l ledgerRepo) ListLedgerEntries(ctx context.Context, platform domain.Platform, programID domain.ProgramID, since, until *time.Time) ([]domain.LedgerEntry, error) {
	return l.repo.ListLedgerEntries(ctx, platform, programID, since, until)
}
func (l ledgerRepo) GetSubmission(ctx context.Context, id domain.SubmissionID) (domain.Submission, error) {
	return l.repo.GetSubmission(ctx, id)
}
func (l ledgerRepo) HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error) {
	_, err := l.repo.GetEvidence(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func parseLedgerOptions(args []string) (ledgerOptions, error) {
	var o ledgerOptions
	fs := flag.NewFlagSet("ledger", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.File, "file", "", "JSON ledger entry or reconciliation input")
	fs.BoolVar(&o.Persist, "persist", false, "write an appended ledger entry")
	fs.BoolVar(&o.Summary, "summary", false, "print a ledger summary")
	fs.BoolVar(&o.Reconcile, "reconcile", false, "reconcile platform/bank balances")
	fs.StringVar(&o.Platform, "platform", "", "platform filter (vulbox/xianzhi/butian)")
	fs.StringVar(&o.Program, "program", "", "program filter")
	fs.StringVar(&o.Since, "since", "", "period start (RFC3339)")
	fs.StringVar(&o.Until, "until", "", "period end (RFC3339)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	modes := 0
	if o.Summary {
		modes++
	}
	if o.Reconcile {
		modes++
	}
	if o.File != "" && !o.Summary && !o.Reconcile {
		modes++
	}
	if modes != 1 {
		return o, errors.New("ledger: choose exactly one of -file, -summary or -reconcile")
	}
	if o.Summary && o.Platform == "" {
		return o, errors.New("ledger: -summary requires -platform")
	}
	if o.Platform != "" && !domain.Platform(o.Platform).Valid() {
		return o, fmt.Errorf("ledger: invalid platform %q", o.Platform)
	}
	if o.Summary && o.File != "" {
		return o, errors.New("ledger: use -summary or -file, not both")
	}
	if o.Reconcile && o.File == "" {
		return o, errors.New("ledger: -reconcile requires -file")
	}
	if o.Persist && (o.Summary || o.Reconcile) {
		return o, errors.New("ledger: -persist is only valid with an append file")
	}
	for _, s := range []string{o.Since, o.Until} {
		if s == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return o, fmt.Errorf("ledger: invalid period time %q", s)
		}
	}
	return o, nil
}

func runLedger(ctx context.Context, args []string) error {
	opts, err := parseLedgerOptions(args)
	if err != nil {
		return err
	}
	d, err := dsn()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, d)
	if err != nil {
		return err
	}
	defer pool.Close()
	lr := ledgerRepo{repo: postgres.NewRepo(pool)}
	svc, err := ledger.New(ledger.Options{Store: lr, Submissions: lr, Evidence: lr, Auditor: postgres.NewAuditStore(pool)})
	if err != nil {
		return err
	}

	var out any
	switch {
	case opts.Summary:
		since, until, err := parsePeriod(opts.Since, opts.Until)
		if err != nil {
			return err
		}
		out, err = svc.Summary(ctx, domain.Platform(opts.Platform), domain.ProgramID(opts.Program), since, until)
		if err != nil {
			return err
		}
	case opts.Reconcile:
		in, err := loadLedgerFile(opts.File)
		if err != nil {
			return err
		}
		platform, program, since, until := reconcileScope(in, opts)
		if platform != "" && !platform.Valid() {
			return fmt.Errorf("ledger: invalid platform %q", platform)
		}
		summary, err := svc.Summary(ctx, platform, program, since, until)
		if err != nil {
			return err
		}
		out, err = ledger.Reconcile(summary, moneyPtr(in.PlatformBalance), moneyPtr(in.BankAmount))
		if err != nil {
			return err
		}
	default:
		in, err := loadLedgerFile(opts.File)
		if err != nil {
			return err
		}
		entry := in.toEntry()
		if opts.Persist {
			entry, err = svc.Append(ctx, entry)
		} else {
			entry, err = svc.Validate(ctx, entry)
		}
		if err != nil {
			return err
		}
		out = ledgerAppendOutput{Entry: entry, Persisted: opts.Persist}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func loadLedgerFile(path string) (ledgerFile, error) {
	var in ledgerFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return in, fmt.Errorf("ledger: read file: %w", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("ledger: decode file: %w", err)
	}
	return in, nil
}

func parsePeriod(since, until string) (*time.Time, *time.Time, error) {
	var s, u *time.Time
	if since != "" {
		v, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, nil, err
		}
		v = v.UTC()
		s = &v
	}
	if until != "" {
		v, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return nil, nil, err
		}
		v = v.UTC()
		u = &v
	}
	return s, u, nil
}

func reconcileScope(in ledgerFile, opts ledgerOptions) (domain.Platform, domain.ProgramID, *time.Time, *time.Time) {
	platform := in.Platform
	if platform == "" {
		platform = domain.Platform(opts.Platform)
	}
	program := in.ProgramID
	if program == "" {
		program = domain.ProgramID(opts.Program)
	}
	since, until := in.Since, in.Until
	if since == nil && opts.Since != "" {
		if v, err := time.Parse(time.RFC3339, opts.Since); err == nil {
			v = v.UTC()
			since = &v
		}
	}
	if until == nil && opts.Until != "" {
		if v, err := time.Parse(time.RFC3339, opts.Until); err == nil {
			v = v.UTC()
			until = &v
		}
	}
	return platform, program, since, until
}
