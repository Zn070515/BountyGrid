package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrApprovalConsumed = errors.New("postgres: approval already consumed")

// PostgresApprovalLedger makes single-use human approvals durable across
// process restarts. INSERT ... ON CONFLICT DO NOTHING is the atomic gate.
type PostgresApprovalLedger struct {
	Pool *pgxpool.Pool
}

func NewPostgresApprovalLedger(pool *pgxpool.Pool) *PostgresApprovalLedger {
	return &PostgresApprovalLedger{Pool: pool}
}

func (l *PostgresApprovalLedger) Consume(ctx context.Context, approvalID string) error {
	if approvalID == "" {
		return fmt.Errorf("postgres: empty approval id")
	}
	tag, err := l.Pool.Exec(ctx, `
		INSERT INTO consumed_approvals (approval_id) VALUES ($1)
		ON CONFLICT (approval_id) DO NOTHING`, approvalID)
	if err != nil {
		return fmt.Errorf("postgres: consume approval: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrApprovalConsumed
	}
	return nil
}
