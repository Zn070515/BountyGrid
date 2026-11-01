package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/redact"
	"github.com/jackc/pgx/v5"
)

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx, so the same bump
// statement can run standalone or inside the program-upsert transaction.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// BumpRevocationEpoch atomically increments the program's authorization epoch
// and returns the new value. Every execution grant stamped with an older epoch
// is invalid immediately. The first bump inserts epoch 1.
//
// A single INSERT ... ON CONFLICT DO UPDATE keeps the read-modify-write atomic,
// so concurrent bumps cannot return the same epoch.
func (r *Repo) BumpRevocationEpoch(ctx context.Context, program domain.ProgramID) (uint64, error) {
	return bumpRevocationEpoch(ctx, r.Pool, program)
}

func bumpRevocationEpoch(ctx context.Context, q queryRower, program domain.ProgramID) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: bump revocation epoch: empty program id")
	}
	var epoch int64
	err := q.QueryRow(ctx, `
		INSERT INTO revocation_epochs (program_id, epoch, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (program_id) DO UPDATE SET
			epoch = revocation_epochs.epoch + 1,
			updated_at = now()
		RETURNING epoch`,
		string(program),
	).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("postgres: bump revocation epoch: %w", err)
	}
	if epoch < 0 {
		return 0, fmt.Errorf("postgres: bump revocation epoch: negative epoch %d", epoch)
	}
	return uint64(epoch), nil
}

// revocationReason is the audited form of an operator-supplied revocation
// reason. A reason that looks like it contains a secret is never stored
// verbatim: only its SHA-256 (for correlation) and the finding kinds are kept.
type revocationReason struct {
	Text     string
	Redacted bool
	Hash     string
	Kinds    []string
}

// sanitizeRevocationReason keeps the audit trail useful without letting a
// logging guardrail block an emergency revocation. The revocation itself must
// stay atomic (epoch bump + audit), so the reason is sanitized before the
// event is validated rather than aborting the transaction.
func sanitizeRevocationReason(reason string) revocationReason {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return revocationReason{}
	}
	findings := redact.Scan(reason)
	if len(findings) == 0 {
		return revocationReason{Text: reason}
	}
	seen := make(map[string]struct{}, len(findings))
	kinds := make([]string, 0, len(findings))
	for _, finding := range findings {
		if _, duplicate := seen[finding.Kind]; duplicate {
			continue
		}
		seen[finding.Kind] = struct{}{}
		kinds = append(kinds, finding.Kind)
	}
	slices.Sort(kinds)
	sum := sha256.Sum256([]byte(reason))
	return revocationReason{
		Redacted: true,
		Hash:     "sha256:" + hex.EncodeToString(sum[:]),
		Kinds:    kinds,
	}
}

type revokeAuditPayload struct {
	Epoch          uint64   `json:"epoch"`
	Reason         string   `json:"reason,omitempty"`
	ReasonRedacted bool     `json:"reason_redacted,omitempty"`
	ReasonHash     string   `json:"reason_hash,omitempty"`
	RedactionKinds []string `json:"redaction_kinds,omitempty"`
}

// RevokeProgramWithAudit bumps the durable epoch and appends the grant.revoked
// audit event in one transaction. An effective revocation without its audit
// trail must not be committed, so a validation failure rolls the bump back.
// Operator reasons are sanitized first: a secret-like reason is redacted
// (hash + finding kinds) instead of aborting the kill switch.
func (r *Repo) RevokeProgramWithAudit(ctx context.Context, program domain.ProgramID, reason string, at time.Time) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: revoke program: empty program id")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("postgres: begin revoke program: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	epoch, err := bumpRevocationEpoch(ctx, tx, program)
	if err != nil {
		return 0, err
	}
	reasonInfo := sanitizeRevocationReason(reason)
	payload, err := json.Marshal(revokeAuditPayload{
		Epoch:          epoch,
		Reason:         reasonInfo.Text,
		ReasonRedacted: reasonInfo.Redacted,
		ReasonHash:     reasonInfo.Hash,
		RedactionKinds: reasonInfo.Kinds,
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: marshal revoke audit: %w", err)
	}
	event := audit.Event{
		Type: "grant.revoked", EntityType: "program", EntityID: string(program),
		ActorKind: string(domain.ActorHuman), ActorID: "cli",
		Payload: payload, RecordedAt: at,
	}
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("postgres: revoke audit rejected: %w", err)
	}
	if _, err := appendAudit(ctx, tx, event); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("postgres: commit revoke program: %w", err)
	}
	return epoch, nil
}

// programSupersedesAuthority reports whether replacing old with next must
// invalidate outstanding execution grants. It fires when the platform identity
// changes, the program leaves RUNNING (including to UNKNOWN), or the Scope /
// automation+constraints snapshot changes. Pure observation refreshes (new
// observed_at, same content) do not revoke.
func programSupersedesAuthority(old, next domain.Program) bool {
	if old.ID != next.ID || old.Platform != next.Platform {
		return true
	}
	if old.Status.Status != next.Status.Status && next.Status.Status != domain.StatusRunning {
		return true
	}
	if old.Scope.Revision() != next.Scope.Revision() {
		return true
	}
	return old.PolicyRevision() != next.PolicyRevision()
}

// Epoch returns the program's current authorization epoch. A program that has
// never been revoked is at epoch 0.
func (r *Repo) Epoch(ctx context.Context, program domain.ProgramID) (uint64, error) {
	if program == "" {
		return 0, fmt.Errorf("postgres: revocation epoch: empty program id")
	}
	var epoch int64
	err := r.Pool.QueryRow(ctx,
		`SELECT epoch FROM revocation_epochs WHERE program_id = $1`, string(program),
	).Scan(&epoch)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("postgres: revocation epoch: %w", err)
	}
	if epoch < 0 {
		return 0, fmt.Errorf("postgres: revocation epoch: negative epoch %d", epoch)
	}
	return uint64(epoch), nil
}
