package capability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type fakeProgramStore struct {
	programs map[domain.ProgramID]domain.Program
	err      error
}

func (s *fakeProgramStore) GetProgram(_ context.Context, id domain.ProgramID) (domain.Program, error) {
	if s.err != nil {
		return domain.Program{}, s.err
	}
	p, ok := s.programs[id]
	if !ok {
		return domain.Program{}, domain.ErrNotFound
	}
	return p, nil
}

func validatorFixture(t *testing.T, now time.Time) (*fakeProgramStore, *MemoryRevocationRegistry, ExecutionGrant, domain.Program) {
	t.Helper()
	prog := domain.Program{
		ID:       "p1",
		Platform: domain.PlatformButian,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, ObservedAt: now,
			ValidUntil: now.Add(time.Hour), Confidence: 1,
		},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted,
			ContentsKnown: true, ObservedAt: now, ValidUntil: now.Add(time.Hour),
		},
		PolicyObservedAt: now,
		PolicyValidUntil: now.Add(time.Hour),
	}
	store := &fakeProgramStore{programs: map[domain.ProgramID]domain.Program{"p1": prog}}
	rev := NewMemoryRevocationRegistry()
	epoch, err := rev.Epoch(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	grant := ExecutionGrant{
		DecisionID:         "d1",
		AccountID:          "acc1",
		ProgramID:          "p1",
		ExpiresAt:          now.Add(time.Hour),
		LeaseUntil:         now.Add(time.Minute),
		ScopeRevision:      prog.Scope.Revision(),
		PolicyRevision:     prog.PolicyRevision(),
		AuthorizationEpoch: epoch,
	}
	return store, rev, grant, prog
}

func TestProgramValidatorAcceptsValidGrant(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
}

func TestProgramValidatorDeniesExpiredLease(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	grant.LeaseUntil = now.Add(-time.Second)
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("err = %v, want ErrLeaseExpired", err)
	}

	// A zero lease is unknown, not unlimited.
	grant.LeaseUntil = time.Time{}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("zero lease err = %v, want ErrLeaseExpired", err)
	}
}

func TestProgramValidatorDeniesExpiredGrant(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	grant.ExpiresAt = now
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("err = %v, want ErrGrantExpired", err)
	}
}

func TestProgramValidatorDeniesRevokedEpoch(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	if _, err := rev.Bump(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("err = %v, want ErrGrantRevoked", err)
	}
}

func TestProgramValidatorDeniesPausedProgram(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, prog := validatorFixture(t, now)
	prog.Status.Status = domain.StatusPaused
	store.programs["p1"] = prog
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrProgramNotRunning) {
		t.Fatalf("err = %v, want ErrProgramNotRunning", err)
	}
}

func TestProgramValidatorDeniesStaleStatus(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, prog := validatorFixture(t, now)
	prog.Status.ValidUntil = now.Add(-time.Second)
	store.programs["p1"] = prog
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrProgramStatusStale) {
		t.Fatalf("err = %v, want ErrProgramStatusStale", err)
	}
}

func TestProgramValidatorDeniesStaleScope(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, prog := validatorFixture(t, now)
	prog.Scope.ValidUntil = now.Add(-time.Second)
	store.programs["p1"] = prog
	// The revision is unchanged; freshness alone must reject the grant.
	grant.ScopeRevision = prog.Scope.Revision()
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrScopeStale) {
		t.Fatalf("err = %v, want ErrScopeStale", err)
	}
}

func TestProgramValidatorDeniesRevisionMismatch(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}

	scopeMismatch := grant
	scopeMismatch.ScopeRevision = "different"
	if err := v.ValidateGrant(context.Background(), scopeMismatch); !errors.Is(err, ErrScopeRevisionMismatch) {
		t.Fatalf("scope err = %v, want ErrScopeRevisionMismatch", err)
	}

	policyMismatch := grant
	policyMismatch.PolicyRevision = "different"
	if err := v.ValidateGrant(context.Background(), policyMismatch); !errors.Is(err, ErrPolicyRevisionMismatch) {
		t.Fatalf("policy err = %v, want ErrPolicyRevisionMismatch", err)
	}
}

func TestProgramValidatorDeniesMissingProgram(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, _ := validatorFixture(t, now)
	grant.ProgramID = "missing"
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); err == nil {
		t.Fatal("a missing program must fail closed")
	}
}

func TestProgramValidatorWithoutStoreFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	_, _, grant, _ := validatorFixture(t, now)
	v := ProgramValidator{Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); err == nil {
		t.Fatal("a validator without a program store must fail closed")
	}
}

func TestRevocationRegistryBumpAndEpoch(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRevocationRegistry()
	if got, err := r.Epoch(ctx, "p1"); err != nil || got != 0 {
		t.Fatalf("initial epoch = %d, %v; want 0, nil", got, err)
	}
	if got, err := r.Bump(ctx, "p1"); err != nil || got != 1 {
		t.Fatalf("bump = %d, %v; want 1, nil", got, err)
	}
	if got, err := r.Epoch(ctx, "p1"); err != nil || got != 1 {
		t.Fatalf("epoch after bump = %d, %v; want 1, nil", got, err)
	}
	// Programs are independent.
	if got, err := r.Epoch(ctx, "p2"); err != nil || got != 0 {
		t.Fatalf("other program epoch = %d, %v; want 0, nil", got, err)
	}
	// A cancelled context fails closed rather than reporting epoch 0.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Epoch(canceled, "p1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled epoch err = %v, want context canceled", err)
	}
}

type fakeRevocationStore struct {
	epoch   uint64
	bumps   int
	epochErr error
	bumpErr  error
}

func (s *fakeRevocationStore) Epoch(ctx context.Context, _ domain.ProgramID) (uint64, error) {
	return s.epoch, s.epochErr
}

func (s *fakeRevocationStore) BumpRevocationEpoch(ctx context.Context, _ domain.ProgramID) (uint64, error) {
	s.bumps++
	return s.epoch + uint64(s.bumps), s.bumpErr
}

func TestPostgresRevocationRegistryDelegates(t *testing.T) {
	store := &fakeRevocationStore{epoch: 3}
	reg := PostgresRevocationRegistry{Store: store}
	ctx := context.Background()
	if got, err := reg.Epoch(ctx, "p1"); err != nil || got != 3 {
		t.Fatalf("epoch = %d, %v; want 3", got, err)
	}
	if got, err := reg.Bump(ctx, "p1"); err != nil || got != 4 {
		t.Fatalf("bump = %d, %v; want 4", got, err)
	}
	// A configured but unreadable store must fail closed.
	store.epochErr = errors.New("db down")
	if _, err := reg.Epoch(ctx, "p1"); err == nil {
		t.Fatal("unreadable epoch must fail closed")
	}
	// A missing store must also fail closed.
	if _, err := (PostgresRevocationRegistry{}).Epoch(ctx, "p1"); err == nil {
		t.Fatal("missing store must fail closed")
	}
}

func TestProgramValidatorDeniesStalePolicy(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store, rev, grant, prog := validatorFixture(t, now)
	prog.PolicyObservedAt = now.Add(-domain.PolicyMaxAge - time.Hour)
	prog.PolicyValidUntil = time.Time{}
	store.programs["p1"] = prog
	grant.PolicyRevision = prog.PolicyRevision()
	v := ProgramValidator{Programs: store, Revocations: rev, Now: func() time.Time { return now }}
	if err := v.ValidateGrant(context.Background(), grant); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("err = %v, want ErrPolicyStale", err)
	}
}
