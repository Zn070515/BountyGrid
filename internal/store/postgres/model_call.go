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
func (r *Repo) ReserveModelCall(ctx context.Context, id string, rec model.CallRecord) (bool, error) {
	data, err := mustJSON(rec)
	if err != nil {
		return false, err
	}
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO model_calls
			(id, task_id, purpose, data_class, provider, model, prompt_hash,
			 response_hash, input_tokens, output_tokens, cost_cents, currency,
			 error, started_at, ended_at, updated_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),$16::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		id, nullString(string(rec.TaskID)), rec.Purpose, rec.DataClass, rec.Provider, rec.Model,
		rec.PromptHash, rec.ResponseHash, rec.Usage.InputTokens, rec.Usage.OutputTokens,
		rec.Cost.Amount, nullString(rec.Cost.Currency), rec.Error,
		nullTime(rec.StartedAt), nullTime(rec.EndedAt), data)
	if err != nil {
		return false, fmt.Errorf("postgres: reserve model call: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repo) CompleteModelCall(ctx context.Context, id string, rec model.CallRecord) error {
	data, err := mustJSON(rec)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		UPDATE model_calls SET
			provider=$2, model=$3, prompt_hash=$4, response_hash=$5,
			input_tokens=$6, output_tokens=$7, cost_cents=$8, currency=$9,
			error=$10, started_at=$11, ended_at=$12, updated_at=now(), data=$13::jsonb
		WHERE id=$1`,
		id, rec.Provider, rec.Model, rec.PromptHash, rec.ResponseHash,
		rec.Usage.InputTokens, rec.Usage.OutputTokens, rec.Cost.Amount,
		nullString(rec.Cost.Currency), rec.Error, nullTime(rec.StartedAt), nullTime(rec.EndedAt), data)
	if err != nil {
		return fmt.Errorf("postgres: complete model call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: model call %s not found", id)
	}
	return nil
}

func (r *Repo) GetModelCall(ctx context.Context, id string) (model.CallRecord, bool, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM model_calls WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.CallRecord{}, false, nil
		}
		return model.CallRecord{}, false, fmt.Errorf("postgres: get model call: %w", err)
	}
	var rec model.CallRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return model.CallRecord{}, false, fmt.Errorf("postgres: decode model call: %w", err)
	}
	return rec, true, nil
}
