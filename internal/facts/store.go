package facts

import (
	"context"
	"errors"
	"sync"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var ErrFactNotFound = errors.New("fact not found")

type Store interface {
	Put(ctx context.Context, fs []domain.ObservedFact) error
	ListByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error)
	Supersede(ctx context.Context, id domain.FactID, by domain.FactID) error
}

// MemoryStore is append-only except for supersede markers. It is intended for
// tests and probes; PostgreSQL is the product store.
type MemoryStore struct {
	mu           sync.RWMutex
	facts        map[domain.FactID]domain.ObservedFact
	order        []domain.FactID
	supersededBy map[domain.FactID]domain.FactID
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		facts:        make(map[domain.FactID]domain.ObservedFact),
		supersededBy: make(map[domain.FactID]domain.FactID),
	}
}

func (s *MemoryStore) Put(ctx context.Context, fs []domain.ObservedFact) error {
	for _, f := range fs {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range fs {
		if _, ok := s.facts[f.ID]; ok {
			return errors.New("facts: fact is immutable: id already exists")
		}
		s.facts[f.ID] = f
		s.order = append(s.order, f.ID)
	}
	return nil
}

func (s *MemoryStore) ListByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.ObservedFact
	for _, fid := range s.order {
		f := s.facts[fid]
		if f.ProgramID == nil || *f.ProgramID != id {
			continue
		}
		if _, superseded := s.supersededBy[fid]; superseded {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *MemoryStore) Supersede(ctx context.Context, id domain.FactID, by domain.FactID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.facts[id]; !ok {
		return ErrFactNotFound
	}
	if _, ok := s.facts[by]; !ok {
		return ErrFactNotFound
	}
	s.supersededBy[id] = by
	return nil
}
