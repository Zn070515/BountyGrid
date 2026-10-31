package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/redact"
)

// Event is an immutable audit record. Payload must not contain secrets.
type Event struct {
	ID         int64
	Type       string
	EntityType string
	EntityID   string
	ActorKind  string
	ActorID    string
	Payload    json.RawMessage
	RecordedAt time.Time
}

func (e Event) Validate() error {
	if e.Type == "" {
		return fmt.Errorf("audit: type is empty")
	}
	if e.RecordedAt.IsZero() {
		return fmt.Errorf("audit: recorded_at is zero")
	}
	if len(e.Payload) > 0 {
		if !json.Valid(e.Payload) {
			return fmt.Errorf("audit: payload is not valid json")
		}
		if findings := redact.Scan(string(e.Payload)); len(findings) > 0 {
			return fmt.Errorf("audit: payload contains sensitive value (%s)", findings[0].Kind)
		}
	}
	return nil
}

type Filter struct {
	Type       string
	EntityType string
	EntityID   string
	Limit      int
}

type Store interface {
	Append(ctx context.Context, e Event) (int64, error)
	List(ctx context.Context, f Filter) ([]Event, error)
}

// MemoryStore is append-only for tests and local probes.
type MemoryStore struct {
	mu     sync.RWMutex
	nextID int64
	events []Event
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) Append(ctx context.Context, e Event) (int64, error) {
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now().UTC()
	}
	if err := e.Validate(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	e.ID = s.nextID
	e.Payload = bytes.Clone(e.Payload)
	s.events = append(s.events, e)
	return e.ID, nil
}

func (s *MemoryStore) List(ctx context.Context, f Filter) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, 0)
	for _, e := range s.events {
		if f.Type != "" && e.Type != f.Type {
			continue
		}
		if f.EntityType != "" && e.EntityType != f.EntityType {
			continue
		}
		if f.EntityID != "" && e.EntityID != f.EntityID {
			continue
		}
		e.Payload = bytes.Clone(e.Payload)
		out = append(out, e)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}
