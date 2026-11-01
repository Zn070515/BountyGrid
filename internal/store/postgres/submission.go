package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repo) GetSubmission(ctx context.Context, id domain.SubmissionID) (domain.Submission, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM submissions WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Submission{}, ErrNotFound
		}
		return domain.Submission{}, fmt.Errorf("postgres: get submission: %w", err)
	}
	var s domain.Submission
	if err := json.Unmarshal(data, &s); err != nil {
		return domain.Submission{}, fmt.Errorf("postgres: decode submission: %w", err)
	}
	return s, nil
}
