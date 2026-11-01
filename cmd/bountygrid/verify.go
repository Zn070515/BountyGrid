package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	Finding              domain.Finding      `json:"finding"`
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
	VerifiedBy           string              `json:"verified_by"`
	Now                  *time.Time          `json:"now,omitempty"`
}

type verifyStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error)
	PutVerification(ctx context.Context, v domain.Verification) error
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

func executeVerify(ctx context.Context, store verifyStore, in verifyFile, persist bool, now time.Time) (verifyOutput, error) {
	programID := in.ProgramID
	if programID == "" {
		programID = in.Finding.ProgramID
	}
	if programID == "" {
		return verifyOutput{}, errors.New("verify: program_id is required")
	}
	program, err := store.GetProgram(ctx, programID)
	if err != nil {
		return verifyOutput{}, fmt.Errorf("verify: load program %s: %w", programID, err)
	}
	var decision *domain.ActionDecision
	if in.DecisionID != "" {
		d, err := store.GetDecision(ctx, in.DecisionID)
		if err != nil {
			return verifyOutput{}, fmt.Errorf("verify: load decision %s: %w", in.DecisionID, err)
		}
		decision = &d
	}
	if in.Now != nil {
		now = in.Now.UTC()
	}
	v, err := verify.NewRuleVerifier(verify.Options{Now: func() time.Time { return now }}).Verify(ctx, verify.Input{
		Finding: in.Finding, Program: program, Decision: decision,
		Reproduced: in.Reproduced, ReproductionEvidence: in.ReproductionEvidence,
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
