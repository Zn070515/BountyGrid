// Package authz holds the reusable authorization-chain primitives shared by the
// policy engine, the Action Gateway, verification and submission.
//
// The package exists for two reasons:
//
//  1. Scope and freshness checks were duplicated across call sites, which made
//     it easy for one of them to drift. They now share one fail-closed
//     implementation.
//  2. Platform authority is not operator policy. A hand-written rule file must
//     never be able to claim that the platform allows an action. Platform
//     authorization rules are compiled from Evidence, verified here against the
//     evidence store, the program, its policy revision and its freshness window,
//     and only then may they relax a CONDITIONAL automation ceiling.
//
// authz never executes anything and never creates an ActionDecision. It answers
// narrow questions that a policy/gateway caller combines with its own domain
// gates.
package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

const (
	// ClockSkew tolerates a small clock difference between the observer and the
	// decision process. A rule observed slightly in the future is still usable;
	// one observed materially in the future is not.
	ClockSkew = time.Minute

	// MinExecutionConfidence is the floor for a state that drives an execution
	// or submission decision.
	MinExecutionConfidence domain.Confidence = 0.8
)

// EvidenceStore resolves durable Evidence records. Verification is a security
// boundary: a missing store fails closed.
type EvidenceStore interface {
	GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error)
}

// Freshness reason codes returned by CheckAuthorizationFreshness. They are the
// stable reason codes recorded on REQUIRE_MORE_EVIDENCE decisions.
const (
	ReasonStaleProgramState      = "STALE_PROGRAM_STATE"
	ReasonLowConfidenceState     = "LOW_CONFIDENCE_STATE"
	ReasonStaleAutomationPolicy  = "STALE_AUTOMATION_POLICY"
	ReasonStaleAccountCapability = "STALE_ACCOUNT_CAPABILITY"
	ReasonStaleScope             = "STALE_SCOPE"
)

// FreshnessError reports which authorization input was not fresh enough. It is
// a rejection, not an infrastructure failure.
type FreshnessError struct {
	Reason string
}

func (e *FreshnessError) Error() string { return "authz: " + e.Reason }

// Revision reason codes returned by CheckRevisionBinding.
const (
	ReasonScopeRevisionMismatch  = "SCOPE_REVISION_MISMATCH"
	ReasonPolicyRevisionMismatch = "POLICY_REVISION_MISMATCH"
)

// RevisionError reports that a caller-supplied revision does not match the
// program snapshot it is being used with.
type RevisionError struct {
	Field  string
	Want   string
	Got    string
	Reason string
}

func (e *RevisionError) Error() string {
	return fmt.Sprintf("authz: %s revision mismatch (want %s, got %s)", e.Field, e.Want, e.Got)
}

// Scope errors returned by ResolveInScopeAsset.
var (
	ErrAssetRequired = errors.New("authz: asset id is required")
	ErrAssetUnknown  = errors.New("authz: asset is not present in the program scope")
)

// Platform authority rejection reason codes.
const (
	ReasonEvidenceStoreMissing     = "PLATFORM_EVIDENCE_STORE_MISSING"
	ReasonEvidenceMissing          = "PLATFORM_EVIDENCE_MISSING"
	ReasonEvidencePlatformMismatch = "PLATFORM_EVIDENCE_PLATFORM_MISMATCH"
	ReasonRuleProgramMismatch      = "PLATFORM_RULE_PROGRAM_MISMATCH"
	ReasonRuleExpired              = "PLATFORM_RULE_EXPIRED"
	ReasonRuleFromFuture           = "PLATFORM_RULE_FROM_FUTURE"
)

// RejectedError reports that platform authority could not be verified. It is
// never an ALLOW and never an infrastructure retry.
type RejectedError struct {
	Reason string
	Detail string
}

func (e *RejectedError) Error() string {
	if e.Detail == "" {
		return "authz: " + e.Reason
	}
	return "authz: " + e.Reason + ": " + e.Detail
}

func reject(reason, format string, args ...any) error {
	detail := ""
	if format != "" {
		detail = fmt.Sprintf(format, args...)
	}
	return &RejectedError{Reason: reason, Detail: detail}
}

func trimRevision(v string) string { return strings.TrimSpace(v) }
