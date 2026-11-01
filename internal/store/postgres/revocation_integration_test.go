package postgres

import (
	"context"
	"fmt"
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
