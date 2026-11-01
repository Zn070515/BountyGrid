package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

func TestModelCallLifecycle(t *testing.T) {
	r, ctx := integrationRepo(t)
	store := &ModelCallStore{Repo: r}
	now := time.Now().UTC().Truncate(time.Second)
	suffix := now.Format("150405.000000000")
	id := "it-call-" + suffix
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, id)
	})

	rec := model.ModelCall{
		ID: id, TaskID: domain.TaskID("task/it"), StepID: "step-1", Purpose: model.PurposeCode,
		DataClass: model.DataPublic, FinalStatus: model.CallReserved, StartedAt: now,
	}
	ok, err := store.Reserve(ctx, id, rec)
	if err != nil || !ok {
		t.Fatalf("reserve = %v %v, want true", ok, err)
	}
	if ok, err := store.Reserve(ctx, id, rec); err != nil || ok {
		t.Fatalf("duplicate reserve = %v %v, want false", ok, err)
	}
	if got, found, err := store.Get(ctx, id); err != nil || !found || got.FinalStatus != model.CallReserved || got.StepID != "step-1" {
		t.Fatalf("reserved call = %+v found=%v err=%v", got, found, err)
	}

	started := rec
	started.FinalStatus = model.CallStarted
	if err := store.Complete(ctx, id, started); err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.Get(ctx, id); err != nil || got.FinalStatus != model.CallStarted {
		t.Fatalf("started call = %+v err=%v", got, err)
	}
	if err := store.Reclaim(ctx, id); err == nil {
		t.Fatal("a STARTED call must not be reclaimable")
	}

	failed := started
	failed.FinalStatus = model.CallFailedBeforeSend
	failed.Error = "PROVIDER_ERROR"
	failed.EndedAt = now
	if err := store.Complete(ctx, id, failed); err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.Get(ctx, id); err != nil || got.FinalStatus != model.CallFailedBeforeSend {
		t.Fatalf("failed call = %+v err=%v", got, err)
	}
	if err := store.Reclaim(ctx, id); err != nil {
		t.Fatalf("reclaim FAILED_BEFORE_SEND: %v", err)
	}
	if _, found, err := store.Get(ctx, id); err != nil || found {
		t.Fatalf("reclaimed call: found=%v err=%v", found, err)
	}

	completed := rec
	completed.FinalStatus = model.CallCompleted
	completed.EndedAt = now
	completed.TotalCost = domain.Money{Amount: 42, Currency: "CNY"}
	completed.HasAmbiguousAttempt = true
	if ok, err := store.Reserve(ctx, id, rec); err != nil || !ok {
		t.Fatalf("re-reserve after reclaim = %v %v", ok, err)
	}
	if err := store.Complete(ctx, id, completed); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get(ctx, id)
	if err != nil || got.FinalStatus != model.CallCompleted || got.TotalCost.Amount != 42 || !got.HasAmbiguousAttempt {
		t.Fatalf("completed call = %+v err=%v", got, err)
	}
	if ok, err := store.Reserve(ctx, id, rec); err != nil || ok {
		t.Fatalf("duplicate completed reserve = %v %v, want false", ok, err)
	}
}

func TestModelCallAttemptsLedger(t *testing.T) {
	r, ctx := integrationRepo(t)
	store := &ModelCallStore{Repo: r}
	now := time.Now().UTC().Truncate(time.Second)
	suffix := now.Format("150405.000000000")
	id := "it-call-attempts-" + suffix
	t.Cleanup(func() {
		// model_call_attempts cascades from model_calls.
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, id)
	})

	call := model.ModelCall{
		ID: id, TaskID: "task/it-attempts", StepID: "step-2",
		Purpose: model.PurposeAnalyze, DataClass: model.DataInternal,
		FinalStatus: model.CallReserved, StartedAt: now,
	}
	if ok, err := store.Reserve(ctx, id, call); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}

	ambiguous := model.ModelCallAttempt{
		AttemptID: "attempt-" + suffix + "-a", CallID: id,
		Provider: "local-a", Model: "a", Status: model.CallAmbiguous,
		EstimatedCost: domain.Money{Amount: 7, Currency: "CNY"},
		PromptHash:    "prompt", StartedAt: now, EndedAt: now, Error: "PROVIDER_ERROR",
	}
	completed := model.ModelCallAttempt{
		AttemptID: "attempt-" + suffix + "-b", CallID: id,
		Provider: "local-b", Model: "b", Status: model.CallCompleted,
		EstimatedCost: domain.Money{Amount: 8, Currency: "CNY"},
		ActualCost:    domain.Money{Amount: 6, Currency: "CNY"},
		PromptHash:    "prompt", ResponseHash: "response", StartedAt: now, EndedAt: now,
	}
	for _, attempt := range []model.ModelCallAttempt{ambiguous, completed} {
		if err := store.RecordAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	// Repeating an attempt write is idempotent.
	if err := store.RecordAttempt(ctx, ambiguous); err != nil {
		t.Fatal(err)
	}

	attempts, err := store.ListAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Status != model.CallAmbiguous || attempts[1].Status != model.CallCompleted {
		t.Fatalf("attempt order = %+v", attempts)
	}

	call.FinalStatus = model.CallCompleted
	call.TotalCost = domain.Money{Amount: 13, Currency: "CNY"}
	call.HasAmbiguousAttempt = true
	call.EndedAt = now
	if err := store.Complete(ctx, id, call); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalStatus != model.CallCompleted || got.TotalCost.Amount != 13 || !got.HasAmbiguousAttempt {
		t.Fatalf("logical call = %+v", got)
	}
}

func TestModelCallCompleteDefaultsToCompleted(t *testing.T) {
	r, ctx := integrationRepo(t)
	store := &ModelCallStore{Repo: r}
	id := "it-call-empty-" + time.Now().UTC().Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, id)
	})
	if ok, err := store.Reserve(ctx, id, model.ModelCall{
		ID: id, TaskID: "task/it", Purpose: model.PurposeCode, DataClass: model.DataPublic,
		FinalStatus: model.CallReserved,
	}); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	// Complete is the successful transition; an omitted status must not leave
	// the row in RESERVED (which would block any retry forever).
	if err := store.Complete(ctx, id, model.ModelCall{}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(ctx, id)
	if err != nil || !found || got.FinalStatus != model.CallCompleted {
		t.Fatalf("completion without status = %+v found=%v err=%v", got, found, err)
	}
}
