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
	"github.com/Zn070515/BountyGrid/internal/report"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type reportOptions struct {
	File    string
	Persist bool
}

type reportFile struct {
	FindingID      domain.FindingID `json:"finding_id"`
	ProgramID      domain.ProgramID `json:"program_id"`
	VerificationID string           `json:"verification_id"`
	Fields         report.Fields    `json:"fields"`
	AI             report.AIUse     `json:"ai"`
	Attachments    []string         `json:"attachments,omitempty"`
}

type reportStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error)
	GetVerification(ctx context.Context, id string) (domain.Verification, error)
	PutReportDraft(ctx context.Context, d domain.ReportDraft) error
}

type reportOutput struct {
	Draft     domain.ReportDraft `json:"draft"`
	Missing   []string           `json:"missing,omitempty"`
	Ready     bool               `json:"ready"`
	Persisted bool               `json:"persisted"`
}

func parseReportOptions(args []string) (reportOptions, error) {
	var o reportOptions
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.File, "file", "", "JSON file with finding_id/program_id/verification_id/fields/ai")
	fs.BoolVar(&o.Persist, "persist", false, "write the ReportDraft to PostgreSQL")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.File == "" {
		return o, errors.New("report: -file is required")
	}
	return o, nil
}

func runReport(ctx context.Context, args []string) error {
	opts, err := parseReportOptions(args)
	if err != nil {
		return err
	}
	in, err := loadReportFile(opts.File)
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
	out, err := executeReport(ctx, postgres.NewRepo(pool), in, opts.Persist, time.Now().UTC())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func loadReportFile(path string) (reportFile, error) {
	var in reportFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return in, fmt.Errorf("report: read file: %w", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("report: decode file: %w", err)
	}
	return in, nil
}

func executeReport(ctx context.Context, store reportStore, in reportFile, persist bool, now time.Time) (reportOutput, error) {
	if in.FindingID == "" || in.ProgramID == "" || in.VerificationID == "" {
		return reportOutput{}, errors.New("report: finding_id/program_id/verification_id are required")
	}
	program, err := store.GetProgram(ctx, in.ProgramID)
	if err != nil {
		return reportOutput{}, fmt.Errorf("report: load program %s: %w", in.ProgramID, err)
	}
	finding, err := store.GetFinding(ctx, in.FindingID)
	if err != nil {
		return reportOutput{}, fmt.Errorf("report: load finding %s: %w", in.FindingID, err)
	}
	verification, err := store.GetVerification(ctx, in.VerificationID)
	if err != nil {
		return reportOutput{}, fmt.Errorf("report: load verification %s: %w", in.VerificationID, err)
	}
	res, err := report.Build(report.Input{
		Finding: finding, Program: program, Verification: verification,
		Fields: in.Fields, AI: in.AI, Attachments: in.Attachments, Now: now,
	})
	if err != nil {
		return reportOutput{}, err
	}
	if persist {
		if err := store.PutReportDraft(ctx, res.Draft); err != nil {
			return reportOutput{}, fmt.Errorf("report: persist: %w", err)
		}
	}
	return reportOutput{Draft: res.Draft, Missing: res.Missing, Ready: res.Ready, Persisted: persist}, nil
}
