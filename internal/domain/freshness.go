package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Freshness windows for authorization inputs. These are deliberately separate:
// a fresh Program.Status does not make an old Scope, AutomationPolicy or
// AccountCapability current. Execution/submission actions require every
// authorization input to be within its own window; any stale input fails
// closed with REQUIRE_MORE_EVIDENCE.
const (
	// ScopeMaxAge is how long a scope snapshot may authorize execution. Platform
	// scopes can be edited (assets removed, temporary bans added) between list
	// syncs, so the scope observation is checked independently.
	ScopeMaxAge = 7 * 24 * time.Hour
	// PolicyMaxAge bounds the automation/constraints snapshot carried by the
	// Program. Automation rules can be paused for a campaign without the
	// program status changing.
	PolicyMaxAge = 7 * 24 * time.Hour
	// AccountCapabilityMaxAge bounds the account eligibility observation
	// (identity/compliance/practical certification/bank binding).
	AccountCapabilityMaxAge = 24 * time.Hour
	// ProgramStatusMaxAge is the fallback window when a state has no explicit
	// ValidUntil. States normally carry one; this keeps a zero value fail-closed
	// rather than infinitely fresh.
	ProgramStatusMaxAge = 30 * 24 * time.Hour
)

// freshAt reports whether an observation with an optional explicit expiry is
// still usable at now. A zero timestamp is unknown and therefore stale.
func freshAt(observedAt, validUntil time.Time, maxAge time.Duration, now time.Time) bool {
	if observedAt.IsZero() || now.Before(observedAt.Add(-time.Minute)) {
		return false
	}
	deadline := observedAt.Add(maxAge)
	if !validUntil.IsZero() && validUntil.Before(deadline) {
		deadline = validUntil
	}
	return deadline.After(now)
}

// FreshAt reports whether this scope snapshot may authorize execution now.
func (s Scope) FreshAt(now time.Time) bool {
	return freshAt(s.ObservedAt, s.ValidUntil, ScopeMaxAge, now)
}

// FreshUntil is the absolute deadline for this scope snapshot.
func (s Scope) FreshUntil() time.Time {
	deadline := s.ObservedAt.Add(ScopeMaxAge)
	if !s.ValidUntil.IsZero() && s.ValidUntil.Before(deadline) {
		deadline = s.ValidUntil
	}
	return deadline
}

// Revision is a deterministic digest of the disclosure/access decision and the
// asset lists. It is recorded with decisions so an approval can be tied to the
// exact scope snapshot it was evaluated against.
func (s Scope) Revision() string {
	payload := struct {
		Visibility    ScopeVisibility `json:"visibility"`
		Access        ScopeAccess     `json:"access"`
		ContentsKnown bool            `json:"contents_known"`
		Include       []Asset         `json:"include,omitempty"`
		Exclude       []Asset         `json:"exclude,omitempty"`
		Conditional   []Asset         `json:"conditional,omitempty"`
	}{
		Visibility: s.Visibility, Access: s.Access, ContentsKnown: s.ContentsKnown,
		Include: s.Include, Exclude: s.Exclude, Conditional: s.Conditional,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// FreshAt reports whether the account's observed capability may be used to
// authorize execution now.
func (a Account) CapabilityFreshAt(now time.Time) bool {
	at := a.capabilityObservedAt()
	return freshAt(at, a.Capability.ValidUntil, AccountCapabilityMaxAge, now)
}

// CapabilityFreshUntil is the absolute deadline for the account capability.
func (a Account) CapabilityFreshUntil() time.Time {
	deadline := a.capabilityObservedAt().Add(AccountCapabilityMaxAge)
	if !a.Capability.ValidUntil.IsZero() && a.Capability.ValidUntil.Before(deadline) {
		deadline = a.Capability.ValidUntil
	}
	return deadline
}

func (a Account) capabilityObservedAt() time.Time {
	if !a.Capability.EvaluatedAt.IsZero() {
		return a.Capability.EvaluatedAt
	}
	return a.ObservedAt
}

// PolicyFreshAt reports whether the program's automation/constraints snapshot
// is recent enough to authorize execution now. A zero PolicyObservedAt is
// unknown (the policy may never have been read), not "fresh because the
// program row was synced".
func (p Program) PolicyFreshAt(now time.Time) bool {
	return freshAt(p.PolicyObservedAt, p.PolicyValidUntil, PolicyMaxAge, now)
}

// PolicyFreshUntil is the absolute deadline for the automation/constraints
// snapshot.
func (p Program) PolicyFreshUntil() time.Time {
	deadline := p.PolicyObservedAt.Add(PolicyMaxAge)
	if !p.PolicyValidUntil.IsZero() && p.PolicyValidUntil.Before(deadline) {
		deadline = p.PolicyValidUntil
	}
	return deadline
}

// PolicyRevision is a deterministic digest of the automation and constraint
// snapshot. It is recorded with decisions so an approval can be tied to the
// exact policy it was evaluated against.
func (p Program) PolicyRevision() string {
	raw, err := json.Marshal(struct {
		Automation  AutomationPolicy `json:"automation"`
		Constraints Constraints      `json:"constraints"`
	}{Automation: p.Automation, Constraints: p.Constraints})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// StatusFreshAt reports whether the program status may authorize execution now.
// An explicit ValidUntil is authoritative; otherwise the status is bounded by
// ProgramStatusMaxAge.
func (p Program) StatusFreshAt(now time.Time) bool {
	if !p.Status.ValidUntil.IsZero() {
		return p.Status.ValidUntil.After(now)
	}
	return freshAt(p.Status.ObservedAt, time.Time{}, ProgramStatusMaxAge, now)
}

// StatusFreshUntil is the absolute deadline for the program status.
func (p Program) StatusFreshUntil() time.Time {
	if !p.Status.ValidUntil.IsZero() {
		return p.Status.ValidUntil
	}
	return p.Status.ObservedAt.Add(ProgramStatusMaxAge)
}
