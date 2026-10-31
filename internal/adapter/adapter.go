// Package adapter defines the read-only platform adapter contract. A Reader
// returns domain snapshots plus the Facts and Evidence that support them; it
// never writes to the database or mutates platform state.
package adapter

import (
	"context"
	"errors"

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
	Facts       []domain.ObservedFact
	Evidence    []domain.Evidence
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
