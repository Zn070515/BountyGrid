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
// Verification is a security boundary: without a checker the verifier fails
// closed and cannot produce VERIFIED.
type EvidenceChecker interface {
	HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error)
}

// ReceiptChecker resolves the execution receipts recorded on a Finding. An
// ALLOW decision proves authorization, not execution; a completed receipt is
// the proof that the action actually ran and produced the evidence.
type ReceiptChecker interface {
	GetReceiptByID(ctx context.Context, id string) (domain.ExecutionReceipt, error)
}

// HumanReviewChecker resolves a durable human review by ID.
type HumanReviewChecker interface {
	GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error)
}

type Input struct {
	Finding  domain.Finding
	Program  domain.Program
	Decision *domain.ActionDecision
	// Evidence must be set for a VERIFIED verdict; a nil checker is unknown, not
	// "everything exists".
	Evidence EvidenceChecker
	// Receipts may be supplied directly (tests, already-resolved callers) or
	// resolved from Finding.ExecutionReceipts through ReceiptChecker.
	Receipts       []domain.ExecutionReceipt
	ReceiptChecker ReceiptChecker
	// HumanReview may be supplied directly or resolved from HumanReviewID.
	HumanReview       *domain.HumanReview
	HumanReviewID     string
	HumanReviewLookup HumanReviewChecker

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
	// Evidence is the default checker used when Input.Evidence is nil. There is
	// no mode in which verification succeeds without one.
	Evidence EvidenceChecker
}

type Verifier interface {
	Verify(ctx context.Context, in Input) (domain.Verification, error)
}

type RuleVerifier struct {
	threshold float64
	now       func() time.Time
	evidence  EvidenceChecker
}

func NewRuleVerifier(opts Options) *RuleVerifier {
	if opts.DuplicateRiskThreshold <= 0 || opts.DuplicateRiskThreshold > 1 {
		opts.DuplicateRiskThreshold = DefaultDuplicateRiskThreshold
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RuleVerifier{threshold: opts.DuplicateRiskThreshold, now: opts.Now, evidence: opts.Evidence}
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
	// ID is not proof. A missing checker is unknown, not "all IDs exist", so the
	// verifier fails closed.
	checker := in.Evidence
	if checker == nil {
		checker = r.evidence
	}
	if checker == nil {
		return reject(domain.VerdictNeedsEvidence, "EVIDENCE_CHECKER_MISSING")
	}
	for _, id := range v.Evidence {
		ok, err := checker.HasEvidence(ctx, id)
		if err != nil {
			return domain.Verification{}, fmt.Errorf("verify: check evidence %s: %w", id, err)
		}
		if !ok {
			return reject(domain.VerdictNeedsEvidence, "EVIDENCE_NOT_FOUND:"+string(id))
		}
	}

	// 5. Duplicate risk. An unchecked finding cannot be verified.
	if !in.DuplicateChecked {
		return reject(domain.VerdictNeedsEvidence, "DUPLICATE_CHECK_MISSING")
	}
	if in.DuplicateRisk >= r.threshold {
		return reject(domain.VerdictDuplicateRisk, "DUPLICATE_RISK")
	}

	// 6. AI assistance must have a durable human review, not just a string.
	if in.AIAssisted {
		if !in.HumanVerified {
			return reject(domain.VerdictRejected, "AI_NOT_HUMAN_VERIFIED")
		}
		review, reason, err := resolveHumanReview(ctx, in)
		if err != nil {
			return domain.Verification{}, fmt.Errorf("verify: load human review: %w", err)
		}
		if reason != "" {
			return reject(domain.VerdictNeedsEvidence, reason)
		}
		if review.FindingID != in.Finding.ID {
			return reject(domain.VerdictRejected, "AI_HUMAN_REVIEW_FINDING_MISMATCH")
		}
		if review.Verdict != domain.HumanReviewApproved {
			return reject(domain.VerdictRejected, "AI_HUMAN_REVIEW_REJECTED")
		}
		if review.ReviewedAt.After(now.Add(time.Minute)) {
			return reject(domain.VerdictRejected, "AI_HUMAN_REVIEW_FROM_FUTURE")
		}
		for _, id := range review.Evidence {
			ok, err := checker.HasEvidence(ctx, id)
			if err != nil {
				return domain.Verification{}, fmt.Errorf("verify: check human review evidence %s: %w", id, err)
			}
			if !ok {
				return reject(domain.VerdictNeedsEvidence, "HUMAN_REVIEW_EVIDENCE_NOT_FOUND:"+string(id))
			}
		}
		v.Evidence = unionEvidence(v.Evidence, review.Evidence)
		v.HumanReviewID = review.ID
		v.HumanVerificationRef = firstNonEmpty(in.HumanVerificationRef, review.NotesHash)
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

	// 8. Evidence of execution: the finding must be backed by a COMPLETED
	// receipt for the same program/asset/action and overlapping evidence. An
	// ALLOW decision is authorization, not proof that the tool ran.
	receipts, reason, err := resolveReceipts(ctx, in)
	if err != nil {
		return domain.Verification{}, fmt.Errorf("verify: load execution receipts: %w", err)
	}
	if reason != "" {
		return reject(domain.VerdictNeedsEvidence, reason)
	}
	matching := matchingReceipts(receipts, in)
	if len(matching) == 0 {
		return reject(domain.VerdictNeedsEvidence, "EXECUTION_RECEIPT_NOT_PROVEN")
	}
	for _, rec := range matching {
		v.ReceiptIDs = append(v.ReceiptIDs, domain.ReceiptID(rec.ID))
	}
	if len(in.Finding.SourceDecisionIDs) > 0 && !containsDecision(in.Finding.SourceDecisionIDs, in.Decision.ID) {
		return reject(domain.VerdictRejected, "AUTHORIZATION_NOT_LINKED_TO_FINDING")
	}

	v.Verdict = domain.VerdictVerified
	v.Reasons = append(v.Reasons, "ALL_CHECKS_PASSED")
	if err := v.Validate(); err != nil {
		return domain.Verification{}, fmt.Errorf("verify: produced invalid verification: %w", err)
	}
	return v, nil
}

func resolveHumanReview(ctx context.Context, in Input) (*domain.HumanReview, string, error) {
	if in.HumanReview != nil {
		if err := in.HumanReview.Validate(); err != nil {
			return nil, "AI_HUMAN_REVIEW_INVALID", nil
		}
		return in.HumanReview, "", nil
	}
	id := strings.TrimSpace(in.HumanReviewID)
	if id == "" {
		return nil, "AI_HUMAN_REVIEW_MISSING", nil
	}
	if in.HumanReviewLookup == nil {
		return nil, "AI_HUMAN_REVIEW_UNRESOLVED", nil
	}
	review, err := in.HumanReviewLookup.GetHumanReview(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, "AI_HUMAN_REVIEW_NOT_FOUND", nil
		}
		return nil, "", err
	}
	if err := review.Validate(); err != nil {
		return nil, "AI_HUMAN_REVIEW_INVALID", nil
	}
	return &review, "", nil
}

func resolveReceipts(ctx context.Context, in Input) ([]domain.ExecutionReceipt, string, error) {
	out := append([]domain.ExecutionReceipt{}, in.Receipts...)
	if len(in.Finding.ExecutionReceipts) > 0 {
		if in.ReceiptChecker == nil {
			if len(out) == 0 {
				return nil, "EXECUTION_RECEIPT_CHECKER_MISSING", nil
			}
		} else {
			for _, rid := range in.Finding.ExecutionReceipts {
				rec, err := in.ReceiptChecker.GetReceiptByID(ctx, string(rid))
				if err != nil {
					if errors.Is(err, domain.ErrReceiptNotFound) {
						continue
					}
					return nil, "", err
				}
				out = append(out, rec)
			}
		}
	}
	if len(out) == 0 {
		return nil, "EXECUTION_RECEIPT_MISSING", nil
	}
	return out, "", nil
}

// matchingReceipts selects the receipts that actually prove this finding: same
// program, same asset, a scoped action, the decision that authorized it, and at
// least one evidence ID shared with the finding/reproduction/impact evidence.
func matchingReceipts(receipts []domain.ExecutionReceipt, in Input) []domain.ExecutionReceipt {
	evidence := unionEvidence(in.Finding.Evidence, in.ReproductionEvidence, in.ImpactEvidence)
	var out []domain.ExecutionReceipt
	for _, rec := range receipts {
		if rec.Status != domain.ReceiptCompleted {
			continue
		}
		if rec.DecisionID == "" || in.Decision == nil || rec.DecisionID != in.Decision.ID {
			continue
		}
		if rec.ProgramID != in.Finding.ProgramID {
			continue
		}
		if !rec.Action.NeedsScope() {
			continue
		}
		if in.Finding.AssetID != nil {
			if rec.AssetID == nil || *rec.AssetID != *in.Finding.AssetID {
				continue
			}
		}
		if !intersectsEvidence(rec.Evidence, evidence) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func intersectsEvidence(a, b []domain.EvidenceID) bool {
	seen := make(map[domain.EvidenceID]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; ok {
			return true
		}
	}
	return false
}

func containsDecision(ids []domain.DecisionID, id domain.DecisionID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
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
