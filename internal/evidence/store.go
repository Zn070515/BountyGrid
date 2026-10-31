package evidence

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

var ErrNotFound = errors.New("evidence not found")

// Store is the immutable evidence store. Implementations may persist only
// metadata plus an encrypted raw reference.
type Store interface {
	Put(ctx context.Context, e domain.Evidence) error
	Get(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error)
	ListByPlatform(ctx context.Context, p domain.Platform) ([]domain.Evidence, error)
}

// MemoryStore is for tests and local probes.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[domain.EvidenceID]domain.Evidence
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[domain.EvidenceID]domain.Evidence)}
}

// Prepare scans and redacts captured text, computes the content hash, and
// returns an Evidence value plus the redacted text. RawRef is supplied by the
// caller (local/object store reference).
func Prepare(e domain.Evidence, text string) (domain.Evidence, string, []redact.Finding, error) {
	redacted, findings := redact.Redact(text)
	e.ContentHash = redact.Hash([]byte(redacted))
	e.Redactions = e.Redactions[:0]
	for _, f := range findings {
		e.Redactions = append(e.Redactions, f.Kind)
	}
	if err := e.Validate(); err != nil {
		return domain.Evidence{}, "", findings, err
	}
	return e, redacted, findings, nil
}

func (s *MemoryStore) Put(ctx context.Context, e domain.Evidence) error {
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[e.ID]; ok {
		return errors.New("evidence is immutable: id already exists")
	}
	s.items[e.ID] = cloneEvidence(e)
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[id]
	if !ok {
		return domain.Evidence{}, ErrNotFound
	}
	return cloneEvidence(e), nil
}

func (s *MemoryStore) ListByPlatform(ctx context.Context, p domain.Platform) ([]domain.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Evidence
	for _, e := range s.items {
		if e.Platform == p {
			out = append(out, cloneEvidence(e))
		}
	}
	return out, nil
}

func cloneEvidence(e domain.Evidence) domain.Evidence {
	e.Redactions = slices.Clone(e.Redactions)
	return e
}
