package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repo) GetSubmissionByReport(ctx context.Context, reportID string) (domain.Submission, bool, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT data FROM submissions
		WHERE report_id=$1 AND status <> 'DRY_RUN'
		ORDER BY submitted_at DESC LIMIT 1`, reportID).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Submission{}, false, nil
		}
		return domain.Submission{}, false, fmt.Errorf("postgres: get submission by report: %w", err)
	}
	var s domain.Submission
	if err := json.Unmarshal(data, &s); err != nil {
		return domain.Submission{}, false, fmt.Errorf("postgres: decode submission: %w", err)
	}
	return s, true, nil
}

// ReserveSubmissionIntent claims the single in-flight submission slot for a
// report. It is called before the platform action; a second concurrent submit
// sees the intent and refuses instead of reaching the Submitter twice.
func (r *Repo) ReserveSubmissionIntent(ctx context.Context, reportID, approvalID string, now time.Time) (bool, error) {
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO submission_intents (report_id, approval_id, started_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (report_id) DO NOTHING`, reportID, approvalID, now)
	if err != nil {
		return false, fmt.Errorf("postgres: reserve submission intent: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repo) ReleaseSubmissionIntent(ctx context.Context, reportID string) error {
	if _, err := r.Pool.Exec(ctx, `DELETE FROM submission_intents WHERE report_id=$1`, reportID); err != nil {
		return fmt.Errorf("postgres: release submission intent: %w", err)
	}
	return nil
}

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
