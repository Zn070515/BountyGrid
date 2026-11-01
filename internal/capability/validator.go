package capability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// Grant validation errors. They are distinct so a caller can report a precise
// denial reason; every one of them is a Deny (there is no approvable state).
var (
	ErrGrantExpired           = errors.New("capability: execution grant has expired")
	ErrLeaseExpired           = errors.New("capability: execution grant lease has expired")
	ErrProgramNotRunning      = errors.New("capability: program is not running")
	ErrProgramStatusStale     = errors.New("capability: program status is stale")
	ErrScopeStale             = errors.New("capability: program scope is stale")
	ErrScopeRevisionMismatch  = errors.New("capability: grant scope revision does not match the program")
	ErrPolicyRevisionMismatch = errors.New("capability: grant policy revision does not match the program")
	ErrGrantRevoked           = errors.New("capability: execution grant authorization epoch is stale")
)

// ProgramStore is the authoritative program source a validator re-reads. The
// PostgreSQL Repo satisfies it.
type ProgramStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
}

// RevocationRegistry holds the per-program authorization epoch. A grant is
// valid only while its AuthorizationEpoch equals the current epoch; Bump
// invalidates every outstanding grant for the program immediately.
type RevocationRegistry interface {
	Epoch(program domain.ProgramID) uint64
	Bump(program domain.ProgramID) uint64
}

// MemoryRevocationRegistry is the process-local registry. Wiring it to the
// Reader/MaterializedState pipeline (or a durable kill switch) is the next
// step; today it exposes the seam and the validator behavior.
type MemoryRevocationRegistry struct {
	mu     sync.Mutex
	epochs map[domain.ProgramID]uint64
}

func NewMemoryRevocationRegistry() *MemoryRevocationRegistry {
	return &MemoryRevocationRegistry{epochs: make(map[domain.ProgramID]uint64)}
}

// Epoch returns the current authorization epoch. An unseen program is at
// epoch 0.
func (r *MemoryRevocationRegistry) Epoch(program domain.ProgramID) uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epochs[program]
}

// Bump invalidates every outstanding grant for the program and returns the new
// epoch.
func (r *MemoryRevocationRegistry) Bump(program domain.ProgramID) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epochs[program]++
	return r.epochs[program]
}

// GrantValidator re-checks an execution grant against authoritative state
// before a request is served. The proxy calls it after the local grant check
// and before any upstream I/O.
type GrantValidator interface {
	ValidateGrant(ctx context.Context, grant ExecutionGrant) error
}

// ProgramValidator validates a grant against the stored Program and the
// revocation registry:
//
//   - the decision expiry and the short grant lease are both in the future;
//   - the program is RUNNING and its status/scope snapshots are fresh;
//   - the grant's scope/policy revisions match the current program;
//   - the current revocation epoch matches the epoch captured at grant build.
//
// Any failure is a hard Deny: a grant cannot be "renewed" by the proxy.
type ProgramValidator struct {
	Programs    ProgramStore
	Revocations RevocationRegistry
	// Now is injectable for tests; nil uses time.Now.
	Now func() time.Time
}

func (v ProgramValidator) ValidateGrant(ctx context.Context, g ExecutionGrant) error {
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if g.ExpiresAt.IsZero() || !g.ExpiresAt.After(now) {
		return ErrGrantExpired
	}
	if g.LeaseUntil.IsZero() || !g.LeaseUntil.After(now) {
		return ErrLeaseExpired
	}
	if v.Programs == nil {
		return errors.New("capability: no program store configured")
	}
	p, err := v.Programs.GetProgram(ctx, g.ProgramID)
	if err != nil {
		return fmt.Errorf("capability: load program %s: %w", g.ProgramID, err)
	}
	if p.Status.Status != domain.StatusRunning {
		return fmt.Errorf("%w: status is %s", ErrProgramNotRunning, p.Status.Status)
	}
	if !p.StatusFreshAt(now) {
		return ErrProgramStatusStale
	}
	if !p.Scope.FreshAt(now) {
		return ErrScopeStale
	}
	if g.ScopeRevision != p.Scope.Revision() {
		return ErrScopeRevisionMismatch
	}
	if g.PolicyRevision != p.PolicyRevision() {
		return ErrPolicyRevisionMismatch
	}
	// A nil registry means "no revocation source configured": the current epoch
	// is then 0, so a grant stamped with a non-zero epoch still fails closed.
	var epoch uint64
	if v.Revocations != nil {
		epoch = v.Revocations.Epoch(g.ProgramID)
	}
	if epoch != g.AuthorizationEpoch {
		return ErrGrantRevoked
	}
	return nil
}
