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
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/policy"
	"github.com/Zn070515/BountyGrid/internal/report"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
	"github.com/Zn070515/BountyGrid/internal/submit"
)

type submitOptions struct {
	ReportID        string
	AccountID       string
	Approval        string
	ApprovalID      string
	PrintBinding    bool
	AttachmentsRoot string
	RulesFile       string
}

// approvalFile is the JSON shape for a human approval. domain.HumanApproval has
// no JSON tags, so a local DTO is used to keep snake_case input unambiguous.
type approvalFile struct {
	ID             string           `json:"id"`
	ApprovedBy     string           `json:"approved_by"`
	ApprovedAt     time.Time        `json:"approved_at"`
	RequestID      string           `json:"request_id"`
	Action         domain.Action    `json:"action"`
	ProgramID      domain.ProgramID `json:"program_id"`
	AssetID        *domain.AssetID  `json:"asset_id,omitempty"`
	ResourceID     string           `json:"resource_id"`
	ParametersHash string           `json:"parameters_hash"`
	Scope          string           `json:"scope,omitempty"`
	ExpiresAt      time.Time        `json:"expires_at"`
	SingleUse      bool             `json:"single_use"`
	Comment        string           `json:"comment,omitempty"`
}

func (a approvalFile) toDomain() *domain.HumanApproval {
	return &domain.HumanApproval{
		ID: a.ID, ApprovedBy: a.ApprovedBy, ApprovedAt: a.ApprovedAt,
		RequestID: a.RequestID, Action: a.Action, ProgramID: a.ProgramID,
		AssetID: a.AssetID, ResourceID: a.ResourceID,
		ParametersHash: a.ParametersHash, Scope: a.Scope,
		ExpiresAt: a.ExpiresAt, SingleUse: a.SingleUse, Comment: a.Comment,
	}
}

type submitBinding struct {
	RequestID      string            `json:"request_id"`
	Parameters     map[string]string `json:"parameters"`
	ParametersHash string            `json:"parameters_hash"`
}

func parseSubmitOptions(args []string) (submitOptions, error) {
	var o submitOptions
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.ReportID, "report", "", "report draft id")
	fs.StringVar(&o.AccountID, "account", "", "account id (e.g. vulbox/account)")
	fs.StringVar(&o.Approval, "approval", "", "human approval JSON file")
	fs.StringVar(&o.ApprovalID, "approval-id", "", "approval id for -print-binding")
	fs.BoolVar(&o.PrintBinding, "print-binding", false, "print the request_id/parameters_hash an approval must bind")
	fs.StringVar(&o.AttachmentsRoot, "attachments-root", "", "root for re-verifying approved attachment hashes")
	fs.StringVar(&o.RulesFile, "rules", "", "optional JSON rule-set overlay")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.PrintBinding {
		if o.ReportID == "" || o.ApprovalID == "" {
			return o, errors.New("submit: -print-binding requires -report and -approval-id")
		}
		return o, nil
	}
	if o.ReportID == "" || o.AccountID == "" || o.Approval == "" {
		return o, errors.New("submit: -report, -account and -approval are required")
	}
	return o, nil
}

func runSubmit(ctx context.Context, args []string) error {
	opts, err := parseSubmitOptions(args)
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
	repo := postgres.NewRepo(pool)
	if opts.PrintBinding {
		draft, err := repo.GetReportDraft(ctx, opts.ReportID)
		if err != nil {
			return fmt.Errorf("submit: load report %s: %w", opts.ReportID, err)
		}
		hash, err := report.ContentHash(draft)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(buildBinding(opts.ReportID, opts.ApprovalID, hash))
	}
	appr, err := loadApproval(opts.Approval)
	if err != nil {
		return err
	}
	rs, err := loadOptionalRules(opts.RulesFile)
	if err != nil {
		return err
	}
	gate := gateway.New(repo, policy.NewEngineWithRules(rs), postgres.NewAuditStore(pool), postgres.NewPostgresApprovalLedger(pool))
	svc, err := submit.New(submit.Options{
		Store: repo, Gate: gate, Submitter: submit.DryRunSubmitter{},
		Auditor:         postgres.NewAuditStore(pool),
		AttachmentsRoot: opts.AttachmentsRoot,
	})
	if err != nil {
		return err
	}
	sub, err := svc.Submit(ctx, submit.Input{
		ReportID: opts.ReportID, AccountID: domain.AccountID(opts.AccountID),
		Approval: appr, Now: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(sub)
}

func buildBinding(reportID, approvalID, contentHash string) submitBinding {
	params := map[string]string{"report_id": reportID, "report_content_hash": contentHash}
	raw, _ := json.Marshal(params)
	return submitBinding{
		RequestID:      fmt.Sprintf("submit/%s/%s", reportID, approvalID),
		Parameters:     params,
		ParametersHash: domain.CanonicalParametersHash(raw),
	}
}

func loadApproval(path string) (*domain.HumanApproval, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("submit: read approval: %w", err)
	}
	var a approvalFile
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("submit: decode approval: %w", err)
	}
	return a.toDomain(), nil
}
