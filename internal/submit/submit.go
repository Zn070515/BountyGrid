// Package submit is the human-approved report submission service. It reloads
// the stored report/finding/program/verification, re-runs the Action Gateway,
// consumes the single-use approval, and only then calls a Submitter. This
// repository contains only a dry-run Submitter; it never touches a platform.
package submit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/report"
)

var (
	ErrInvalidInput     = errors.New("submit: invalid input")
	ErrNotReady         = errors.New("submit: report is not ready")
	ErrAlreadySubmitted = errors.New("submit: report already submitted")
	ErrPlatformSubmit   = errors.New("submit: platform submit failed")
)

type Store interface {
	GetReportDraft(ctx context.Context, id string) (domain.ReportDraft, error)
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
	GetFinding(ctx context.Context, id domain.FindingID) (domain.Finding, error)
	GetVerification(ctx context.Context, id string) (domain.Verification, error)
	GetSubmissionByReport(ctx context.Context, reportID string) (domain.Submission, bool, error)
	RecordSubmission(ctx context.Context, s domain.Submission, d domain.ReportDraft) error
}

type Gate interface {
	Evaluate(ctx context.Context, in gateway.EvaluateInput) (domain.ActionDecision, error)
	AuthorizeWith(ctx context.Context, id domain.DecisionID, ai gateway.AuthorizeInput) error
}

type Auditor interface {
	Append(ctx context.Context, e audit.Event) (int64, error)
}

type Result struct {
	PlatformURL string
	Receipt     string
	Status      string
	DryRun      bool
}

type Request struct {
	Report   domain.ReportDraft
	Program  domain.Program
	Decision domain.ActionDecision
	Approval *domain.HumanApproval
}

type Submitter interface {
	Submit(ctx context.Context, req Request) (Result, error)
}

// DryRunSubmitter records a deterministic local receipt without any network or
// platform action. It is the only submitter in this repository.
type DryRunSubmitter struct{}

func (DryRunSubmitter) Submit(ctx context.Context, req Request) (Result, error) {
	return Result{
		Receipt: fmt.Sprintf("dry-run/%s/%s", req.Report.ID, req.Decision.ID),
		Status:  "DRY_RUN", DryRun: true,
	}, nil
}

type Input struct {
	ReportID  string
	AccountID domain.AccountID
	Approval  *domain.HumanApproval
	Now       time.Time
}

type Options struct {
	Store     Store
	Gate      Gate
	Submitter Submitter
	Auditor   Auditor
	Now       func() time.Time
}

type Service struct {
	store     Store
	gate      Gate
	submitter Submitter
	auditor   Auditor
	now       func() time.Time
}

func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Gate == nil || opts.Submitter == nil {
		return nil, fmt.Errorf("%w: store/gate/submitter required", ErrInvalidInput)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: opts.Store, gate: opts.Gate, submitter: opts.Submitter, auditor: opts.Auditor, now: opts.Now}, nil
}

type DecisionError struct {
	Decision domain.ActionDecision
}

func (e DecisionError) Error() string {
	return fmt.Sprintf("submit: action %s is %s %v", e.Decision.Action, e.Decision.Decision, e.Decision.Reasons)
}

func (s *Service) Submit(ctx context.Context, in Input) (domain.Submission, error) {
	if in.ReportID == "" || in.AccountID == "" || in.Approval == nil || in.Approval.ID == "" {
		return domain.Submission{}, fmt.Errorf("%w: report_id/account_id/approval required", ErrInvalidInput)
	}
	now := in.Now
	if now.IsZero() {
		now = s.now().UTC()
	}

	draft, err := s.store.GetReportDraft(ctx, in.ReportID)
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: load report %s: %w", in.ReportID, err)
	}
	if draft.Status == domain.ReportStatusSubmitted {
		return domain.Submission{}, fmt.Errorf("%w: %s", ErrAlreadySubmitted, draft.ID)
	}
	if draft.Status != domain.ReportStatusDraft &&
		draft.Status != domain.ReportStatusReadyForReview &&
		draft.Status != domain.ReportStatusApproved {
		return domain.Submission{}, fmt.Errorf("%w: report status %s", ErrInvalidInput, draft.Status)
	}
	// The status row may be stale after a failed update; the submission table is
	// authoritative. Dry-run rows are excluded so they never block a real one.
	if existing, ok, err := s.store.GetSubmissionByReport(ctx, draft.ID); err != nil {
		return domain.Submission{}, fmt.Errorf("submit: check existing submission: %w", err)
	} else if ok {
		return domain.Submission{}, fmt.Errorf("%w: %s", ErrAlreadySubmitted, existing.ID)
	}

	program, err := s.store.GetProgram(ctx, draft.ProgramID)
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: load program %s: %w", draft.ProgramID, err)
	}
	finding, err := s.store.GetFinding(ctx, draft.FindingID)
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: load finding %s: %w", draft.FindingID, err)
	}
	verification, err := s.store.GetVerification(ctx, draft.VerificationID)
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: load verification for %s: %w", draft.ID, err)
	}
	if err := checkScope(program, finding); err != nil {
		return domain.Submission{}, err
	}
	res, err := report.Recheck(draft, finding, program, verification)
	if err != nil {
		return domain.Submission{}, err
	}
	if !res.Ready {
		return domain.Submission{}, fmt.Errorf("%w: missing %v", ErrNotReady, res.Missing)
	}

	requestID := fmt.Sprintf("submit/%s/%s", draft.ID, in.Approval.ID)
	contentHash, err := report.ContentHash(draft)
	if err != nil {
		return domain.Submission{}, err
	}
	params, err := json.Marshal(map[string]string{
		"report_id": draft.ID, "report_content_hash": contentHash,
	})
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: marshal parameters: %w", err)
	}
	if err := checkApproval(in.Approval, requestID, program, finding, draft, params, now); err != nil {
		return domain.Submission{}, err
	}

	req := domain.ActionRequest{
		ID: requestID, Actor: domain.ActorRef{Kind: domain.ActorHuman, ID: in.Approval.ApprovedBy},
		AccountID: in.AccountID, ProgramID: program.ID, AssetID: finding.AssetID,
		Action: domain.ActionReportSubmit, Parameters: params,
		Purpose: "submit human-approved report", RequestedAt: now,
		IdempotencyKey: requestID,
	}
	decision, err := s.gate.Evaluate(ctx, gateway.EvaluateInput{
		Request: req, ResourceID: draft.ID, HumanApproval: in.Approval, Now: now,
	})
	if err != nil {
		return domain.Submission{}, fmt.Errorf("submit: gateway evaluate: %w", err)
	}
	if decision.Decision != domain.DecisionAllow {
		return domain.Submission{}, DecisionError{Decision: decision}
	}
	if decision.Action != domain.ActionReportSubmit {
		return domain.Submission{}, fmt.Errorf("submit: gateway approved action %s, not REPORT_SUBMIT", decision.Action)
	}
	if err := s.gate.AuthorizeWith(ctx, decision.ID, gateway.AuthorizeInput{Now: now}); err != nil {
		return domain.Submission{}, fmt.Errorf("submit: gateway authorize: %w", err)
	}

	result, err := s.submitter.Submit(ctx, Request{
		Report: draft, Program: program, Decision: decision,
		Approval: in.Approval,
	})
	if err != nil {
		perr := fmt.Errorf("%w: %v", ErrPlatformSubmit, err)
		if aerr := s.appendAudit(ctx, draft, decision, in.Approval, "SUBMIT_ERROR", now); aerr != nil {
			return domain.Submission{}, errors.Join(perr, aerr)
		}
		return domain.Submission{}, perr
	}
	if result.Receipt == "" || result.Status == "" {
		perr := fmt.Errorf("%w: submitter returned no receipt/status", ErrPlatformSubmit)
		if aerr := s.appendAudit(ctx, draft, decision, in.Approval, "SUBMIT_INVALID_RECEIPT", now); aerr != nil {
			return domain.Submission{}, errors.Join(perr, aerr)
		}
		return domain.Submission{}, perr
	}

	sub := domain.Submission{
		ID:       domain.SubmissionID(fmt.Sprintf("sub/%s/%s/%s", draft.ID, in.Approval.ID, now.UTC().Format("20060102T150405.000000000"))),
		ReportID: draft.ID, ProgramID: program.ID, SubmittedAt: now,
		PlatformURL: result.PlatformURL, Receipt: result.Receipt, Status: result.Status,
		ApprovalID: in.Approval.ID, ContentHash: contentHash,
		Evidence: append([]domain.EvidenceID{}, verification.Evidence...),
	}
	// A dry run must not consume the report's real-submission slot or flip the
	// draft to SUBMITTED; it leaves a DRY_RUN receipt for audit only.
	if !result.DryRun {
		draft.Status = domain.ReportStatusSubmitted
	}
	if err := s.store.RecordSubmission(ctx, sub, draft); err != nil {
		perr := fmt.Errorf("submit: platform submit may have succeeded but recording failed; do not retry without reconciliation: %w", err)
		if aerr := s.appendAudit(ctx, draft, decision, in.Approval, "SUBMISSION_RECORD_FAILED", now); aerr != nil {
			return domain.Submission{}, errors.Join(perr, aerr)
		}
		return domain.Submission{}, perr
	}
	if err := s.appendAudit(ctx, draft, decision, in.Approval, "RECORDED", now); err != nil {
		return sub, err
	}
	return sub, nil
}

func checkApproval(a *domain.HumanApproval, requestID string, program domain.Program, finding domain.Finding, draft domain.ReportDraft, params []byte, now time.Time) error {
	if a.Action != domain.ActionReportSubmit {
		return fmt.Errorf("%w: approval action %s", ErrInvalidInput, a.Action)
	}
	if a.RequestID != requestID {
		return fmt.Errorf("%w: approval request_id=%q want=%q", ErrInvalidInput, a.RequestID, requestID)
	}
	if a.ProgramID != program.ID {
		return fmt.Errorf("%w: approval program mismatch", ErrInvalidInput)
	}
	if a.ResourceID != draft.ID {
		return fmt.Errorf("%w: approval resource_id=%q want=%q", ErrInvalidInput, a.ResourceID, draft.ID)
	}
	if !sameAsset(a.AssetID, finding.AssetID) {
		return fmt.Errorf("%w: approval asset mismatch", ErrInvalidInput)
	}
	if !a.SingleUse {
		return fmt.Errorf("%w: approval must be single-use", ErrInvalidInput)
	}
	if a.ExpiresAt.IsZero() || !a.ExpiresAt.After(now) {
		return fmt.Errorf("%w: approval expired", ErrInvalidInput)
	}
	if a.ParametersHash != domain.ParametersHash(params) &&
		a.ParametersHash != domain.CanonicalParametersHash(params) {
		return fmt.Errorf("%w: approval parameters_hash does not match report binding", ErrInvalidInput)
	}
	return nil
}

// checkScope re-checks the finding's asset against the program's current
// Scope. REPORT_SUBMIT is not NeedsScope in Policy, so the submission service
// must enforce lifecycle §5.2 itself.
func checkScope(program domain.Program, finding domain.Finding) error {
	if finding.AssetID == nil || *finding.AssetID == "" {
		return fmt.Errorf("%w: finding has no asset", ErrInvalidInput)
	}
	asset, _, ok := program.Scope.ResolveAsset(*finding.AssetID)
	if !ok {
		return fmt.Errorf("%w: asset is no longer in the program scope", ErrInvalidInput)
	}
	switch program.Scope.CheckAsset(asset) {
	case domain.ScopeMatchIncluded:
		return nil
	case domain.ScopeMatchConditional:
		return fmt.Errorf("%w: asset is conditional; human re-verification required", ErrInvalidInput)
	default:
		return fmt.Errorf("%w: asset is out of scope", ErrInvalidInput)
	}
}

func sameAsset(a, b *domain.AssetID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *Service) appendAudit(ctx context.Context, draft domain.ReportDraft, decision domain.ActionDecision, approval *domain.HumanApproval, outcome string, now time.Time) error {
	if s.auditor == nil {
		return nil
	}
	payload, err := json.Marshal(struct {
		ReportID   string `json:"report_id"`
		ProgramID  string `json:"program_id"`
		DecisionID string `json:"decision_id"`
		Outcome    string `json:"outcome"`
	}{
		ReportID: draft.ID, ProgramID: string(draft.ProgramID),
		DecisionID: string(decision.ID), Outcome: outcome,
	})
	if err != nil {
		return err
	}
	if _, err := s.auditor.Append(ctx, audit.Event{
		Type: "submission." + outcome, EntityType: "report", EntityID: draft.ID,
		ActorKind: string(domain.ActorHuman), ActorID: approval.ApprovedBy,
		Payload: payload, RecordedAt: now,
	}); err != nil {
		return fmt.Errorf("submit: audit: %w", err)
	}
	return nil
}
