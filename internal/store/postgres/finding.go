package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repo) GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM findings WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Finding{}, ErrNotFound
		}
		return domain.Finding{}, fmt.Errorf("postgres: get finding: %w", err)
	}
	var f domain.Finding
	if err := json.Unmarshal(data, &f); err != nil {
		return domain.Finding{}, fmt.Errorf("postgres: decode finding: %w", err)
	}
	return f, nil
}
