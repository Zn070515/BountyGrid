package authz

import (
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// RequiresFreshState reports whether the action needs a fresh Program.Status.
// Viewing a program or its rules deliberately does not.
func RequiresFreshState(a domain.Action) bool {
	switch a {
	case domain.ActionViewProgram, domain.ActionViewRules:
		return false
	default:
		return true
	}
}

// RequiresFreshAuthorizationInputs reports whether the action depends on the
// scope contents, automation/constraints snapshot and account capability. Each
// input has its own freshness window; a fresh Program.Status does not refresh
// them.
func RequiresFreshAuthorizationInputs(a domain.Action) bool {
	return a.NeedsScope() || a == domain.ActionReportSubmit
}

// CheckAuthorizationFreshness validates every freshness input an action needs.
// It returns nil when all inputs are usable, or a *FreshnessError carrying one
// of the stable STALE_* / LOW_CONFIDENCE_* reason codes. The order matches the
// policy engine's historical evaluation order so reason codes stay stable.
func CheckAuthorizationFreshness(program domain.Program, account domain.Account, action domain.Action, now time.Time) error {
	if !RequiresFreshState(action) {
		return nil
	}
	if !program.StatusFreshAt(now) {
		return &FreshnessError{Reason: ReasonStaleProgramState}
	}
	if program.Status.Confidence < MinExecutionConfidence {
		return &FreshnessError{Reason: ReasonLowConfidenceState}
	}
	if !RequiresFreshAuthorizationInputs(action) {
		return nil
	}
	if !program.PolicyFreshAt(now) {
		return &FreshnessError{Reason: ReasonStaleAutomationPolicy}
	}
	if !account.CapabilityFreshAt(now) {
		return &FreshnessError{Reason: ReasonStaleAccountCapability}
	}
	if !program.Scope.FreshAt(now) {
		return &FreshnessError{Reason: ReasonStaleScope}
	}
	return nil
}

// CheckRevisionBinding verifies that the supplied revision strings match the
// current program snapshot. Empty arguments mean "this caller does not bind
// that revision" and are skipped. This is used to tie platform evidence and
// approvals to the exact scope/policy they were observed against.
func CheckRevisionBinding(program domain.Program, scopeRevision, policyRevision string) error {
	if rev := trimRevision(scopeRevision); rev != "" {
		want := program.Scope.Revision()
		if rev != want {
			return &RevisionError{Field: "scope", Want: want, Got: rev, Reason: ReasonScopeRevisionMismatch}
		}
	}
	if rev := trimRevision(policyRevision); rev != "" {
		want := program.PolicyRevision()
		if rev != want {
			return &RevisionError{Field: "policy", Want: want, Got: rev, Reason: ReasonPolicyRevisionMismatch}
		}
	}
	return nil
}
