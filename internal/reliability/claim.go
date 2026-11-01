package reliability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// ProofResolver resolves proof IDs against the authoritative store. A
// Reliability layer must never trust a caller-supplied "COMPLETED"/"VERIFIED"
// string; the resolver owns the association checks (finding/asset/program).
type ProofResolver interface {
	ResolveReceipt(ctx context.Context, id string) (ReceiptProof, error)
	ResolveVerification(ctx context.Context, id string) (VerificationProof, error)
}

// ErrProofResolverRequired means a VERIFIED_FACT promotion was attempted
// without an authoritative proof resolver.
var ErrProofResolverRequired = fmt.Errorf("%w: VERIFIED_FACT requires an authoritative ProofResolver", ErrVerificationRequired)

// PromotionRequest supplies the additional evidence a level requires. The
// promoted record always carries the union of the existing and supplied
// evidence; a caller cannot drop evidence while promoting.
type PromotionRequest struct {
	// To must be exactly the next level in the ladder.
	To ClaimLevel
	// Evidence is attached to the promoted claim.
	Evidence []domain.EvidenceID
	// Receipts and Verification are required for VERIFIED_FACT. Their IDs are
	// resolved through Resolver; the supplied summaries are only hints.
	Receipts     []ReceiptProof
	Verification *VerificationProof
	// Resolver is mandatory for VERIFIED_FACT. It resolves the receipt and
	// verification IDs against the authoritative store.
	Resolver ProofResolver
	// Ctx bounds resolver calls; nil uses context.Background().
	Ctx context.Context
	// Now is the promotion timestamp. A zero value keeps the record's
	// UpdatedAt, and a value earlier than UpdatedAt is clamped forward so
	// timestamps never move backwards.
	Now time.Time
}

// Promote is the functional form of FactRecord.Promote.
func Promote(record FactRecord, req PromotionRequest) (FactRecord, error) {
	return record.Promote(req)
}

// Promote advances a fact by exactly one claim level.
//
// The function enforces:
//
//   - the target must be the immediate next level (no skipping, no demotion);
//   - OBSERVATION -> HYPOTHESIS -> CLAIM -> VERIFIED_FACT is the only path;
//   - a CLAIM must have evidence, so an unsupported HYPOTHESIS cannot become a
//     CLAIM without evidence supplied here or already attached;
//   - VERIFIED_FACT requires at least one COMPLETED execution receipt and a
//     VERIFIED verification proof with evidence.
//
// A model cannot use Promote to produce VERIFIED_FACT from model output alone:
// the receipt and verification are external, durable artifacts supplied by the
// caller, and their presence is checked here.
func (f FactRecord) Promote(req PromotionRequest) (FactRecord, error) {
	if err := f.Validate(); err != nil {
		return FactRecord{}, err
	}
	if !req.To.Valid() {
		return FactRecord{}, fmt.Errorf("%w: unknown target level %q", ErrInvalidPromotion, req.To)
	}
	next, ok := f.Claim.Level.Next()
	if !ok {
		return FactRecord{}, fmt.Errorf("%w: %s is terminal", ErrInvalidPromotion, f.Claim.Level)
	}
	if req.To != next {
		if req.To.Rank() > next.Rank() {
			return FactRecord{}, fmt.Errorf("%w: cannot promote %s directly to %s; next level is %s",
				ErrLevelSkipped, f.Claim.Level, req.To, next)
		}
		return FactRecord{}, fmt.Errorf("%w: cannot move %s to %s; next level is %s",
			ErrInvalidPromotion, f.Claim.Level, req.To, next)
	}

	out := cloneFactRecord(f)
	out.Claim.Evidence = mergeEvidence(out.Claim.Evidence, req.Evidence)
	out.Receipts = mergeReceipts(out.Receipts, req.Receipts)
	if req.Verification != nil {
		v := cloneVerification(*req.Verification)
		out.Verification = &v
	}
	out.Claim.Level = req.To

	if req.To == LevelVerifiedFact {
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if req.Resolver == nil {
			return FactRecord{}, ErrProofResolverRequired
		}
		resolvedReceipts := make([]ReceiptProof, 0, len(out.Receipts))
		for _, hint := range out.Receipts {
			rec, err := req.Resolver.ResolveReceipt(ctx, hint.ID)
			if err != nil {
				return FactRecord{}, fmt.Errorf("%w: resolve receipt %s: %w", ErrReceiptRequired, hint.ID, err)
			}
			if rec.ID != hint.ID {
				return FactRecord{}, fmt.Errorf("%w: resolver returned receipt %s for %s", ErrReceiptRequired, rec.ID, hint.ID)
			}
			resolvedReceipts = append(resolvedReceipts, rec)
		}
		out.Receipts = resolvedReceipts
		if len(out.Receipts) == 0 {
			return FactRecord{}, fmt.Errorf("%w: VERIFIED_FACT requires a receipt", ErrReceiptRequired)
		}
		completedReceipt := false
		for _, r := range out.Receipts {
			if r.Completed() {
				completedReceipt = true
				break
			}
		}
		if !completedReceipt {
			return FactRecord{}, fmt.Errorf("%w: VERIFIED_FACT requires a COMPLETED receipt", ErrReceiptRequired)
		}
		verificationHint := req.Verification
		if verificationHint == nil {
			verificationHint = out.Verification
		}
		if verificationHint == nil || strings.TrimSpace(verificationHint.ID) == "" {
			return FactRecord{}, fmt.Errorf("%w: VERIFIED_FACT requires a verification id", ErrVerificationRequired)
		}
		verification, err := req.Resolver.ResolveVerification(ctx, verificationHint.ID)
		if err != nil {
			return FactRecord{}, fmt.Errorf("%w: resolve verification %s: %w", ErrVerificationRequired, verificationHint.ID, err)
		}
		out.Verification = &verification
	}

	now := utc(req.Now)
	if now.IsZero() {
		now = out.UpdatedAt
	}
	if now.Before(out.UpdatedAt) {
		now = out.UpdatedAt
	}
	out.UpdatedAt = now

	switch req.To {
	case LevelObservation:
		if !out.Claim.HasEvidence() {
			return FactRecord{}, fmt.Errorf("%w: OBSERVATION requires evidence", ErrEvidenceRequired)
		}
	case LevelHypothesis:
		// A hypothesis may be unsupported; evidence is optional.
	case LevelClaim:
		if !out.Claim.HasEvidence() {
			return FactRecord{}, fmt.Errorf("%w: a HYPOTHESIS without evidence cannot become a CLAIM", ErrEvidenceRequired)
		}
	case LevelVerifiedFact:
		if err := requireVerifiedFact(out); err != nil {
			return FactRecord{}, err
		}
	}
	if err := out.Validate(); err != nil {
		return FactRecord{}, err
	}
	return out, nil
}

// requireVerifiedFact is the hard gate for the top of the ladder. It is used
// both by promotion and by FactRecord.Validate so a record cannot be assembled
// directly at VERIFIED_FACT while bypassing Promote.
func requireVerifiedFact(f FactRecord) error {
	if !f.Claim.HasEvidence() {
		return fmt.Errorf("%w: VERIFIED_FACT requires claim evidence", ErrEvidenceRequired)
	}
	if strings.TrimSpace(string(f.ProgramID)) == "" || strings.TrimSpace(string(f.FindingID)) == "" {
		return fmt.Errorf("%w: VERIFIED_FACT requires program_id and finding_id", ErrVerificationRequired)
	}
	completed := 0
	for _, r := range f.Receipts {
		if strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("%w: VERIFIED_FACT has a receipt without an id", ErrReceiptRequired)
		}
		if !r.Action.Valid() {
			return fmt.Errorf("%w: receipt %s has invalid action %q", ErrReceiptRequired, r.ID, r.Action)
		}
		if r.ProgramID != f.ProgramID || r.FindingID != f.FindingID || !sameAssetID(r.AssetID, f.AssetID) {
			return fmt.Errorf("%w: receipt %s is not bound to this program/finding/asset", ErrReceiptRequired, r.ID)
		}
		if r.Completed() {
			completed++
		}
	}
	if completed == 0 {
		return fmt.Errorf("%w: VERIFIED_FACT requires a COMPLETED receipt", ErrReceiptRequired)
	}
	if f.Verification == nil {
		return fmt.Errorf("%w: VERIFIED_FACT requires a verification", ErrVerificationRequired)
	}
	if err := f.Verification.Validate(); err != nil {
		return err
	}
	if f.Verification.Verdict != domain.VerdictVerified {
		return fmt.Errorf("%w: verification verdict is %s", ErrVerificationRequired, f.Verification.Verdict)
	}
	if f.Verification.ProgramID != f.ProgramID || f.Verification.FindingID != f.FindingID ||
		!sameAssetID(f.Verification.AssetID, f.AssetID) {
		return fmt.Errorf("%w: verification %s is not bound to this program/finding/asset", ErrVerificationRequired, f.Verification.ID)
	}
	if len(dedupeEvidence(f.Verification.Evidence)) == 0 {
		return fmt.Errorf("%w: verification has no evidence", ErrVerificationRequired)
	}
	if !intersectsEvidence(f.Claim.Evidence, f.Verification.Evidence) {
		return fmt.Errorf("%w: verification evidence does not cover the claim", ErrVerificationRequired)
	}
	return nil
}

func sameAssetID(a, b *domain.AssetID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
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

func cloneFactRecord(f FactRecord) FactRecord {
	out := f
	out.Claim.Evidence = append([]domain.EvidenceID(nil), f.Claim.Evidence...)
	out.SourceObservationIDs = append([]string(nil), f.SourceObservationIDs...)
	out.Receipts = make([]ReceiptProof, len(f.Receipts))
	for i, r := range f.Receipts {
		out.Receipts[i] = cloneReceipt(r)
	}
	if f.Verification != nil {
		v := cloneVerification(*f.Verification)
		out.Verification = &v
	}
	return out
}

func cloneVerification(v VerificationProof) VerificationProof {
	v.Evidence = append([]domain.EvidenceID(nil), v.Evidence...)
	return v
}

func mergeReceipts(a, b []ReceiptProof) []ReceiptProof {
	out := make([]ReceiptProof, 0, len(a)+len(b))
	index := make(map[string]int, len(a)+len(b))
	for _, r := range a {
		index[r.ID] = len(out)
		out = append(out, cloneReceipt(r))
	}
	for _, r := range b {
		if i, ok := index[r.ID]; ok {
			out[i] = cloneReceipt(r)
			continue
		}
		index[r.ID] = len(out)
		out = append(out, cloneReceipt(r))
	}
	return out
}

func cloneReceipt(r ReceiptProof) ReceiptProof {
	r.Evidence = append([]domain.EvidenceID(nil), r.Evidence...)
	return r
}
