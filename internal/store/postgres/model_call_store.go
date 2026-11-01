package postgres

import (
	"context"

	"github.com/Zn070515/BountyGrid/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelCallStore adapts Repo's model-call methods to model.CallStore. The Repo
// keeps descriptive method names (ReserveModelCall/CompleteModelCall/...); this
// wrapper is the production CallStore the Router can consume directly.
type ModelCallStore struct {
	Repo *Repo
}

func NewModelCallStore(pool *pgxpool.Pool) *ModelCallStore {
	return &ModelCallStore{Repo: NewRepo(pool)}
}

func (s *ModelCallStore) Reserve(ctx context.Context, id string, rec model.CallRecord) (bool, error) {
	return s.Repo.ReserveModelCall(ctx, id, rec)
}

func (s *ModelCallStore) Complete(ctx context.Context, id string, rec model.CallRecord) error {
	return s.Repo.CompleteModelCall(ctx, id, rec)
}

func (s *ModelCallStore) Get(ctx context.Context, id string) (model.CallRecord, bool, error) {
	return s.Repo.GetModelCall(ctx, id)
}

func (s *ModelCallStore) Reclaim(ctx context.Context, id string) error {
	return s.Repo.ReclaimModelCall(ctx, id)
}

var _ model.CallStore = (*ModelCallStore)(nil)
