// Package ledger is the append-only cash revenue ledger. v1 records CNY cash
// events only; non-cash rewards (KB/RYB/points/honor/physical/coupons) are left
// to NonCashRecorder so they are never squeezed into a money amount.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/collections"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

var (
	ErrInvalidEntry        = errors.New("ledger: invalid entry")
	ErrConflict            = errors.New("ledger: entry conflict")
	ErrEvidenceNotFound    = errors.New("ledger: evidence not found")
	ErrDryRunSubmission    = errors.New("ledger: dry-run submission cannot be booked")
	ErrUnsupportedCurrency = errors.New("ledger: only CNY cash is supported")
	ErrReversal            = errors.New("ledger: invalid reversal")
	ErrReversalTarget      = errors.New("ledger: reversal target not found")
)

// NonCashReward is the v1 extension point for KB/RYB/points/honor/physical/
// coupons. It is intentionally not stored in ledger_entries.
type NonCashReward struct {
	Platform     domain.Platform
	ProgramID    domain.ProgramID
	SubmissionID *domain.SubmissionID
	Kind         string
	Units        int64
	Unit         string
	OccurredAt   time.Time
	ExternalRef  string
	Evidence     []domain.EvidenceID
}

type NonCashRecorder interface {
	Record(ctx context.Context, r NonCashReward) error
}

type Store interface {
	GetLedgerEntry(ctx context.Context, id string) (domain.LedgerEntry, bool, error)
	PutLedgerEntry(ctx context.Context, e domain.LedgerEntry) error
	ListLedgerEntries(ctx context.Context, platform domain.Platform, programID domain.ProgramID, since, until *time.Time) ([]domain.LedgerEntry, error)
}

type SubmissionReader interface {
	GetSubmission(ctx context.Context, id domain.SubmissionID) (domain.Submission, error)
}

type EvidenceChecker interface {
	HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error)
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

type Options struct {
	Store       Store
	Submissions SubmissionReader
	Evidence    EvidenceChecker
	Auditor     Auditor
	Now         func() time.Time
}

type Service struct {
	store       Store
	submissions SubmissionReader
	evidence    EvidenceChecker
	auditor     Auditor
	now         func() time.Time
}

func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Submissions == nil || opts.Evidence == nil {
		return nil, fmt.Errorf("%w: store/submissions/evidence required", ErrInvalidEntry)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store: opts.Store, submissions: opts.Submissions,
		evidence: opts.Evidence, auditor: opts.Auditor, now: opts.Now,
	}, nil
}

// Validate applies the ledger invariants (defaults, CNY, evidence existence,
// submission linkage, reversal limits) without writing anything.
func (s *Service) Validate(ctx context.Context, e domain.LedgerEntry) (domain.LedgerEntry, error) {
	now := s.now().UTC()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = now
	}
	if e.Status == "" {
		e.Status = domain.LedgerStatusRecorded
	}
	if e.ID == "" {
		e.ID = entryID(e)
	}
	if e.Amount.Currency != "CNY" {
		return e, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, e.Amount.Currency)
	}
	if err := e.Validate(); err != nil {
		return e, err
	}
	if len(e.Evidence) == 0 {
		return e, fmt.Errorf("%w: evidence is required", ErrInvalidEntry)
	}
	for _, id := range e.Evidence {
		ok, err := s.evidence.HasEvidence(ctx, id)
		if err != nil {
			return e, fmt.Errorf("ledger: check evidence %s: %w", id, err)
		}
		if !ok {
			return e, fmt.Errorf("%w: %s", ErrEvidenceNotFound, id)
		}
	}
	if err := s.checkSubmission(ctx, e); err != nil {
		return e, err
	}
	if e.Kind == domain.LedgerReversal {
		if err := s.checkReversal(ctx, e); err != nil {
			return e, err
		}
	}
	return e, nil
}

// Append validates and records one entry. Same ID + same content is
// idempotent; same ID + different content is a conflict.
func (s *Service) Append(ctx context.Context, e domain.LedgerEntry) (domain.LedgerEntry, error) {
	e, err := s.Validate(ctx, e)
	if err != nil {
		return e, err
	}
	now := s.now().UTC()

	existing, ok, err := s.store.GetLedgerEntry(ctx, e.ID)
	if err != nil {
		return e, err
	}
	if ok {
		if sameEntry(existing, e) {
			return existing, nil
		}
		return e, fmt.Errorf("%w: %s exists with different content", ErrConflict, e.ID)
	}
	// Entries written before the content-digest ID scheme have the legacy ID.
	// Re-importing them must be idempotent instead of creating a second row that
	// would double-count non-cash-external entries.
	if legacyID := legacyEntryID(e); legacyID != e.ID {
		legacy, lok, err := s.store.GetLedgerEntry(ctx, legacyID)
		if err != nil {
			return e, err
		}
		if lok {
			if sameEntryContent(legacy, e) {
				return legacy, nil
			}
			return e, fmt.Errorf("%w: legacy entry %s exists with different content", ErrConflict, legacyID)
		}
	}
	ev, err := s.auditEvent(e, now)
	if err != nil {
		return e, err
	}
	if rec, ok := s.store.(auditRecorder); ok {
		if err := rec.RecordLedgerEntryWithAudit(ctx, e, ev); err != nil {
			return e, err
		}
		return e, nil
	}
	if err := s.store.PutLedgerEntry(ctx, e); err != nil {
		return e, err
	}
	if s.auditor != nil {
		if _, err := s.auditor.Append(ctx, ev); err != nil {
			return e, fmt.Errorf("ledger: audit: %w", err)
		}
	}
	return e, nil
}

// auditRecorder is implemented by stores that can write the entry and its
// audit event atomically (PostgreSQL). The in-memory fake falls back to
// PutLedgerEntry + Auditor.
type auditRecorder interface {
	RecordLedgerEntryWithAudit(ctx context.Context, e domain.LedgerEntry, ev audit.Event) error
}

func (s *Service) checkSubmission(ctx context.Context, e domain.LedgerEntry) error {
	if e.SubmissionID == nil || *e.SubmissionID == "" {
		return nil
	}
	sub, err := s.submissions.GetSubmission(ctx, *e.SubmissionID)
	if err != nil {
		return fmt.Errorf("ledger: load submission %s: %w", *e.SubmissionID, err)
	}
	if sub.Status == domain.SubmissionDryRun {
		return fmt.Errorf("%w: %s", ErrDryRunSubmission, sub.ID)
	}
	if sub.ProgramID != e.ProgramID {
		return fmt.Errorf("%w: submission/program mismatch", ErrInvalidEntry)
	}
	return nil
}

func (s *Service) checkReversal(ctx context.Context, e domain.LedgerEntry) error {
	target, ok, err := s.store.GetLedgerEntry(ctx, e.ReversesID)
	if err != nil {
		return fmt.Errorf("ledger: load reversal target %s: %w", e.ReversesID, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrReversalTarget, e.ReversesID)
	}
	if target.Platform != e.Platform || target.ProgramID != e.ProgramID {
		return fmt.Errorf("%w: target entity mismatch", ErrReversal)
	}
	if target.Kind != domain.LedgerPaid && target.Kind != domain.LedgerRefund {
		return fmt.Errorf("%w: v1 only reverses PAID/REFUND, got %s", ErrReversal, target.Kind)
	}
	if target.Amount.Currency != e.Amount.Currency {
		return fmt.Errorf("%w: currency mismatch", ErrReversal)
	}
	entries, err := s.store.ListLedgerEntries(ctx, e.Platform, e.ProgramID, nil, nil)
	if err != nil {
		return fmt.Errorf("ledger: list reversals: %w", err)
	}
	var already int64
	for _, x := range entries {
		// Re-validating the identical entry (idempotent retry) must not count
		// its already-stored copy against the target.
		if x.ID == e.ID {
			continue
		}
		if x.Kind == domain.LedgerReversal && x.ReversesID == e.ReversesID {
			already += x.Amount.Amount
		}
	}
	if already+e.Amount.Amount > target.Amount.Amount {
		return fmt.Errorf("%w: reversals exceed target %s", ErrReversal, target.ID)
	}
	return nil
}

func (s *Service) auditEvent(e domain.LedgerEntry, now time.Time) (audit.Event, error) {
	payload, err := json.Marshal(struct {
		Kind       string `json:"kind"`
		Amount     int64  `json:"amount_cents"`
		Currency   string `json:"currency"`
		Status     string `json:"status"`
		ProgramID  string `json:"program_id"`
		Submission string `json:"submission_id,omitempty"`
		Reverses   string `json:"reverses_id,omitempty"`
		HasExtRef  bool   `json:"has_external_ref"`
	}{
		Kind: string(e.Kind), Amount: e.Amount.Amount, Currency: e.Amount.Currency,
		Status: e.Status, ProgramID: string(e.ProgramID),
		Submission: submissionIDString(e.SubmissionID), Reverses: e.ReversesID,
		HasExtRef: strings.TrimSpace(e.ExternalRef) != "",
	})
	if err != nil {
		return audit.Event{}, err
	}
	return audit.Event{
		Type: "ledger.entry.recorded", EntityType: "ledger", EntityID: e.ID,
		ActorKind: string(domain.ActorHuman), ActorID: "ledger",
		Payload: payload, RecordedAt: now,
	}, nil
}

func entryID(e domain.LedgerEntry) string {
	// Include a content discriminator: a platform statement can list multiple
	// entries for the same program/kind at the same date-only timestamp.
	key := e.ExternalRef
	if key == "" {
		key = fmt.Sprintf("%d|%s|%s|%s|%s|%s",
			e.Amount.Amount, e.Status, e.ReversesID,
			submissionIDString(e.SubmissionID), findingIDString(e.FindingID),
			strings.Join(evidenceStrings(e.Evidence), ","))
	}
	digest := domain.ParametersHash([]byte(key))
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return fmt.Sprintf("ledger/%s/%s/%s/%s/%s",
		e.Platform, e.ProgramID, e.Kind,
		e.OccurredAt.UTC().Format("20060102T150405.000000000"), digest)
}

// legacyEntryID is the pre-digest scheme. It is retained only for idempotent
// compatibility with rows written before the upgrade.
func legacyEntryID(e domain.LedgerEntry) string {
	return fmt.Sprintf("ledger/%s/%s/%s/%s",
		e.Platform, e.ProgramID, e.Kind,
		e.OccurredAt.UTC().Format("20060102T150405.000000000"))
}

func sameEntryContent(a, b domain.LedgerEntry) bool {
	a.ID = b.ID
	return sameEntry(a, b)
}

func findingIDString(id *domain.FindingID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

func evidenceStrings(ids []domain.EvidenceID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func submissionIDString(id *domain.SubmissionID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

func sameEntry(a, b domain.LedgerEntry) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

type Summary struct {
	Platform      domain.Platform
	ProgramID     domain.ProgramID
	Programs      int
	Currency      string
	Entries       int
	Submissions   int
	Accepted      int
	PaidCount     int
	Expected      domain.Money
	Submitted     domain.Money
	GrossAccepted domain.Money
	Fees          domain.Money
	Taxes         domain.Money
	Refunds       domain.Money
	// PaidNet is the net cash actually paid, after cash REVERSALs.
	PaidNet domain.Money
	// NetCash is PaidNet + Refunds (refunds are cash returned to the researcher).
	NetCash     domain.Money
	AvgAccepted domain.Money
	AvgPaid     domain.Money
}

// Summary loads a period and resolves reversal targets that fall outside it.
func (s *Service) Summary(ctx context.Context, platform domain.Platform, programID domain.ProgramID, since, until *time.Time) (Summary, error) {
	entries, err := s.store.ListLedgerEntries(ctx, platform, programID, since, until)
	if err != nil {
		return Summary{}, err
	}
	targets := make(map[string]domain.LedgerEntry)
	for _, e := range entries {
		if e.Kind != domain.LedgerReversal {
			targets[e.ID] = e
		}
	}
	for _, e := range entries {
		if e.Kind != domain.LedgerReversal {
			continue
		}
		if _, ok := targets[e.ReversesID]; ok {
			continue
		}
		target, ok, err := s.store.GetLedgerEntry(ctx, e.ReversesID)
		if err != nil {
			return Summary{}, err
		}
		if !ok {
			return Summary{}, fmt.Errorf("%w: %s", ErrReversalTarget, e.ReversesID)
		}
		targets[target.ID] = target
	}
	if programID != "" {
		out, err := summarize(entries, targets)
		if err != nil {
			return Summary{}, err
		}
		if out.Platform == "" {
			out.Platform = platform
			out.ProgramID = programID
		}
		// A program-scoped summary always covers exactly one program, even when
		// that program has no entries in the period.
		out.Programs = 1
		return out, nil
	}

	// Platform-wide summary: aggregate per program, because a single
	// program-scoped summary must not reject entries from other programs.
	groups := make(map[domain.ProgramID][]domain.LedgerEntry)
	for _, e := range entries {
		groups[e.ProgramID] = append(groups[e.ProgramID], e)
	}
	keys := collections.SortedKeys(groups)
	out := Summary{
		Platform: platform, Currency: "CNY", Programs: len(keys),
		Expected: cny(0), Submitted: cny(0), GrossAccepted: cny(0),
		Fees: cny(0), Taxes: cny(0), Refunds: cny(0),
		PaidNet: cny(0), NetCash: cny(0), AvgAccepted: cny(0), AvgPaid: cny(0),
	}
	for _, key := range keys {
		s, err := summarize(groups[key], targets)
		if err != nil {
			return Summary{}, err
		}
		out.Entries += s.Entries
		out.Submissions += s.Submissions
		out.Accepted += s.Accepted
		out.PaidCount += s.PaidCount
		out.Expected.Amount += s.Expected.Amount
		out.Submitted.Amount += s.Submitted.Amount
		out.GrossAccepted.Amount += s.GrossAccepted.Amount
		out.Fees.Amount += s.Fees.Amount
		out.Taxes.Amount += s.Taxes.Amount
		out.Refunds.Amount += s.Refunds.Amount
		out.PaidNet.Amount += s.PaidNet.Amount
		out.NetCash.Amount += s.NetCash.Amount
	}
	if out.Accepted > 0 {
		out.AvgAccepted.Amount = out.GrossAccepted.Amount / int64(out.Accepted)
	}
	if out.PaidCount > 0 {
		out.AvgPaid.Amount = out.PaidNet.Amount / int64(out.PaidCount)
	}
	return out, nil
}

// Summarize is the pure summary function; reversal targets must be in entries
// or supplied by the caller.
func Summarize(entries []domain.LedgerEntry) (Summary, error) {
	targets := make(map[string]domain.LedgerEntry, len(entries))
	for _, e := range entries {
		if e.Kind != domain.LedgerReversal {
			targets[e.ID] = e
		}
	}
	return summarize(entries, targets)
}

func summarize(entries []domain.LedgerEntry, targets map[string]domain.LedgerEntry) (Summary, error) {
	out := Summary{Currency: "CNY", Expected: cny(0), Submitted: cny(0), GrossAccepted: cny(0),
		Fees: cny(0), Taxes: cny(0), Refunds: cny(0), PaidNet: cny(0), NetCash: cny(0),
		AvgAccepted: cny(0), AvgPaid: cny(0)}
	out.Entries = len(entries)
	submissions := make(map[domain.SubmissionID]struct{})
	reversed := make(map[string]int64)
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return Summary{}, err
		}
		if e.Amount.Currency != "CNY" {
			return Summary{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, e.Amount.Currency)
		}
		if out.Platform == "" {
			out.Platform = e.Platform
			out.ProgramID = e.ProgramID
		} else if e.Platform != out.Platform || e.ProgramID != out.ProgramID {
			return Summary{}, fmt.Errorf("%w: entries span multiple platform/program", ErrInvalidEntry)
		}
		if e.Kind != domain.LedgerReversal {
			continue
		}
		target, ok := targets[e.ReversesID]
		if !ok {
			return Summary{}, fmt.Errorf("%w: %s", ErrReversalTarget, e.ReversesID)
		}
		if target.Kind != domain.LedgerPaid && target.Kind != domain.LedgerRefund {
			return Summary{}, fmt.Errorf("%w: summary only supports cash reversals", ErrReversal)
		}
		if target.Amount.Currency != e.Amount.Currency {
			return Summary{}, fmt.Errorf("%w: currency mismatch", ErrReversal)
		}
		reversed[target.ID] += e.Amount.Amount
	}
	for id, amount := range reversed {
		if amount > targets[id].Amount.Amount {
			return Summary{}, fmt.Errorf("%w: reversals exceed %s", ErrReversal, id)
		}
	}

	for _, e := range entries {
		switch e.Kind {
		case domain.LedgerReversal:
			target := targets[e.ReversesID]
			switch target.Kind {
			case domain.LedgerPaid:
				out.PaidNet.Amount -= e.Amount.Amount
			case domain.LedgerRefund:
				out.Refunds.Amount -= e.Amount.Amount
			}
		case domain.LedgerExpected:
			out.Expected.Amount += e.Amount.Amount
		case domain.LedgerSubmitted:
			out.Submitted.Amount += e.Amount.Amount
			if e.SubmissionID != nil && *e.SubmissionID != "" {
				submissions[*e.SubmissionID] = struct{}{}
			}
		case domain.LedgerAccepted:
			out.GrossAccepted.Amount += e.Amount.Amount
			out.Accepted++
		case domain.LedgerPaid:
			out.PaidNet.Amount += e.Amount.Amount
			out.PaidCount++
		case domain.LedgerFee:
			out.Fees.Amount += e.Amount.Amount
		case domain.LedgerTax:
			out.Taxes.Amount += e.Amount.Amount
		case domain.LedgerRefund:
			out.Refunds.Amount += e.Amount.Amount
		}
	}
	out.Submissions = len(submissions)
	out.NetCash.Amount = out.PaidNet.Amount + out.Refunds.Amount
	if out.Accepted > 0 {
		out.AvgAccepted.Amount = out.GrossAccepted.Amount / int64(out.Accepted)
	}
	if out.PaidCount > 0 {
		out.AvgPaid.Amount = out.PaidNet.Amount / int64(out.PaidCount)
	}
	return out, nil
}

type Reconciliation struct {
	Paid            domain.Money
	PlatformBalance *domain.Money
	PlatformDiff    *domain.Money
	BankAmount      *domain.Money
	BankDiff        *domain.Money
	Balanced        bool
}

// Reconcile compares a period's net paid cash with the platform statement and
// bank arrival. It reports differences; it never adjusts the ledger.
func Reconcile(summary Summary, platformBalance, bankAmount *domain.Money) (Reconciliation, error) {
	out := Reconciliation{Paid: summary.PaidNet, PlatformBalance: platformBalance, BankAmount: bankAmount, Balanced: true}
	if platformBalance != nil {
		if platformBalance.Currency != summary.Currency {
			return Reconciliation{}, fmt.Errorf("%w: platform balance %q", ErrUnsupportedCurrency, platformBalance.Currency)
		}
		d := domain.Money{Amount: platformBalance.Amount - summary.PaidNet.Amount, Currency: summary.Currency}
		out.PlatformDiff = &d
		if d.Amount != 0 {
			out.Balanced = false
		}
	}
	if bankAmount != nil {
		if bankAmount.Currency != summary.Currency {
			return Reconciliation{}, fmt.Errorf("%w: bank amount %q", ErrUnsupportedCurrency, bankAmount.Currency)
		}
		d := domain.Money{Amount: bankAmount.Amount - summary.PaidNet.Amount, Currency: summary.Currency}
		out.BankDiff = &d
		if d.Amount != 0 {
			out.Balanced = false
		}
	}
	return out, nil
}

type ProgramFeedback struct {
	Platform      domain.Platform
	ProgramID     domain.ProgramID
	Submitted     int
	Accepted      int
	PaidCount     int
	GrossAccepted domain.Money
	PaidNet       domain.Money
	NetCash       domain.Money
	Fees          domain.Money
	Taxes         domain.Money
	Refunds       domain.Money
	AvgAccepted   domain.Money
	AvgPaid       domain.Money
}

// Feedback derives the ledger-measurable metrics for the Opportunity Engine.
// Duplicate rate/rejection reasons are not derivable from the ledger; those
// come from Submission/platform status later.
func Feedback(entries []domain.LedgerEntry) ([]ProgramFeedback, error) {
	groups := make(map[string][]domain.LedgerEntry)
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		key := string(e.Platform) + "|" + string(e.ProgramID)
		groups[key] = append(groups[key], e)
	}
	keys := collections.SortedKeys(groups)
	out := make([]ProgramFeedback, 0, len(keys))
	for _, k := range keys {
		s, err := Summarize(groups[k])
		if err != nil {
			return nil, err
		}
		out = append(out, ProgramFeedback{
			Platform: s.Platform, ProgramID: s.ProgramID,
			Submitted: s.Submissions, Accepted: s.Accepted, PaidCount: s.PaidCount,
			GrossAccepted: s.GrossAccepted, PaidNet: s.PaidNet, NetCash: s.NetCash,
			Fees: s.Fees, Taxes: s.Taxes, Refunds: s.Refunds,
			AvgAccepted: s.AvgAccepted, AvgPaid: s.AvgPaid,
		})
	}
	return out, nil
}

func cny(amount int64) domain.Money {
	return domain.Money{Amount: amount, Currency: "CNY"}
}
