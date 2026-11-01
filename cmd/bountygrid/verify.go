package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
	"github.com/Zn070515/BountyGrid/internal/verify"
)

type verifyOptions struct {
	File    string
	Persist bool
}

// verifyFile is the JSON input for `bountygrid verify`. Program and Decision
// are loaded from PostgreSQL by ID so the verifier always evaluates the stored
// authoritative objects, not a caller-supplied copy.
type verifyFile struct {
	Finding              verifyFinding       `json:"finding"`
	ProgramID            domain.ProgramID    `json:"program_id"`
	DecisionID           domain.DecisionID   `json:"decision_id,omitempty"`
	Reproduced           bool                `json:"reproduced"`
	ReproductionEvidence []domain.EvidenceID `json:"reproduction_evidence"`
	ImpactVerified       bool                `json:"impact_verified"`
	ImpactEvidence       []domain.EvidenceID `json:"impact_evidence"`
	DataMinimized        *bool               `json:"data_minimized"`
	DuplicateChecked     bool                `json:"duplicate_checked"`
	DuplicateRisk        float64             `json:"duplicate_risk"`
	PubliclyKnown        bool                `json:"publicly_known"`
	AIAssisted           bool                `json:"ai_assisted"`
	HumanVerified        bool                `json:"human_verified"`
	HumanVerificationRef string              `json:"human_verification_ref"`
	HumanReviewID        string              `json:"human_review_id"`
	// HumanReview may be supplied inline. When -persist is set it is written to
	// the durable human_reviews store before the Verification so the reference
	// resolves.
	HumanReview       *domain.HumanReview `json:"human_review,omitempty"`
	ExecutionReceipts []string            `json:"execution_receipts"`
	VerifiedBy        string              `json:"verified_by"`
}

// verifyFinding is the JSON shape for a Finding. domain.Finding has no JSON
// tags, so embedding it directly would silently decode snake_case keys to zero
// values.
type verifyFinding struct {
	ID        domain.FindingID    `json:"id"`
	TaskID    domain.TaskID       `json:"task_id"`
	ProgramID domain.ProgramID    `json:"program_id"`
	AssetID   *domain.AssetID     `json:"asset_id,omitempty"`
	Title     string              `json:"title"`
	VulnType  string              `json:"vuln_type"`
	Severity  string              `json:"severity"`
	Evidence  []domain.EvidenceID `json:"evidence"`
	Status    string              `json:"status"`
	CreatedAt time.Time           `json:"created_at"`
	// Provenance: which decisions authorized the work and which receipts prove
	// it executed. The verifier resolves each receipt from the store.
	SourceDecisionIDs []domain.DecisionID `json:"source_decision_ids,omitempty"`
	ExecutionReceipts []domain.ReceiptID  `json:"execution_receipts,omitempty"`
}

func (f verifyFinding) toDomain() domain.Finding {
	return domain.Finding{
		ID: f.ID, TaskID: f.TaskID, ProgramID: f.ProgramID, AssetID: f.AssetID,
		Title: f.Title, VulnType: f.VulnType, Severity: f.Severity,
		Evidence: f.Evidence, Status: f.Status, CreatedAt: f.CreatedAt,
		SourceDecisionIDs: f.SourceDecisionIDs, ExecutionReceipts: f.ExecutionReceipts,
	}
}

type verifyStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error)
	GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error)
	GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error)
	PutVerification(ctx context.Context, v domain.Verification) error
	GetReceiptByID(ctx context.Context, id string) (domain.ExecutionReceipt, error)
	GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error)
	PutHumanReview(ctx context.Context, h domain.HumanReview) error
}

type repoEvidenceChecker struct {
	store verifyStore
}

func (c repoEvidenceChecker) HasEvidence(ctx context.Context, id domain.EvidenceID) (bool, error) {
	_, err := c.store.GetEvidence(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type repoReceiptChecker struct {
	store verifyStore
}

func (c repoReceiptChecker) GetReceiptByID(ctx context.Context, id string) (domain.ExecutionReceipt, error) {
	return c.store.GetReceiptByID(ctx, id)
}

type repoHumanReviewLookup struct {
	store verifyStore
}

func (c repoHumanReviewLookup) GetHumanReview(ctx context.Context, id string) (domain.HumanReview, error) {
	return c.store.GetHumanReview(ctx, id)
}

type verifyOutput struct {
	domain.Verification
	Persisted bool `json:"persisted"`
}

func parseVerifyOptions(args []string) (verifyOptions, error) {
	var o verifyOptions
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.File, "file", "", "JSON file with finding, program_id, decision_id and verification evidence")
	fs.BoolVar(&o.Persist, "persist", false, "write the Verification to PostgreSQL")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.File == "" {
		return o, errors.New("verify: -file is required")
	}
	return o, nil
}

func runVerify(ctx context.Context, args []string) error {
	opts, err := parseVerifyOptions(args)
	if err != nil {
		return err
	}
	in, err := loadVerifyFile(opts.File)
	if err != nil {
		return err
	}
	d, err := dsn()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, d)
	if err != nil {
		return err
	}
	defer pool.Close()
	out, err := executeVerify(ctx, postgres.NewRepo(pool), in, opts.Persist, time.Now().UTC())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func loadVerifyFile(path string) (verifyFile, error) {
	var in verifyFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return in, fmt.Errorf("verify: read file: %w", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("verify: decode file: %w", err)
	}
	return in, nil
}

func appendUniqueReceipt(ids []domain.ReceiptID, id domain.ReceiptID) []domain.ReceiptID {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

func executeVerify(ctx context.Context, store verifyStore, in verifyFile, persist bool, now time.Time) (verifyOutput, error) {
	finding := in.Finding.toDomain()
	programID := in.ProgramID
	if programID == "" {
		programID = finding.ProgramID
	}
	if programID == "" {
		return verifyOutput{}, errors.New("verify: program_id is required")
	}
	program, err := store.GetProgram(ctx, programID)
	if err != nil {
		return verifyOutput{}, fmt.Errorf("verify: load program %s: %w", programID, err)
	}
	// Persisting a verification requires the subject Finding to exist. Use the
	// stored finding as authoritative rather than the file body.
	if persist {
		stored, err := store.GetFinding(ctx, finding.ID)
		if err != nil {
			return verifyOutput{}, fmt.Errorf("verify: load finding %s: %w", finding.ID, err)
		}
		finding = stored
	}
	// Receipt IDs named in the file are resolution hints only: the verifier
	// loads each stored receipt and re-checks program/asset/action/decision/
	// evidence, so naming a receipt cannot fabricate execution.
	for _, id := range in.ExecutionReceipts {
		if id = strings.TrimSpace(id); id != "" {
			finding.ExecutionReceipts = appendUniqueReceipt(finding.ExecutionReceipts, domain.ReceiptID(id))
		}
	}
	humanReviewID := strings.TrimSpace(in.HumanReviewID)
	if in.HumanReview != nil {
		if strings.TrimSpace(in.HumanReview.ID) == "" {
			return verifyOutput{}, errors.New("verify: human_review.id is required")
		}
		if humanReviewID == "" {
			humanReviewID = in.HumanReview.ID
		}
		if persist {
			if err := store.PutHumanReview(ctx, *in.HumanReview); err != nil {
				return verifyOutput{}, fmt.Errorf("verify: persist human review: %w", err)
			}
		}
	}
	var decision *domain.ActionDecision
	if in.DecisionID != "" {
		d, err := store.GetDecision(ctx, in.DecisionID)
		if err != nil {
			return verifyOutput{}, fmt.Errorf("verify: load decision %s: %w", in.DecisionID, err)
		}
		decision = &d
	}
	v, err := verify.NewRuleVerifier(verify.Options{
		Now: func() time.Time { return now }, Evidence: repoEvidenceChecker{store: store},
	}).Verify(ctx, verify.Input{
		Finding: finding, Program: program, Decision: decision,
		Evidence:          repoEvidenceChecker{store: store},
		ReceiptChecker:    repoReceiptChecker{store: store},
		HumanReview:       in.HumanReview,
		HumanReviewID:     humanReviewID,
		HumanReviewLookup: repoHumanReviewLookup{store: store},
		Reproduced:        in.Reproduced, ReproductionEvidence: in.ReproductionEvidence,
		ImpactVerified: in.ImpactVerified, ImpactEvidence: in.ImpactEvidence,
		DataMinimized:    in.DataMinimized,
		DuplicateChecked: in.DuplicateChecked, DuplicateRisk: in.DuplicateRisk,
		PubliclyKnown: in.PubliclyKnown,
		AIAssisted:    in.AIAssisted, HumanVerified: in.HumanVerified,
		HumanVerificationRef: in.HumanVerificationRef,
		VerifiedBy:           in.VerifiedBy,
		Now:                  now,
	})
	if err != nil {
		return verifyOutput{}, err
	}
	if persist {
		if err := store.PutVerification(ctx, v); err != nil {
			return verifyOutput{}, fmt.Errorf("verify: persist: %w", err)
		}
	}
	return verifyOutput{Verification: v, Persisted: persist}, nil
}
