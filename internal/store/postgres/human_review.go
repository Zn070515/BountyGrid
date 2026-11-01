package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

// PutHumanReview stores a durable human-verification record. Reviews are
// append-only: same ID + same content is idempotent, different content is a
// conflict.
func (r *Repo) PutHumanReview(ctx context.Context, h domain.HumanReview) error {
	if err := h.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(h)
	if err != nil {
		return err
	}
	evidence, err := mustJSON(h.Evidence)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO human_reviews
			(id, finding_id, reviewer, reviewed_at, verdict, notes_hash, evidence, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		h.ID, h.FindingID, h.Reviewer, h.ReviewedAt, h.Verdict, h.NotesHash, evidence, data)
	if err != nil {
		return fmt.Errorf("postgres: put human review: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existing []byte
		if err := r.Pool.QueryRow(ctx, `SELECT data FROM human_reviews WHERE id=$1`, h.ID).Scan(&existing); err != nil {
			return fmt.Errorf("postgres: read existing human review: %w", err)
		}
		if domain.CanonicalParametersHash(existing) != domain.CanonicalParametersHash([]byte(data)) {
			return fmt.Errorf("%w: human review %s exists with different content", domain.ErrConflict, h.ID)
		}
	}
	return nil
}

func (r *Repo) GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM human_reviews WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.HumanReview{}, ErrNotFound
		}
		return domain.HumanReview{}, fmt.Errorf("postgres: get human review: %w", err)
	}
	var h domain.HumanReview
	if err := json.Unmarshal(data, &h); err != nil {
		return domain.HumanReview{}, fmt.Errorf("postgres: decode human review: %w", err)
	}
	return h, nil
}
