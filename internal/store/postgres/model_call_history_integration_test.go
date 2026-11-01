package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

// TestReclaimArchivesAttempts proves that reclaiming a FAILED_BEFORE_SEND call
// removes the reclaimable logical row but keeps the provider-attempt ledger in
// the history table (the append-only audit trail survives).
func TestReclaimArchivesAttempts(t *testing.T) {
	r, ctx := integrationRepo(t)
	store := &ModelCallStore{Repo: r}
	now := time.Now().UTC().Truncate(time.Second)
	id := "it-reclaim-history-" + now.Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, id)
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_call_attempt_history WHERE call_id=$1`, id)
	})

	call := model.ModelCall{
		ID: id, TaskID: "task/it-history", Purpose: model.PurposeCode,
		DataClass: model.DataPublic, FinalStatus: model.CallReserved, StartedAt: now,
	}
	if ok, err := store.Reserve(ctx, id, call); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	attempt := model.ModelCallAttempt{
		AttemptID: "attempt-" + id, CallID: id,
		Provider: "local-a", Model: "a", Status: model.CallFailedBeforeSend,
		EstimatedCost: domain.Money{Amount: 3, Currency: "CNY"},
		PromptHash:    "prompt", StartedAt: now, EndedAt: now, Error: "PROVIDER_ERROR",
	}
	if err := store.RecordAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	call.FinalStatus = model.CallFailedBeforeSend
	call.EndedAt = now
	if err := store.Complete(ctx, id, call); err != nil {
		t.Fatal(err)
	}
	if err := store.Reclaim(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, found, err := store.Get(ctx, id); err != nil || found {
		t.Fatalf("reclaimed logical call found=%v err=%v", found, err)
	}
	current, err := store.ListAttempts(ctx, id)
	if err != nil || len(current) != 0 {
		t.Fatalf("current attempts = %+v err=%v, want none", current, err)
	}
	history, err := r.ListModelCallAttemptHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].AttemptID != attempt.AttemptID ||
		history[0].Status != model.CallFailedBeforeSend {
		t.Fatalf("history = %+v", history)
	}
}
