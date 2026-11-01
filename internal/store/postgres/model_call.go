package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/model"
	"github.com/jackc/pgx/v5"
)

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ReserveModelCall implements model.CallStore. The primary key reservation is
// the idempotency boundary: a retry that sees an existing row must not invoke a
// provider and pay twice.
func (r *Repo) ReserveModelCall(ctx context.Context, id string, rec model.ModelCall) (bool, error) {
	status := rec.FinalStatus
	if status == "" {
		status = model.CallReserved
	}
	rec.FinalStatus = status
	data, err := mustJSON(rec)
	if err != nil {
		return false, err
	}
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO model_calls
			(id, task_id, step_id, purpose, data_class, error, started_at,
			 ended_at, status, total_cost_cents, currency,
			 has_ambiguous_attempt, updated_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now(),$13::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		id, nullString(string(rec.TaskID)), rec.StepID, rec.Purpose, rec.DataClass,
		rec.Error, nullTime(rec.StartedAt), nullTime(rec.EndedAt), status,
		rec.TotalCost.Amount, nullString(rec.TotalCost.Currency),
		rec.HasAmbiguousAttempt, data)
	if err != nil {
		return false, fmt.Errorf("postgres: reserve model call: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repo) CompleteModelCall(ctx context.Context, id string, rec model.ModelCall) error {
	status := rec.FinalStatus
	if status == "" {
		// "Complete" is the successful transition. Defaulting to RESERVED here
		// (as ReserveModelCall does) would record a finished call as never
		// started, leaving it permanently non-reclaimable and retry-blocked.
		status = model.CallCompleted
	}
	rec.FinalStatus = status
	data, err := mustJSON(rec)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		UPDATE model_calls SET
			step_id=$2, purpose=$3, data_class=$4, error=$5,
			started_at=$6, ended_at=$7, status=$8,
			total_cost_cents=$9, cost_cents=$9, currency=$10,
			has_ambiguous_attempt=$11, updated_at=now(), data=$12::jsonb
		WHERE id=$1`,
		id, rec.StepID, rec.Purpose, rec.DataClass, rec.Error,
		nullTime(rec.StartedAt), nullTime(rec.EndedAt), status,
		rec.TotalCost.Amount, nullString(rec.TotalCost.Currency),
		rec.HasAmbiguousAttempt, data)
	if err != nil {
		return fmt.Errorf("postgres: complete model call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: model call %s not found", id)
	}
	return nil
}

// ReclaimModelCall deletes a FAILED_BEFORE_SEND reservation so the same call ID
// can be reserved again. It deliberately refuses every other status: a call that
// was STARTED, COMPLETED or left AMBIGUOUS may already have reached and been
// charged by the provider.
func (r *Repo) ReclaimModelCall(ctx context.Context, id string) error {
	tag, err := r.Pool.Exec(ctx, `
		DELETE FROM model_calls WHERE id=$1 AND status=$2`,
		id, model.CallFailedBeforeSend)
	if err != nil {
		return fmt.Errorf("postgres: reclaim model call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: model call %s is not reclaimable", id)
	}
	return nil
}

func (r *Repo) GetModelCall(ctx context.Context, id string) (model.ModelCall, bool, error) {
	var data []byte
	var status model.CallStatus
	var stepID string
	var total int64
	var currency string
	var ambiguous bool
	err := r.Pool.QueryRow(ctx, `
		SELECT data, status, COALESCE(step_id,''), COALESCE(total_cost_cents,0),
		       COALESCE(currency,''), COALESCE(has_ambiguous_attempt,false)
		FROM model_calls WHERE id=$1`, id,
	).Scan(&data, &status, &stepID, &total, &currency, &ambiguous)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ModelCall{}, false, nil
		}
		return model.ModelCall{}, false, fmt.Errorf("postgres: get model call: %w", err)
	}
	var rec model.ModelCall
	if err := json.Unmarshal(data, &rec); err != nil {
		return model.ModelCall{}, false, fmt.Errorf("postgres: decode model call: %w", err)
	}
	// The columns are the durable state machine. Data written before a column
	// existed has no matching field, so fall back to the row value.
	if rec.FinalStatus == "" {
		rec.FinalStatus = status
	}
	if rec.StepID == "" {
		rec.StepID = stepID
	}
	if rec.TotalCost.Amount == 0 && total != 0 {
		rec.TotalCost.Amount = total
	}
	if rec.TotalCost.Currency == "" {
		rec.TotalCost.Currency = currency
	}
	if !rec.HasAmbiguousAttempt {
		rec.HasAmbiguousAttempt = ambiguous
	}
	return rec, true, nil
}

// RecordModelCallAttempt appends one provider invocation. Attempt rows are
// immutable: a duplicate attempt ID is ignored so a retried write is
// idempotent, while a different call's row is never overwritten.
func (r *Repo) RecordModelCallAttempt(ctx context.Context, attempt model.ModelCallAttempt) error {
	data, err := mustJSON(attempt)
	if err != nil {
		return err
	}
	currency := attempt.ActualCost.Currency
	if currency == "" {
		currency = attempt.EstimatedCost.Currency
	}
	if _, err := r.Pool.Exec(ctx, `
		INSERT INTO model_call_attempts
			(attempt_id, call_id, provider, model, status,
			 estimated_cost_cents, actual_cost_cents, currency,
			 input_tokens, output_tokens, prompt_hash, response_hash,
			 error, started_at, ended_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)
		ON CONFLICT (attempt_id) DO NOTHING`,
		attempt.AttemptID, attempt.CallID, attempt.Provider, attempt.Model, attempt.Status,
		attempt.EstimatedCost.Amount, attempt.ActualCost.Amount,
		nullString(currency), attempt.Usage.InputTokens,
		attempt.Usage.OutputTokens, attempt.PromptHash, attempt.ResponseHash,
		attempt.Error, nullTime(attempt.StartedAt), nullTime(attempt.EndedAt), data,
	); err != nil {
		return fmt.Errorf("postgres: record model call attempt: %w", err)
	}
	return nil
}

// ListModelCallAttempts returns the attempts for one logical call in execution
// order. It is used for reconciliation and integration tests; the Router itself
// only appends.
func (r *Repo) ListModelCallAttempts(ctx context.Context, callID string) ([]model.ModelCallAttempt, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT data FROM model_call_attempts
		WHERE call_id=$1
		ORDER BY started_at NULLS FIRST, attempt_id`, callID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list model call attempts: %w", err)
	}
	defer rows.Close()
	var out []model.ModelCallAttempt
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("postgres: scan model call attempt: %w", err)
		}
		var attempt model.ModelCallAttempt
		if err := json.Unmarshal(data, &attempt); err != nil {
			return nil, fmt.Errorf("postgres: decode model call attempt: %w", err)
		}
		out = append(out, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list model call attempts: %w", err)
	}
	return out, nil
}
