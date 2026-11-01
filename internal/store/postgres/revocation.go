package postgres

import (
	"context"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

// BumpRevocationEpoch atomically increments the program's authorization epoch
// and returns the new value. Every execution grant stamped with an older epoch
// is invalid immediately. The first bump inserts epoch 1.
//
// A single INSERT ... ON CONFLICT DO UPDATE keeps the read-modify-write atomic,
// so concurrent bumps cannot return the same epoch.
func (r *Repo) BumpRevocationEpoch(ctx context.Context, program domain.ProgramID) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: bump revocation epoch: empty program id")
	}
	var epoch int64
	err := r.Pool.QueryRow(ctx, `
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
