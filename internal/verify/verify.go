// Package verify is the rule-based Verification gate. It decides whether a
// Finding is ready to become a report: the asset must be in scope, the action
// must have been authorized, reproduction and impact must be evidenced, data
// must be minimized, duplicate risk must be checked, and AI-assisted work must
// have human verification. A model can never by itself produce VERIFIED.
package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

const (
	DefaultDuplicateRiskThreshold = 0.7
	ruleVersion                   = "rule/verify@0.1.0"
)

var ErrInvalidInput = errors.New("verify: invalid input")

// EvidenceChecker verifies that an EvidenceID exists in the durable store.
// When nil, the verifier can only check that IDs are syntactically non-empty.
type EvidenceChecker interface {
	HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error)
}

type Input struct {
	Finding  domain.Finding
	Program  domain.Program
	Decision *domain.ActionDecision
	// Evidence, when set, rejects fabricated evidence IDs.
	Evidence EvidenceChecker

	Reproduced           bool
	ReproductionEvidence []domain.EvidenceID
	ImpactVerified       bool
	ImpactEvidence       []domain.EvidenceID

	// DataMinimized is nil when minimization was not checked. false is an
	// observed violation, not an unknown.
	DataMinimized *bool

	DuplicateChecked bool
	DuplicateRisk    float64
	PubliclyKnown    bool

	AIAssisted           bool
	HumanVerified        bool
	HumanVerificationRef string

	VerifiedBy string
	Now        time.Time
}

type Options struct {
	DuplicateRiskThreshold float64
	Now                    func() time.Time
}

type Verifier interface {
	Verify(ctx context.Context, in Input) (domain.Verification, error)
}

type RuleVerifier struct {
	threshold float64
	now       func() time.Time
}

func NewRuleVerifier(opts Options) *RuleVerifier {
	if opts.DuplicateRiskThreshold <= 0 || opts.DuplicateRiskThreshold > 1 {
		opts.DuplicateRiskThreshold = DefaultDuplicateRiskThreshold
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RuleVerifier{threshold: opts.DuplicateRiskThreshold, now: opts.Now}
}

func (r *RuleVerifier) Verify(ctx context.Context, in Input) (domain.Verification, error) {
	if in.Program.ID == "" {
		return domain.Verification{}, fmt.Errorf("%w: program id is empty", ErrInvalidInput)
	}
	if in.DuplicateRisk < 0 || in.DuplicateRisk > 1 {
		return domain.Verification{}, fmt.Errorf("%w: duplicate risk %v out of range", ErrInvalidInput, in.DuplicateRisk)
	}
	now := in.Now
	if now.IsZero() {
		now = r.now().UTC()
	}
	verifiedBy := strings.TrimSpace(in.VerifiedBy)
	if verifiedBy == "" {
		verifiedBy = ruleVersion
	}

	v := domain.Verification{
		ID:                   fmt.Sprintf("ver/%s/%s", findingKey(in.Finding.ID), now.UTC().Format("20060102T150405.000000000")),
		FindingID:            in.Finding.ID,
		DuplicateChecked:     in.DuplicateChecked,
		DuplicateRisk:        in.DuplicateRisk,
		PubliclyKnown:        in.PubliclyKnown,
		AIAssisted:           in.AIAssisted,
		HumanVerified:        in.HumanVerified,
		HumanVerificationRef: in.HumanVerificationRef,
		VerifiedBy:           verifiedBy,
		VerifiedAt:           now,
		Reasons:              []string{},
		Evidence:             unionEvidence(in.Finding.Evidence, in.ReproductionEvidence, in.ImpactEvidence),
	}
	reject := func(verdict domain.VerificationVerdict, reason string) (domain.Verification, error) {
		v.Verdict = verdict
		v.Reasons = append(v.Reasons, reason)
		if verdict == domain.VerdictOutOfScope {
			v.InScope = false
		}
		return v, nil
	}

	// 1. Finding basics.
	if strings.TrimSpace(string(in.Finding.ID)) == "" || in.Finding.TaskID == "" ||
		strings.TrimSpace(in.Finding.Title) == "" || strings.TrimSpace(in.Finding.VulnType) == "" ||
		len(in.Finding.Evidence) == 0 {
		return reject(domain.VerdictNeedsEvidence, "FINDING_INCOMPLETE")
	}
	if in.Finding.ProgramID != in.Program.ID {
		return reject(domain.VerdictRejected, "PROGRAM_MISMATCH")
	}

	// 2. Scope: the asset must resolve to a canonical in-scope asset.
	if in.Finding.AssetID == nil {
		return reject(domain.VerdictNeedsEvidence, "ASSET_MISSING")
	}
	asset, _, ok := in.Program.Scope.ResolveAsset(*in.Finding.AssetID)
	if !ok {
		return reject(domain.VerdictOutOfScope, "ASSET_NOT_IN_SCOPE")
	}
	switch in.Program.Scope.CheckAsset(asset) {
	case domain.ScopeMatchIncluded:
		v.InScope = true
	case domain.ScopeMatchConditional:
		return reject(domain.VerdictNeedsEvidence, "ASSET_CONDITIONAL")
	default:
		return reject(domain.VerdictOutOfScope, "ASSET_OUT_OF_SCOPE")
	}

	// 3. The finding must come from an authorized execution/analysis action.
	if in.Decision == nil {
		return reject(domain.VerdictNeedsEvidence, "AUTHORIZATION_MISSING")
	}
	if in.Decision.Decision != domain.DecisionAllow {
		return reject(domain.VerdictRejected, "UNAUTHORIZED_ACTION")
	}
	// The decision must be structurally valid (ALLOW carries an expiry, the
	// request/approval bindings are coherent). Verification may happen after the
	// execution window, so wall-clock expiry is not re-checked here.
	if err := in.Decision.Validate(); err != nil {
		return reject(domain.VerdictRejected, "AUTHORIZATION_INVALID")
	}
	if in.Decision.ProgramID != in.Finding.ProgramID {
		return reject(domain.VerdictRejected, "AUTHORIZATION_PROGRAM_MISMATCH")
	}
	if in.Decision.Request.ID == "" {
		return reject(domain.VerdictNeedsEvidence, "AUTHORIZATION_CONTEXT_MISSING")
	}
	if in.Decision.Request.ProgramID != in.Finding.ProgramID || in.Decision.Request.Action != in.Decision.Action {
		return reject(domain.VerdictRejected, "AUTHORIZATION_CONTEXT_MISMATCH")
	}
	if !in.Decision.Action.NeedsScope() {
		return reject(domain.VerdictRejected, "INVALID_SOURCE_ACTION")
	}
	if in.Decision.Action.NeedsAsset() && !sameAsset(in.Decision.Request.AssetID, in.Finding.AssetID) {
		return reject(domain.VerdictRejected, "AUTHORIZATION_ASSET_MISMATCH")
	}

	// 4. Data minimization and public-known checks are hard gates.
	if in.DataMinimized == nil {
		return reject(domain.VerdictNeedsEvidence, "DATA_MINIMIZATION_UNKNOWN")
	}
	v.DataMinimized = *in.DataMinimized
	if !*in.DataMinimized {
		return reject(domain.VerdictRejected, "DATA_NOT_MINIMIZED")
	}
	if in.PubliclyKnown {
		return reject(domain.VerdictRejected, "PUBLICLY_KNOWN")
	}

	// 4b. Evidence IDs must exist in the durable store; a syntactically valid
	// ID is not proof.
	if in.Evidence != nil {
		for _, id := range v.Evidence {
			ok, err := in.Evidence.HasEvidence(ctx, id)
			if err != nil {
				return domain.Verification{}, fmt.Errorf("verify: check evidence %s: %w", id, err)
			}
			if !ok {
				return reject(domain.VerdictNeedsEvidence, "EVIDENCE_NOT_FOUND:"+string(id))
			}
		}
	}

	// 5. Duplicate risk. An unchecked finding cannot be verified.
	if !in.DuplicateChecked {
		return reject(domain.VerdictNeedsEvidence, "DUPLICATE_CHECK_MISSING")
	}
	if in.DuplicateRisk >= r.threshold {
		return reject(domain.VerdictDuplicateRisk, "DUPLICATE_RISK")
	}

	// 6. AI assistance must have a human verification trace.
	if in.AIAssisted {
		if !in.HumanVerified {
			return reject(domain.VerdictRejected, "AI_NOT_HUMAN_VERIFIED")
		}
		if strings.TrimSpace(in.HumanVerificationRef) == "" {
			return reject(domain.VerdictNeedsEvidence, "AI_HUMAN_VERIFICATION_REF_MISSING")
		}
	}

	// 7. Reproduction and impact must both be claimed and evidenced.
	if !in.Reproduced {
		return reject(domain.VerdictNeedsEvidence, "NOT_REPRODUCED")
	}
	if !hasEvidence(in.ReproductionEvidence) {
		return reject(domain.VerdictNeedsEvidence, "REPRODUCTION_EVIDENCE_MISSING")
	}
	v.Reproduced = true
	if !in.ImpactVerified {
		return reject(domain.VerdictNeedsEvidence, "IMPACT_NOT_VERIFIED")
	}
	if !hasEvidence(in.ImpactEvidence) {
		return reject(domain.VerdictNeedsEvidence, "IMPACT_EVIDENCE_MISSING")
	}
	v.ImpactVerified = true

	v.Verdict = domain.VerdictVerified
	v.Reasons = append(v.Reasons, "ALL_CHECKS_PASSED")
	if err := v.Validate(); err != nil {
		return domain.Verification{}, fmt.Errorf("verify: produced invalid verification: %w", err)
	}
	return v, nil
}

func findingKey(id domain.FindingID) string {
	if id == "" {
		return "unknown"
	}
	return string(id)
}

func sameAsset(a, b *domain.AssetID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func hasEvidence(ids []domain.EvidenceID) bool {
	for _, id := range ids {
		if strings.TrimSpace(string(id)) != "" {
			return true
		}
	}
	return false
}

func unionEvidence(groups ...[]domain.EvidenceID) []domain.EvidenceID {
	seen := make(map[domain.EvidenceID]struct{})
	out := make([]domain.EvidenceID, 0)
	for _, ids := range groups {
		for _, id := range ids {
			if strings.TrimSpace(string(id)) == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
