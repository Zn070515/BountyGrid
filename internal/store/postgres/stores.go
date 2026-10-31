package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditStore implements audit.Store on PostgreSQL.
type AuditStore struct {
	Pool *pgxpool.Pool
}

func NewAuditStore(pool *pgxpool.Pool) *AuditStore { return &AuditStore{Pool: pool} }

func (s *AuditStore) Append(ctx context.Context, e audit.Event) (int64, error) {
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now().UTC()
	}
	// Keep parity with the in-memory store: reject invalid JSON and payloads
	// containing secrets before they reach PostgreSQL.
	if err := e.Validate(); err != nil {
		return 0, err
	}
	var id int64
	payload := string(e.Payload)
	if payload == "" {
		payload = "{}"
	}
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO audit_events
			(event_type, entity_type, entity_id, actor_kind, actor_id, payload, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		RETURNING id`,
		e.Type, e.EntityType, e.EntityID, e.ActorKind, e.ActorID, payload, e.RecordedAt,
	).Scan(&id)
	return id, err
}

func (s *AuditStore) List(ctx context.Context, f audit.Filter) ([]audit.Event, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, event_type, entity_type, entity_id, actor_kind, actor_id, payload, recorded_at
		FROM audit_events
		WHERE ($1 = '' OR event_type = $1)
		  AND ($2 = '' OR entity_type = $2)
		  AND ($3 = '' OR entity_id = $3)
		ORDER BY id DESC LIMIT $4`, f.Type, f.EntityType, f.EntityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Event
	for rows.Next() {
		var e audit.Event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.EntityType, &e.EntityID, &e.ActorKind, &e.ActorID, &payload, &e.RecordedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// FactStore implements facts.Store on PostgreSQL.
type FactStore struct {
	Repo *Repo
}

func NewFactStore(pool *pgxpool.Pool) *FactStore { return &FactStore{Repo: NewRepo(pool)} }

func (s *FactStore) Put(ctx context.Context, fs []domain.ObservedFact) error {
	return s.Repo.PutFacts(ctx, fs)
}

func (s *FactStore) ListByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error) {
	return s.Repo.ListFactsByProgram(ctx, id)
}

func (s *FactStore) Supersede(ctx context.Context, id domain.FactID, by domain.FactID) error {
	return s.Repo.SupersedeFact(ctx, id, by)
}

var _ audit.Store = (*AuditStore)(nil)
var _ facts.Store = (*FactStore)(nil)
