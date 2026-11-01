package ledger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	entries map[string]domain.LedgerEntry
}

func newFakeStore() *fakeStore {
	return &fakeStore{entries: make(map[string]domain.LedgerEntry)}
}
func (s *fakeStore) GetLedgerEntry(ctx context.Context, id string) (domain.LedgerEntry, bool, error) {
	e, ok := s.entries[id]
	return e, ok, nil
}
func (s *fakeStore) PutLedgerEntry(ctx context.Context, e domain.LedgerEntry) error {
	s.entries[e.ID] = e
	return nil
}
func (s *fakeStore) ListLedgerEntries(ctx context.Context, platform domain.Platform, programID domain.ProgramID, since, until *time.Time) ([]domain.LedgerEntry, error) {
	var out []domain.LedgerEntry
	for _, e := range s.entries {
		if platform != "" && e.Platform != platform {
			continue
		}
		if programID != "" && e.ProgramID != programID {
			continue
		}
		if since != nil && e.OccurredAt.Before(*since) {
			continue
		}
		if until != nil && !e.OccurredAt.Before(*until) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

type fakeSubs struct {
	subs map[domain.SubmissionID]domain.Submission
}

func (s *fakeSubs) GetSubmission(ctx context.Context, id domain.SubmissionID) (domain.Submission, error) {
	sub, ok := s.subs[id]
	if !ok {
		return domain.Submission{}, errors.New("not found")
	}
	return sub, nil
}

type fakeEvidence struct {
	ids map[domain.EvidenceID]bool
}

func (e *fakeEvidence) HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error) {
	return e.ids[id], nil
}

func testService(t *testing.T, store *fakeStore, subs *fakeSubs, ev *fakeEvidence) (*Service, *audit.MemoryStore) {
	t.Helper()
	aud := audit.NewMemoryStore()
	s, err := New(Options{
		Store: store, Submissions: subs, Evidence: ev, Auditor: aud,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, aud
}

func baseEntry(id string, kind domain.LedgerKind, amount int64) domain.LedgerEntry {
	return domain.LedgerEntry{
		ID: id, Platform: domain.PlatformVulbox, ProgramID: "p1", Kind: kind,
		Amount: cny(amount), Status: domain.LedgerStatusRecorded,
		ExternalRef: "ext-" + id, OccurredAt: testNow,
		Evidence: []domain.EvidenceID{"ev1"},
	}
}

func paidEntry(id string, amount int64) domain.LedgerEntry {
	e := baseEntry(id, domain.LedgerPaid, amount)
	return e
}

func TestAppendPaidAndAudit(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	s, aud := testService(t, store, &fakeSubs{subs: map[domain.SubmissionID]domain.Submission{}}, ev)
	e, err := s.Append(context.Background(), paidEntry("l-paid", 10000))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "l-paid" || store.entries["l-paid"].Amount.Amount != 10000 {
		t.Fatalf("entry = %+v", e)
	}
	events, _ := aud.List(context.Background(), audit.Filter{Type: "ledger.entry.recorded"})
	if len(events) != 1 {
		t.Fatalf("audit = %+v", events)
	}
}

func TestAppendIdempotentAndConflict(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	s, _ := testService(t, store, &fakeSubs{}, ev)
	e := paidEntry("l-paid", 10000)
	if _, err := s.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), e); err != nil {
		t.Fatalf("idempotent re-append: %v", err)
	}
	e.Amount.Amount = 20000
	if _, err := s.Append(context.Background(), e); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestAppendRejectsDryRunSubmission(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	subID := domain.SubmissionID("sub-1")
	subs := &fakeSubs{subs: map[domain.SubmissionID]domain.Submission{
		subID: {ID: subID, ProgramID: "p1", Status: "DRY_RUN"},
	}}
	s, _ := testService(t, store, subs, ev)
	e := baseEntry("l-sub", domain.LedgerSubmitted, 1000)
	e.SubmissionID = &subID
	if _, err := s.Append(context.Background(), e); !errors.Is(err, ErrDryRunSubmission) {
		t.Fatalf("err = %v, want ErrDryRunSubmission", err)
	}
}

func TestAppendRejectsMissingEvidenceAndNonCNY(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{}}
	s, _ := testService(t, store, &fakeSubs{}, ev)
	if _, err := s.Append(context.Background(), paidEntry("l-paid", 100)); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("err = %v, want ErrEvidenceNotFound", err)
	}
	e := paidEntry("l-paid", 100)
	e.Amount.Currency = "USD"
	if _, err := s.Append(context.Background(), e); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("err = %v, want ErrUnsupportedCurrency", err)
	}
}

func TestReversalRules(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	s, _ := testService(t, store, &fakeSubs{}, ev)
	if _, err := s.Append(context.Background(), paidEntry("l-paid", 10000)); err != nil {
		t.Fatal(err)
	}
	rev := baseEntry("l-rev", domain.LedgerReversal, 3000)
	rev.ReversesID = "l-paid"
	if _, err := s.Append(context.Background(), rev); err != nil {
		t.Fatal(err)
	}
	tooMuch := baseEntry("l-rev2", domain.LedgerReversal, 8000)
	tooMuch.ReversesID = "l-paid"
	if _, err := s.Append(context.Background(), tooMuch); !errors.Is(err, ErrReversal) {
		t.Fatalf("err = %v, want ErrReversal", err)
	}

	accepted := baseEntry("l-accepted", domain.LedgerAccepted, 1000)
	fid := domain.FindingID("f1")
	accepted.FindingID = &fid
	if _, err := s.Append(context.Background(), accepted); err != nil {
		t.Fatal(err)
	}
	bad := baseEntry("l-rev3", domain.LedgerReversal, 100)
	bad.ReversesID = "l-accepted"
	if _, err := s.Append(context.Background(), bad); !errors.Is(err, ErrReversal) {
		t.Fatalf("reversal of ACCEPTED err = %v, want ErrReversal", err)
	}
}

func summaryEntries() []domain.LedgerEntry {
	subID := domain.SubmissionID("sub-1")
	fid := domain.FindingID("f1")
	entries := []domain.LedgerEntry{
		baseEntry("e-expected", domain.LedgerExpected, 1000),
		baseEntry("e-submitted", domain.LedgerSubmitted, 1000),
		baseEntry("e-accepted", domain.LedgerAccepted, 8000),
		baseEntry("e-fee", domain.LedgerFee, 500),
		baseEntry("e-tax", domain.LedgerTax, 1000),
		baseEntry("e-paid", domain.LedgerPaid, 6500),
		baseEntry("e-refund", domain.LedgerRefund, 200),
		baseEntry("e-reversal", domain.LedgerReversal, 500),
	}
	entries[1].SubmissionID = &subID
	entries[2].SubmissionID = &subID
	entries[3].SubmissionID = &subID
	entries[4].SubmissionID = &subID
	entries[2].FindingID = &fid
	entries[7].ReversesID = "e-paid"
	return entries
}

func TestSummarizeCashNet(t *testing.T) {
	s, err := Summarize(summaryEntries())
	if err != nil {
		t.Fatal(err)
	}
	if s.PaidNet.Amount != 6000 {
		t.Fatalf("paid net = %d, want 6000", s.PaidNet.Amount)
	}
	if s.Refunds.Amount != 200 || s.NetCash.Amount != 6200 {
		t.Fatalf("refunds/net cash = %d/%d", s.Refunds.Amount, s.NetCash.Amount)
	}
	if s.GrossAccepted.Amount != 8000 || s.Fees.Amount != 500 || s.Taxes.Amount != 1000 {
		t.Fatalf("accepted/fees/taxes = %d/%d/%d", s.GrossAccepted.Amount, s.Fees.Amount, s.Taxes.Amount)
	}
	if s.Submissions != 1 || s.Accepted != 1 || s.PaidCount != 1 {
		t.Fatalf("counts = %d/%d/%d", s.Submissions, s.Accepted, s.PaidCount)
	}
	if s.AvgAccepted.Amount != 8000 || s.AvgPaid.Amount != 6000 {
		t.Fatalf("averages = %d/%d", s.AvgAccepted.Amount, s.AvgPaid.Amount)
	}
}

func TestSummaryResolvesReversalTargetOutsidePeriod(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	target := paidEntry("l-paid", 10000)
	target.OccurredAt = testNow.Add(-48 * time.Hour)
	store.entries[target.ID] = target
	rev := baseEntry("l-rev", domain.LedgerReversal, 3000)
	rev.ReversesID = "l-paid"
	store.entries[rev.ID] = rev
	s, _ := testService(t, store, &fakeSubs{}, ev)
	since := testNow.Add(-24 * time.Hour)
	out, err := s.Summary(context.Background(), domain.PlatformVulbox, "p1", &since, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.PaidNet.Amount != -3000 {
		t.Fatalf("period paid net = %d, want -3000", out.PaidNet.Amount)
	}
}

func TestReversalRetryIsIdempotent(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	s, _ := testService(t, store, &fakeSubs{}, ev)
	if _, err := s.Append(context.Background(), paidEntry("l-paid", 10000)); err != nil {
		t.Fatal(err)
	}
	rev := baseEntry("l-rev", domain.LedgerReversal, 10000)
	rev.ReversesID = "l-paid"
	if _, err := s.Append(context.Background(), rev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), rev); err != nil {
		t.Fatalf("idempotent reversal retry: %v", err)
	}
}

func TestEntryIDsDistinguishExternalRefs(t *testing.T) {
	a := paidEntry("", 100)
	a.ExternalRef = "bank-a"
	b := paidEntry("", 100)
	b.ExternalRef = "bank-b"
	if entryID(a) == entryID(b) {
		t.Fatal("different external refs produced the same generated ID")
	}
	c := a
	if entryID(a) != entryID(c) {
		t.Fatal("identical entries must be idempotent")
	}
}

func TestPlatformWideSummary(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	a := paidEntry("a-paid", 1000)
	b := paidEntry("b-paid", 2000)
	b.ProgramID = "p2"
	store.entries[a.ID] = a
	store.entries[b.ID] = b
	s, _ := testService(t, store, &fakeSubs{}, ev)
	out, err := s.Summary(context.Background(), domain.PlatformVulbox, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Programs != 2 || out.PaidNet.Amount != 3000 || out.PaidCount != 2 {
		t.Fatalf("platform summary = %+v", out)
	}
}

func TestProgramSummaryProgramsIsOne(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	store.entries["p1-paid"] = paidEntry("p1-paid", 1000)
	s, _ := testService(t, store, &fakeSubs{}, ev)
	out, err := s.Summary(context.Background(), domain.PlatformVulbox, "p1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Programs != 1 {
		t.Fatalf("program summary Programs = %d, want 1", out.Programs)
	}
}

func TestReconcile(t *testing.T) {
	summary := Summary{Currency: "CNY", PaidNet: cny(6000)}
	platform := cny(6000)
	bank := cny(5900)
	out, err := Reconcile(summary, &platform, &bank)
	if err != nil {
		t.Fatal(err)
	}
	if out.Balanced {
		t.Fatal("bank difference should make the reconciliation unbalanced")
	}
	if out.PlatformDiff.Amount != 0 || out.BankDiff.Amount != -100 {
		t.Fatalf("diffs = %d/%d", out.PlatformDiff.Amount, out.BankDiff.Amount)
	}
}

func TestFeedbackGroupsByProgram(t *testing.T) {
	entries := summaryEntries()
	p2 := baseEntry("e-p2-paid", domain.LedgerPaid, 2000)
	p2.ProgramID = "p2"
	p2.OccurredAt = testNow
	entries = append(entries, p2)
	out, err := Feedback(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ProgramID != "p1" || out[1].ProgramID != "p2" {
		t.Fatalf("feedback = %+v", out)
	}
	if out[1].PaidNet.Amount != 2000 {
		t.Fatalf("p2 paid = %d", out[1].PaidNet.Amount)
	}
}

func TestSummarizeRejectsMixedPrograms(t *testing.T) {
	entries := []domain.LedgerEntry{paidEntry("a", 100), paidEntry("b", 100)}
	entries[1].ProgramID = "p2"
	if _, err := Summarize(entries); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("err = %v, want ErrInvalidEntry", err)
	}
}

func BenchmarkSummarize(b *testing.B) {
	entries := make([]domain.LedgerEntry, 0, 1000)
	for i := 0; i < 1000; i++ {
		e := paidEntry(fmt.Sprintf("e-%d", i), 100)

		e.ExternalRef = fmt.Sprintf("bank-%d", i)
		entries = append(entries, e)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Summarize(entries); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLegacyEntryIDIsIdempotent(t *testing.T) {
	store := newFakeStore()
	ev := &fakeEvidence{ids: map[domain.EvidenceID]bool{"ev1": true}}
	s, _ := testService(t, store, &fakeSubs{}, ev)
	e := paidEntry("", 10000)
	legacy := e
	legacy.ID = legacyEntryID(e)
	store.entries[legacy.ID] = legacy

	got, err := s.Append(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != legacy.ID {
		t.Fatalf("append returned %s, want legacy %s", got.ID, legacy.ID)
	}
	if len(store.entries) != 1 {
		t.Fatalf("legacy re-import created %d entries, want 1", len(store.entries))
	}
}
