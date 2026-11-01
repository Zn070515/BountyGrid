package postgres

import (
	"context"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx, so the same bump
// statement can run standalone or inside the program-upsert transaction.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// BumpRevocationEpoch atomically increments the program's authorization epoch
// and returns the new value. Every execution grant stamped with an older epoch
// is invalid immediately. The first bump inserts epoch 1.
//
// A single INSERT ... ON CONFLICT DO UPDATE keeps the read-modify-write atomic,
// so concurrent bumps cannot return the same epoch.
func (r *Repo) BumpRevocationEpoch(ctx context.Context, program domain.ProgramID) (uint64, error) {
	return bumpRevocationEpoch(ctx, r.Pool, program)
}

func bumpRevocationEpoch(ctx context.Context, q queryRower, program domain.ProgramID) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: bump revocation epoch: empty program id")
	}
	var epoch int64
	err := q.QueryRow(ctx, `
		INSERT INTO revocation_epochs (program_id, epoch, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (program_id) DO UPDATE SET
			epoch = revocation_epochs.epoch + 1,
			updated_at = now()
		RETURNING epoch`,
		string(program),
	).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("postgres: bump revocation epoch: %w", err)
	}
	if epoch < 0 {
		return 0, fmt.Errorf("postgres: bump revocation epoch: negative epoch %d", epoch)
	}
	return uint64(epoch), nil
}

// programSupersedesAuthority reports whether replacing old with next must
// invalidate outstanding execution grants. It fires when the platform identity
// changes, the program leaves RUNNING (including to UNKNOWN), or the Scope /
// automation+constraints snapshot changes. Pure observation refreshes (new
// observed_at, same content) do not revoke.
func programSupersedesAuthority(old, next domain.Program) bool {
	if old.ID != next.ID || old.Platform != next.Platform {
		return true
	}
	if old.Status.Status != next.Status.Status && next.Status.Status != domain.StatusRunning {
		return true
	}
	if old.Scope.Revision() != next.Scope.Revision() {
		return true
	}
	return old.PolicyRevision() != next.PolicyRevision()
}

// Epoch returns the program's current authorization epoch. A program that has
// never been revoked is at epoch 0.
func (r *Repo) Epoch(ctx context.Context, program domain.ProgramID) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: revocation epoch: empty program id")
	}
	var epoch int64
	err := r.Pool.QueryRow(ctx,
		`SELECT epoch FROM revocation_epochs WHERE program_id = $1`, string(program),
	).Scan(&epoch)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("postgres: revocation epoch: %w", err)
	}
	if epoch < 0 {
		return 0, fmt.Errorf("postgres: revocation epoch: negative epoch %d", epoch)
	}
	return uint64(epoch), nil
}
