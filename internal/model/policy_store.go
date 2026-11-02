package model

import (
	"context"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// ProgramStore supplies the stored program that owns a model request. The
// PostgreSQL Repo satisfies it.
type ProgramStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
}

// StorePolicyResolver derives the authoritative ModelPolicy for a request. The
// egress ceiling comes from the stored Program; Redacted and Escalation are
// operator attestations, never request fields. Without a ProgramID the resolver
// fails closed to LOCAL_ONLY and does not touch the store.
type StorePolicyResolver struct {
	Store       ProgramStore
	Attestation ModelPolicy
}

func (r StorePolicyResolver) ResolveModelPolicy(ctx context.Context, req ModelRequest) (ModelPolicy, error) {
	if req.ProgramID == "" {
		return ModelPolicy{Egress: domain.ModelEgressLocalOnly}, nil
	}
	if r.Store == nil {
		return ModelPolicy{}, fmt.Errorf("%w: program store is not configured", ErrInvalidRequest)
	}
	program, err := r.Store.GetProgram(ctx, req.ProgramID)
	if err != nil {
		return ModelPolicy{}, fmt.Errorf("model: load program %s: %w", req.ProgramID, err)
	}
	escalation := r.Attestation.Escalation
	if !escalation.Valid() {
		escalation = EscalationNone
	}
	return ModelPolicy{
		Egress:     program.ModelEgressPolicy.Effective(),
		Redacted:   r.Attestation.Redacted,
		Escalation: escalation,
	}, nil
}
