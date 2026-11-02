package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

func TestExecuteModelCallPersists(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	t.Setenv("DATABASE_URL", dsn)

	ctx := context.Background()
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer srv.Close()

	callID := "call/it/model/" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, callID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE event_type='model.call' AND payload->>'call_id'=$1`, callID)
	})

	out, err := executeModelCall(ctx, modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m", Persist: true,
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 16, Budget: 100, Timeout: 5 * time.Second,
		CallID: callID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.CallID != callID {
		t.Fatalf("call id = %q, want %q", out.CallID, callID)
	}

	var calls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM model_calls WHERE id=$1`, callID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model_calls rows = %d, want 1", calls)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM model_call_attempts WHERE call_id=$1`, callID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("model_call_attempts rows = %d, want 1", attempts)
	}

	var audits int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE event_type='model.call' AND payload->>'call_id'=$1`,
		callID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("model.call audit rows = %d, want 1", audits)
	}
}
