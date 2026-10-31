package evidence

import (
	"context"
	"errors"
	"sync"

	"github.com/Zn070515/BountyGrid/internal/domain"
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

func (s *MemoryStore) Put(ctx context.Context, e domain.Evidence) error {
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[e.ID]; ok {
		return errors.New("evidence is immutable: id already exists")
	}
	s.items[e.ID] = e
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[id]
	if !ok {
		return domain.Evidence{}, ErrNotFound
	}
	return e, nil
}

func (s *MemoryStore) ListByPlatform(ctx context.Context, p domain.Platform) ([]domain.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Evidence
	for _, e := range s.items {
		if e.Platform == p {
			out = append(out, e)
		}
	}
	return out, nil
}
