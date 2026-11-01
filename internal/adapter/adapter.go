// Package adapter defines the read-only platform adapter contract. A Reader
// returns domain snapshots plus the Facts and Evidence that support them; it
// never writes to the database or mutates platform state.
package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var (
	// ErrAuthRequired means the saved browser session is missing or expired.
	ErrAuthRequired = errors.New("adapter: authentication required")
	// ErrGated means the platform requires a human action (signup, NDA,
	// application review) before the requested contents are visible.
	ErrGated = errors.New("adapter: gated by a human action")
	// ErrUnsupported is returned when a platform page or program type cannot be
	// interpreted safely. The caller must keep the Observation raw and retry
	// after a human fixes the extractor; it must not guess.
	ErrUnsupported = errors.New("adapter: unsupported page or program type")
)

type AuthState string

const (
	AuthOK       AuthState = "OK"
	AuthRequired AuthState = "REQUIRED"
	AuthUnknown  AuthState = "UNKNOWN"
)

type SyncOptions struct {
	PageSize     int
	MaxPages     int
	IncludeEnded bool
}

type AccountSnapshot struct {
	Account  domain.Account
	Facts    []domain.ObservedFact
	Evidence []domain.Evidence
}

type ProgramBatch struct {
	Programs       []domain.Program
	Facts          []domain.ObservedFact
	Evidence       []domain.Evidence
	ObservedCount  int
	EstimatedTotal int
	// JoinedProgramIDs are programs the logged-in account has explicitly
	// joined/registered according to the list page. They feed the account's
	// JoinedProgramIDs so policy can resolve PROGRAM_SIGNUP gates at runtime.
	JoinedProgramIDs []domain.ProgramID
	// Estimated is true when the platform count is derived from pagination or
	// category sums rather than an exact per-program listing.
	Estimated bool
	// Unsupported lists native program IDs the extractor refused to guess at.
	Unsupported []string
}

type ProgramSnapshot struct {
	Program  domain.Program
	Scope    domain.Scope
	Facts    []domain.ObservedFact
	Evidence []domain.Evidence
}

type ScopeSnapshot struct {
	Scope    domain.Scope
	Facts    []domain.ObservedFact
	Evidence []domain.Evidence
}

type PolicySnapshot struct {
	Automation  domain.AutomationPolicy
	Constraints domain.Constraints
	// ObservedAt timestamps this policy read. It is stored separately from the
	// program row so a later list sync cannot make an old policy look fresh.
	ObservedAt time.Time
	Facts      []domain.ObservedFact
	Evidence   []domain.Evidence
}

// Reader is the canonical Phase 1 subset of the platform Adapter: it only
// reads. Report preparation and submission are Phase 6 work.
type Reader interface {
	Platform() domain.Platform
	AuthStatus(ctx context.Context) (AuthState, error)
	SyncAccount(ctx context.Context) (AccountSnapshot, error)
	SyncPrograms(ctx context.Context, opts SyncOptions) (ProgramBatch, error)
	SyncProgram(ctx context.Context, id domain.ProgramID) (ProgramSnapshot, error)
	SyncScope(ctx context.Context, id domain.ProgramID) (ScopeSnapshot, error)
	SyncPolicy(ctx context.Context, id domain.ProgramID) (PolicySnapshot, error)
}

// ProgramSignupSatisfied reports whether the captured gates explicitly prove
// that the account has joined/registered for the program. UNKNOWN and
// UNSATISFIED both return false; callers must not infer enrollment from a
// visible button alone.
func ProgramSignupSatisfied(p domain.Program) bool {
	for _, gates := range [][]domain.EligibilityGate{p.Gates, p.Eligibility.Gates} {
		for _, g := range gates {
			if g.Kind == domain.GateProgramSignup && g.Satisfied == domain.GateSatisfied {
				return true
			}
		}
	}
	return false
}

// Optional reader capabilities. A platform exposes only the extra sources it
// can observe; callers must type-assert and treat absence as "not available",
// never as an empty list.
type PublicWelfareReader interface {
	SyncPublicWelfare(ctx context.Context, opts SyncOptions) (ProgramBatch, error)
}

// GrowReader reads a campus program (Butian GROW) whose rules live in a modal
// plus the activity center rather than in the program list.
type GrowReader interface {
	SyncGrow(ctx context.Context) (ProgramSnapshot, error)
}
