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

	rec := model.CallRecord{
		ID: id, TaskID: domain.TaskID("task/it"), Purpose: model.PurposeCode,
		DataClass: model.DataPublic, Status: model.CallReserved,
		PromptHash: "prompt-hash", StartedAt: now,
	}
	ok, err := store.Reserve(ctx, id, rec)
	if err != nil || !ok {
		t.Fatalf("reserve = %v %v, want true", ok, err)
	}
	if ok, err := store.Reserve(ctx, id, rec); err != nil || ok {
		t.Fatalf("duplicate reserve = %v %v, want false", ok, err)
	}
	if got, found, err := store.Get(ctx, id); err != nil || !found || got.Status != model.CallReserved {
		t.Fatalf("reserved call = %+v found=%v err=%v", got, found, err)
	}

	started := rec
	started.Status = model.CallStarted
	started.Provider = "local"
	started.Model = "local-model"
	if err := store.Complete(ctx, id, started); err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.Get(ctx, id); err != nil || got.Status != model.CallStarted {
		t.Fatalf("started call = %+v err=%v", got, err)
	}
	if err := store.Reclaim(ctx, id); err == nil {
		t.Fatal("a STARTED call must not be reclaimable")
	}

	failed := started
	failed.Status = model.CallFailedBeforeSend
	failed.Error = "PROVIDER_ERROR"
	failed.EndedAt = now
	if err := store.Complete(ctx, id, failed); err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.Get(ctx, id); err != nil || got.Status != model.CallFailedBeforeSend {
		t.Fatalf("failed call = %+v err=%v", got, err)
	}
	if err := store.Reclaim(ctx, id); err != nil {
		t.Fatalf("reclaim FAILED_BEFORE_SEND: %v", err)
	}
	if _, found, err := store.Get(ctx, id); err != nil || found {
		t.Fatalf("reclaimed call: found=%v err=%v", found, err)
	}

	completed := rec
	completed.Status = model.CallCompleted
	completed.Provider = "local"
	completed.Model = "local-model"
	completed.ResponseHash = "response-hash"
	completed.EndedAt = now
	if ok, err := store.Reserve(ctx, id, rec); err != nil || !ok {
		t.Fatalf("re-reserve after reclaim = %v %v", ok, err)
	}
	if err := store.Complete(ctx, id, completed); err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.Get(ctx, id); err != nil || got.Status != model.CallCompleted {
		t.Fatalf("completed call = %+v err=%v", got, err)
	}
	if ok, err := store.Reserve(ctx, id, rec); err != nil || ok {
		t.Fatalf("duplicate completed reserve = %v %v, want false", ok, err)
	}
}
