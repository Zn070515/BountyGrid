package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// TestUpsertProgramRevokesOnAuthorityChange proves the durable revocation
// trigger: leaving RUNNING, a Scope content change or a policy-revision change
// bumps the program epoch inside the same transaction as the program upsert.
// Observation refreshes with identical authority do not revoke.
func TestUpsertProgramRevokesOnAuthorityChange(t *testing.T) {
	r, ctx := integrationRepo(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := domain.ProgramID(fmt.Sprintf("it-revoke-%d", now.UnixNano()))
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM programs WHERE id=$1`, id)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM revocation_epochs WHERE program_id=$1`, id)
	})

	p := revocationProgram(id)
	p.ObservedAt, p.Status.ObservedAt = now, now
	p.Status.ValidUntil = now.Add(time.Hour)
	if err := r.UpsertProgram(ctx, p); err != nil {
		t.Fatal(err)
	}
	epoch := func() uint64 {
		t.Helper()
		got, err := r.Epoch(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := epoch(); got != 0 {
		t.Fatalf("new program epoch = %d, want 0", got)
	}

	// An identical authority snapshot (only observations move) must not revoke.
	same := p
	same.ObservedAt = now.Add(time.Minute)
	same.Status.ObservedAt = now.Add(time.Minute)
	same.Status.ValidUntil = now.Add(2 * time.Hour)
	if err := r.UpsertProgram(ctx, same); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 0 {
		t.Fatalf("observation refresh bumped epoch to %d", got)
	}

	paused := same
	paused.Status.Status = domain.StatusPaused
	paused.Status.Reason = "platform pause"
	if err := r.UpsertProgram(ctx, paused); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 1 {
		t.Fatalf("pause epoch = %d, want 1", got)
	}

	// Re-syncing the same PAUSED state is not another revocation.
	pausedAgain := paused
	pausedAgain.Status.ObservedAt = now.Add(2 * time.Minute)
	pausedAgain.Status.ValidUntil = now.Add(3 * time.Hour)
	if err := r.UpsertProgram(ctx, pausedAgain); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 1 {
		t.Fatalf("repeated pause bumped epoch to %d", got)
	}

	running := pausedAgain
	running.Status.Status = domain.StatusRunning
	if err := r.UpsertProgram(ctx, running); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 1 {
		t.Fatalf("resuming bumped epoch to %d", got)
	}

	changedPolicy := running
	changedPolicy.Automation.ActiveHTTP = domain.CapConditional
	if err := r.UpsertProgram(ctx, changedPolicy); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 2 {
		t.Fatalf("policy revision epoch = %d, want 2", got)
	}

	changedScope := changedPolicy
	changedScope.Scope.Include = []domain.Asset{{ID: "asset-it", Value: "example.test"}}
	if err := r.UpsertProgram(ctx, changedScope); err != nil {
		t.Fatal(err)
	}
	if got := epoch(); got != 3 {
		t.Fatalf("scope revision epoch = %d, want 3", got)
	}
}

// TestFirstUpsertIsSerialized covers the race the row lock cannot: two
// first-time upserts both seeing ErrNoRows. The uncommitted RUNNING insert is
// invisible to the competing SELECT, so only the transaction-scoped advisory
// lock forces it to wait; after the commit it must observe RUNNING and bump
// the epoch for the PAUSED authority change.
func TestFirstUpsertIsSerialized(t *testing.T) {
	r, ctx := integrationRepo(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := domain.ProgramID(fmt.Sprintf("it-first-upsert-%d", now.UnixNano()))
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM programs WHERE id=$1`, id)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM revocation_epochs WHERE program_id=$1`, id)
	})

	running := revocationProgram(id)
	running.ObservedAt, running.Status.ObservedAt = now, now
	running.Status.ValidUntil = now.Add(time.Hour)
	paused := running
	paused.Status.Status = domain.StatusPaused
	paused.Status.Reason = "paused during first insert"

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	data, err := json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO programs
			(id, platform, native_id, name, type, status, status_reason,
			 status_observed_at, status_valid_until, next_check_at, data, observed_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,now())`,
		running.ID, running.Platform, running.NativeID, running.Name, running.Type,
		running.Status.Status, running.Status.Reason, running.Status.ObservedAt,
		running.Status.ValidUntil, running.NextCheckAt, string(data), running.ObservedAt,
	); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- r.UpsertProgram(context.Background(), paused) }()
	select {
	case err := <-done:
		t.Fatalf("competing upsert finished before the first insert committed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	epoch, err := r.Epoch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 1 {
		t.Fatalf("epoch = %d, want 1: the PAUSED update skipped revocation", epoch)
	}
	got, err := r.GetProgram(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Status != domain.StatusPaused {
		t.Fatalf("status = %s, want PAUSED", got.Status.Status)
	}
}

// TestRevokeProgramWithAuditIsAtomic proves the bump and the audit event share
// one transaction: a rejected audit payload (an operator reason containing a
// secret) rolls the epoch bump back instead of leaving an unaudited revocation.
func TestRevokeProgramWithAuditIsAtomic(t *testing.T) {
	r, ctx := integrationRepo(t)
	id := domain.ProgramID(fmt.Sprintf("it-revoke-atomic-%d", time.Now().UTC().UnixNano()))
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM revocation_epochs WHERE program_id=$1`, id)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM audit_events WHERE entity_id=$1`, id)
	})
	auditCount := func() int {
		t.Helper()
		var n int
		if err := r.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE event_type='grant.revoked' AND entity_id=$1`, id,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	epoch, err := r.RevokeProgramWithAudit(ctx, id, "manual smoke", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || auditCount() != 1 {
		t.Fatalf("epoch=%d audits=%d, want 1/1", epoch, auditCount())
	}

	// A secret-like reason is redacted, not rejected: the kill switch must not
	// be blocked by the audit guardrail, and the bump + audit stay atomic.
	epoch, err = r.RevokeProgramWithAudit(ctx, id, "password=secret123", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 2 || auditCount() != 2 {
		t.Fatalf("epoch=%d audits=%d, want 2/2", epoch, auditCount())
	}
	var payloadText string
	if err := r.Pool.QueryRow(ctx, `
		SELECT payload::text FROM audit_events
		WHERE event_type='grant.revoked' AND entity_id=$1
		ORDER BY id DESC LIMIT 1`, id).Scan(&payloadText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payloadText, "secret123") {
		t.Fatalf("revocation audit leaked the secret: %s", payloadText)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reason_redacted"] != true {
		t.Fatalf("revocation audit did not mark the reason redacted: %s", payloadText)
	}
	if _, ok := payload["reason"]; ok {
		t.Fatalf("redacted reason was still stored verbatim: %s", payloadText)
	}
}
