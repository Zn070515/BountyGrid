package state

import (
	"errors"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type Derivation struct {
	StateID    domain.StateID
	Inputs     []domain.FactID
	ValidUntil time.Time
}

// Tracker records which facts produced which derived state, so a superseded
// fact can invalidate every downstream state. It is intentionally small; the
// product store can persist the same mapping.
type Tracker struct {
	mu      sync.RWMutex
	byFact  map[domain.FactID][]domain.StateID
	byState map[domain.StateID]Derivation
}

func NewTracker() *Tracker {
	return &Tracker{
		byFact:  make(map[domain.FactID][]domain.StateID),
		byState: make(map[domain.StateID]Derivation),
	}
}

func (t *Tracker) Record(d Derivation) error {
	if d.StateID == "" {
		return errors.New("state: derivation state_id is empty")
	}
	if len(d.Inputs) == 0 {
		return errors.New("state: derivation inputs are empty")
	}
	d.Inputs = uniqueFactIDs(d.Inputs)
	t.mu.Lock()
	defer t.mu.Unlock()
	// Re-recording a state replaces its dependency mapping; the old inputs must
	// stop pointing at it, otherwise superseding an input that is no longer
	// used would wrongly invalidate the current state.
	if prev, ok := t.byState[d.StateID]; ok {
		for _, f := range prev.Inputs {
			t.byFact[f] = removeState(t.byFact[f], d.StateID)
			if len(t.byFact[f]) == 0 {
				delete(t.byFact, f)
			}
		}
	}
	t.byState[d.StateID] = d
	for _, f := range d.Inputs {
		t.byFact[f] = append(t.byFact[f], d.StateID)
	}
	return nil
}

func uniqueFactIDs(ids []domain.FactID) []domain.FactID {
	out := make([]domain.FactID, 0, len(ids))
	seen := make(map[domain.FactID]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// InvalidateFact removes every state derived from factID and returns the
// invalidated state IDs. Unknown facts return an empty slice.
func (t *Tracker) InvalidateFact(factID domain.FactID) []domain.StateID {
	t.mu.Lock()
	defer t.mu.Unlock()
	states := t.byFact[factID]
	delete(t.byFact, factID)
	seen := make(map[domain.StateID]struct{}, len(states))
	out := states[:0]
	for _, sid := range states {
		if _, dup := seen[sid]; dup {
			continue
		}
		seen[sid] = struct{}{}
		out = append(out, sid)
		if d, ok := t.byState[sid]; ok {
			for _, f := range d.Inputs {
				t.byFact[f] = removeState(t.byFact[f], sid)
				if len(t.byFact[f]) == 0 {
					delete(t.byFact, f)
				}
			}
			delete(t.byState, sid)
		}
	}
	return out
}

func removeState(ids []domain.StateID, target domain.StateID) []domain.StateID {
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

func (t *Tracker) Valid(stateID domain.StateID, now time.Time) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	d, ok := t.byState[stateID]
	if !ok {
		return false
	}
	return d.ValidUntil.IsZero() || d.ValidUntil.After(now)
}
