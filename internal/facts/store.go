package facts

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

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
	// Validate the whole batch before touching the store; a failed Put must
	// never leave half an observation behind.
	seen := make(map[domain.FactID]struct{}, len(fs))
	for _, f := range fs {
		if err := f.Validate(); err != nil {
			return err
		}
		if _, dup := seen[f.ID]; dup {
			return errors.New("facts: duplicate id in batch")
		}
		seen[f.ID] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range seen {
		if _, ok := s.facts[id]; ok {
			return errors.New("facts: fact is immutable: id already exists")
		}
	}
	for _, f := range fs {
		s.facts[f.ID] = cloneFact(f)
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
		out = append(out, cloneFact(f))
	}
	return out, nil
}

func (s *MemoryStore) Supersede(ctx context.Context, id domain.FactID, by domain.FactID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == by {
		return errors.New("facts: self-supersede is not allowed")
	}
	old, ok := s.facts[id]
	if !ok {
		return ErrFactNotFound
	}
	next, ok := s.facts[by]
	if !ok {
		return ErrFactNotFound
	}
	if old.Subject != next.Subject || old.Predicate != next.Predicate {
		return errors.New("facts: supersede must keep subject and predicate")
	}
	// Reject cycles: walking from `by` must never reach `id`.
	for cur := by; cur != ""; {
		if cur == id {
			return errors.New("facts: supersede cycle")
		}
		cur = s.supersededBy[cur]
	}
	s.supersededBy[id] = by
	return nil
}

func cloneFact(f domain.ObservedFact) domain.ObservedFact {
	f.EvidenceRefs = slices.Clone(f.EvidenceRefs)
	f.Value = cloneValue(f.Value)
	return f
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x
	case []byte:
		return bytes.Clone(x)
	case []string:
		return slices.Clone(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = cloneValue(val)
		}
		return out
	default:
		return v
	}
}
