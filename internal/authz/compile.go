package authz

import (
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// CompiledPermissions is the trusted compiler output for one Evidence
// document. Only Permissions are candidates for platform authority; Restricted
// lines are returned separately because a conditional permission requires
// human review and can never relax automatically (see ADR-056).
type CompiledPermissions struct {
	Permissions []PermissionFact
	Restricted  []ExtractedPermission
}

// CompilePermissions turns the explicit PERMISSION lines in one Evidence
// document into PermissionFacts bound to program. It is the bridge between the
// EvidenceContentReader (which returns the exact redacted text that was
// hashed) and BuildPlatformPolicy (which proves the facts).
//
// The compiler is intentionally not an authority: it only parses lines that a
// platform-specific Reader/compiler has already written into the evidence. It
// never infers a permission from prose, never fills a missing host/method, and
// never decides that a line applies to another program. Lines for other
// programs are ignored; restricted lines are separated out.
//
// Provenance that a PERMISSION line does not carry is derived conservatively:
//   - ObservedAt is the Evidence capture time (a zero capture time is an error,
//     not "now");
//   - ValidUntil is the observed time plus the policy freshness window, capped
//     by the program's current policy deadline;
//   - PolicyRevision is the program's current policy digest;
//   - Justification names the evidence, so an audit can trace the fact back to
//     the document it came from.
//
// BuildPlatformPolicy still re-verifies every fact: evidence existence,
// platform, content hash, source type, revision, freshness and quote matching.
func CompilePermissions(program domain.Program, ev domain.Evidence, content string) (CompiledPermissions, error) {
	var out CompiledPermissions
	if ev.CapturedAt.IsZero() {
		return out, fmt.Errorf("authz: evidence %s has no captured_at; permission facts need an observation time", ev.ID)
	}
	revision := program.PolicyRevision()
	if revision == "" {
		return out, fmt.Errorf("authz: program %s has no policy revision", program.ID)
	}
	extracted, err := ExtractPermissions(content)
	if err != nil {
		return out, fmt.Errorf("authz: evidence %s: %w", ev.ID, err)
	}
	validUntil := permissionValidUntil(program, ev.CapturedAt)
	for _, extractedPermission := range extracted {
		if extractedPermission.ProgramID != program.ID {
			continue
		}
		if len(extractedPermission.Restrictions) > 0 {
			out.Restricted = append(out.Restricted, extractedPermission)
			continue
		}
		out.Permissions = append(out.Permissions, PermissionFact{
			EvidenceID:          ev.ID,
			EvidenceContentHash: ev.ContentHash,
			EvidenceQuote:       extractedPermission.Quote,
			Platform:            ev.Platform,
			ProgramID:           extractedPermission.ProgramID,
			Action:              extractedPermission.Action,
			Hosts:               append([]string(nil), extractedPermission.Hosts...),
			Methods:             append([]string(nil), extractedPermission.Methods...),
			PathPrefixes:        append([]string(nil), extractedPermission.PathPrefixes...),
			ObservedAt:          ev.CapturedAt,
			ValidUntil:          validUntil,
			PolicyRevision:      revision,
			Justification:       fmt.Sprintf("evidence %s states an explicit PERMISSION line", ev.ID),
		})
	}
	return out, nil
}

// permissionValidUntil bounds a compiled permission by both the evidence
// observation window and the program's policy window. A program that has never
// observed its policy (zero PolicyObservedAt) yields an already-expired window,
// so verification rejects the fact instead of granting authority from an
// unknown snapshot.
func permissionValidUntil(program domain.Program, observedAt time.Time) time.Time {
	deadline := observedAt.Add(domain.PolicyMaxAge)
	if policyDeadline := program.PolicyFreshUntil(); !policyDeadline.IsZero() && policyDeadline.Before(deadline) {
		deadline = policyDeadline
	}
	return deadline
}
