package main

import (
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func TestLedgerFileToEntry(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := ledgerFile{
		Platform: domain.PlatformVulbox, ProgramID: "p1",
		Kind: domain.LedgerPaid, Amount: ledgerMoney{Amount: 10000, Currency: "CNY"},
		Status: domain.LedgerStatusRecorded, OccurredAt: &now,
		ExternalRef: "bank-1", Evidence: []domain.EvidenceID{"ev1"},
	}
	e := f.toEntry()
	if e.Amount.Amount != 10000 || e.Amount.Currency != "CNY" ||
		e.ExternalRef != "bank-1" || !e.OccurredAt.Equal(now) {
		t.Fatalf("entry = %+v", e)
	}
}

func TestParseLedgerOptions(t *testing.T) {
	opts, err := parseLedgerOptions([]string{"-file", "l.json", "-persist"})
	if err != nil || opts.File != "l.json" || !opts.Persist {
		t.Fatalf("append opts = %+v err=%v", opts, err)
	}
	opts, err = parseLedgerOptions([]string{"-summary", "-platform", "vulbox", "-since", "2026-11-01T00:00:00Z"})
	if err != nil || !opts.Summary || opts.Platform != "vulbox" {
		t.Fatalf("summary opts = %+v err=%v", opts, err)
	}
	opts, err = parseLedgerOptions([]string{"-reconcile", "-file", "r.json"})
	if err != nil || !opts.Reconcile {
		t.Fatalf("reconcile opts = %+v err=%v", opts, err)
	}
	if _, err := parseLedgerOptions([]string{"-summary"}); err == nil {
		t.Fatal("summary without platform should fail")
	}
	if _, err := parseLedgerOptions([]string{"-file", "l.json", "-summary"}); err == nil {
		t.Fatal("summary with a file should fail mode selection")
	}
	if _, err := parseLedgerOptions([]string{"-file", "l.json", "-summary", "-platform", "vulbox"}); err == nil {
		t.Fatal("summary+file+platform should be ambiguous")
	}
	if _, err := parseLedgerOptions([]string{"-file", "l.json", "-persist", "-reconcile"}); err == nil {
		t.Fatal("reconcile+persist should fail")
	}
}

func TestReconcileScope(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	in := ledgerFile{Platform: domain.PlatformButian, ProgramID: "butian/1", Since: &now}
	platform, program, since, until := reconcileScope(in, ledgerOptions{})
	if platform != domain.PlatformButian || program != "butian/1" || since == nil || until != nil {
		t.Fatalf("scope = %v %v %v %v", platform, program, since, until)
	}
	platform, program, since, until = reconcileScope(ledgerFile{}, ledgerOptions{
		Platform: "vulbox", Program: "vulbox/1", Since: "2026-11-01T00:00:00Z",
	})
	if platform != domain.PlatformVulbox || program != "vulbox/1" || since == nil || until != nil {
		t.Fatalf("flag scope = %v %v %v %v", platform, program, since, until)
	}
}
