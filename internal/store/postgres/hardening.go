package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// queryExecer is satisfied by both *pgxpool.Pool and pgx.Tx.
type queryExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RecordDecisionWithAudit writes a decision and its audit event in the same
// transaction. A control-plane decision must never exist without its audit
// record.
func (r *Repo) RecordDecisionWithAudit(ctx context.Context, d domain.ActionDecision, e audit.Event) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin decision+audit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := putDecision(ctx, tx, d); err != nil {
		return err
	}
	if _, err := appendAudit(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func putDecision(ctx context.Context, ex queryExecer, d domain.ActionDecision) error {
	reasons, err := mustJSON(d.Reasons)
	if err != nil {
		return err
	}
	gates, err := mustJSON(d.GatesEvaluated)
	if err != nil {
		return err
	}
	approval, err := mustJSON(d.HumanApproval)
	if err != nil {
		return err
	}
	constraints, err := mustJSON(d.Constraints)
	if err != nil {
		return err
	}
	data, err := mustJSON(d)
	if err != nil {
		return err
	}
	_, err = ex.Exec(ctx, `
		INSERT INTO action_decisions
			(id, request_id, program_id, action, decision, reasons, gates_evaluated,
			 policy_version, decided_at, human_approval, expires_at, constraints, data)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10::jsonb,$11,$12::jsonb,$13::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		d.ID, d.RequestID, d.ProgramID, d.Action, d.Decision, reasons, gates,
		d.PolicyVersion, d.DecidedAt, approval, d.ExpiresAt, constraints, data)
	if err != nil {
		return fmt.Errorf("postgres: put decision: %w", err)
	}
	return nil
}

func appendAudit(ctx context.Context, ex queryExecer, e audit.Event) (int64, error) {
	payload := string(e.Payload)
	if payload == "" {
		payload = "{}"
	}
	var id int64
	err := ex.QueryRow(ctx, `
		INSERT INTO audit_events
			(event_type, entity_type, entity_id, actor_kind, actor_id, payload, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		RETURNING id`,
		e.Type, e.EntityType, e.EntityID, e.ActorKind, e.ActorID, payload, e.RecordedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: append audit: %w", err)
	}
	return id, nil
}

// ---------- Execution receipts ----------

func (r *Repo) GetReceipt(ctx context.Context, idempotencyKey string) (domain.ExecutionReceipt, error) {
	var rec domain.ExecutionReceipt
	var data []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT data FROM execution_receipts WHERE idempotency_key=$1`, idempotencyKey).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ExecutionReceipt{}, domain.ErrReceiptNotFound
		}
		return domain.ExecutionReceipt{}, fmt.Errorf("postgres: get receipt: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.ExecutionReceipt{}, fmt.Errorf("postgres: decode receipt: %w", err)
	}
	return rec, nil
}

func (r *Repo) CreateReceipt(ctx context.Context, rec domain.ExecutionReceipt) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(rec)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		INSERT INTO execution_receipts
			(id, idempotency_key, decision_id, tool_name, action, program_id,
			 status, output, error, started_at, updated_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		rec.ID, rec.IdempotencyKey, rec.DecisionID, rec.ToolName, rec.Action, rec.ProgramID,
		rec.Status, rec.Output, rec.Error, rec.StartedAt, rec.UpdatedAt, data)
	if err != nil {
		return fmt.Errorf("postgres: create receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrReceiptExists
	}
	return nil
}

func (r *Repo) UpdateReceipt(ctx context.Context, rec domain.ExecutionReceipt) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(rec)
	if err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		UPDATE execution_receipts SET
			status=$2, output=$3, error=$4, updated_at=$5, data=$6::jsonb
		WHERE idempotency_key=$1`,
		rec.IdempotencyKey, rec.Status, rec.Output, rec.Error, rec.UpdatedAt, data)
	if err != nil {
		return fmt.Errorf("postgres: update receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrReceiptNotFound
	}
	return nil
}

// ---------- Research task lifecycle ----------

// LatestResearchTaskTimes returns the most recent task creation time per
// program, used by the scheduler's cooldown.
func (r *Repo) LatestResearchTaskTimes(ctx context.Context, platform domain.Platform) (map[domain.ProgramID]time.Time, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT rt.program_id, MAX(rt.created_at)
		FROM research_tasks rt
		JOIN programs p ON p.id = rt.program_id
		WHERE ($1 = '' OR p.platform = $1)
		GROUP BY rt.program_id`, string(platform))
	if err != nil {
		return nil, fmt.Errorf("postgres: latest research task times: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.ProgramID]time.Time)
	for rows.Next() {
		var pid string
		var at time.Time
		if err := rows.Scan(&pid, &at); err != nil {
			return nil, err
		}
		out[domain.ProgramID(pid)] = at
	}
	return out, rows.Err()
}
