package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryStoreAppendAndFilter(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	id1, err := s.Append(ctx, Event{
		Type:       "program.observed",
		EntityType: "program",
		EntityID:   "p1",
		ActorKind:  "ADAPTER",
		Payload:    json.RawMessage(`{"status":"RUNNING"}`),
		RecordedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != 1 {
		t.Fatalf("id = %d, want 1", id1)
	}
	if _, err := s.Append(ctx, Event{
		Type:       "program.observed",
		EntityType: "program",
		EntityID:   "p2",
		RecordedAt: at,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(ctx, Filter{EntityID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EntityID != "p1" {
		t.Fatalf("unexpected list: %+v", got)
	}
}

func TestEventRejectsInvalidPayload(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Append(context.Background(), Event{
		Type:       "bad",
		Payload:    json.RawMessage(`{`),
		RecordedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("invalid json payload should be rejected")
	}
}
