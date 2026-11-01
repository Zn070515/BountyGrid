package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

// TestModelCallCompleteIsCompareAndSwap verifies the durable state machine:
// STARTED may be rewritten for the next provider attempt of the same fallback
// chain, but a terminal row can never be overwritten by a stale writer.
func TestModelCallCompleteIsCompareAndSwap(t *testing.T) {
	r, ctx := integrationRepo(t)
	store := &ModelCallStore{Repo: r}
	now := time.Now().UTC().Truncate(time.Second)
	id := fmt.Sprintf("it-call-cas-%d", now.UnixNano())
	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, id)
	})

	rec := model.ModelCall{
		ID: id, TaskID: domain.TaskID("task/it"), Purpose: model.PurposeCode,
		DataClass: model.DataPublic, FinalStatus: model.CallReserved, StartedAt: now,
	}
	if ok, err := store.Reserve(ctx, id, rec); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	started := rec
	started.FinalStatus = model.CallStarted
	if err := store.Complete(ctx, id, started); err != nil {
		t.Fatal(err)
	}
	// STARTED -> STARTED is the second provider attempt in the same chain.
	if err := store.Complete(ctx, id, started); err != nil {
		t.Fatalf("STARTED -> STARTED: %v", err)
	}
	completed := rec
	completed.FinalStatus = model.CallCompleted
	completed.EndedAt = now
	if err := store.Complete(ctx, id, completed); err != nil {
		t.Fatal(err)
	}
	for _, status := range []model.CallStatus{
		model.CallAmbiguous, model.CallFailedBeforeSend, model.CallCompleted,
	} {
		stale := rec
		stale.FinalStatus = status
		if err := store.Complete(ctx, id, stale); !errors.Is(err, model.ErrInvalidCallTransition) {
			t.Fatalf("terminal -> %s err = %v, want ErrInvalidCallTransition", status, err)
		}
	}
	got, found, err := store.Get(ctx, id)
	if err != nil || !found || got.FinalStatus != model.CallCompleted {
		t.Fatalf("row = %+v found=%v err=%v, want COMPLETED", got, found, err)
	}
}
