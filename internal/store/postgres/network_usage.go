package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ConsumeNetworkUsage atomically increments the daily counter for one
// account/program/interface and reports whether the request fits under limit.
//
// A single INSERT ... ON CONFLICT DO UPDATE ... WHERE count < limit RETURNING
// keeps the check and the increment in one statement, so concurrent workers
// cannot both pass the same last slot. A limit of 0 means "no ceiling, keep
// counting"; the row is still created/updated.
//
// businessDate is the Asia/Shanghai calendar date in "YYYY-MM-DD" form.
func (r *Repo) ConsumeNetworkUsage(ctx context.Context, accountID, programID, interfaceName, businessDate string, limit int64) (bool, error) {
	var count int64
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO network_usage
			(account_id, program_id, interface, business_date, count, updated_at)
		VALUES ($1, $2, $3, $4::date, 1, now())
		ON CONFLICT (account_id, program_id, interface, business_date)
		DO UPDATE SET
			count = network_usage.count + 1,
			updated_at = now()
		WHERE $5 <= 0 OR network_usage.count < $5
		RETURNING count`,
		accountID, programID, interfaceName, businessDate, limit,
	).Scan(&count)
	if err != nil {
		if err == pgx.ErrNoRows {
			// The conflict update was filtered out: the limit is reached.
			return false, nil
		}
		return false, fmt.Errorf("postgres: consume network usage: %w", err)
	}
	return true, nil
}
