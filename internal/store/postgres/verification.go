package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repo) PutVerification(ctx context.Context, v domain.Verification) error {
	if err := v.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(v)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO verifications
			(id, finding_id, verdict, reproduced, in_scope, impact_verified,
			 data_minimized, duplicate_risk, verified_by, verified_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		v.ID, v.FindingID, v.Verdict, v.Reproduced, v.InScope, v.ImpactVerified,
		v.DataMinimized, v.DuplicateRisk, v.VerifiedBy, v.VerifiedAt, data,
	)
	if err != nil {
		return fmt.Errorf("postgres: put verification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Verification is an append-only audit record. Same ID + same content is
		// idempotent; different content is a conflict. Re-verification must use a
		// new VerificationID rather than rewriting history.
		var existing []byte
		if err := r.Pool.QueryRow(ctx, `SELECT data FROM verifications WHERE id=$1`, v.ID).Scan(&existing); err != nil {
			return fmt.Errorf("postgres: read existing verification: %w", err)
		}
		if domain.CanonicalParametersHash(existing) != domain.CanonicalParametersHash([]byte(data)) {
			return fmt.Errorf("%w: verification %s exists with different content", domain.ErrConflict, v.ID)
		}
	}
	return nil
}

func (r *Repo) GetVerification(ctx context.Context, id string) (domain.Verification, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM verifications WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Verification{}, ErrNotFound
		}
		return domain.Verification{}, fmt.Errorf("postgres: get verification: %w", err)
	}
	var v domain.Verification
	if err := json.Unmarshal(data, &v); err != nil {
		return domain.Verification{}, fmt.Errorf("postgres: decode verification: %w", err)
	}
	return v, nil
}
