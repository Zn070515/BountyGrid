package domain

import (
	"strings"
	"time"
)

type ResearchTask struct {
	ID            TaskID
	OpportunityID OpportunityID
	ProgramID     ProgramID
	AssetID       *AssetID
	ScopeSnapshot Scope
	Constraints   Constraints
	Status        string
	Budget        *Money
	CreatedAt     time.Time
	ExpiresAt     *time.Time
}

type Finding struct {
	ID        FindingID
	TaskID    TaskID
	ProgramID ProgramID
	AssetID   *AssetID
	Title     string
	VulnType  string
	Severity  string
	Evidence  []EvidenceID
	Status    string
	CreatedAt time.Time
	// SourceDecisionIDs and ExecutionReceipts make the claim "this finding came
	// from an executed, authorized action" auditable. An ALLOW decision alone is
	// not proof that the tool ran; VERIFIED requires a completed receipt that
	// covers this finding's evidence.
	SourceDecisionIDs []DecisionID
	ExecutionReceipts []ReceiptID
}

// ReceiptID is the stable identifier of an ExecutionReceipt (receipts are
// looked up by idempotency key in storage; the ID is what a Finding records).
type ReceiptID string

type VerificationVerdict string

const (
	VerdictVerified      VerificationVerdict = "VERIFIED"
	VerdictNeedsEvidence VerificationVerdict = "NEEDS_MORE_EVIDENCE"
	VerdictOutOfScope    VerificationVerdict = "OUT_OF_SCOPE"
	VerdictDuplicateRisk VerificationVerdict = "DUPLICATE_RISK"
	VerdictRejected      VerificationVerdict = "REJECTED"
)

func (v VerificationVerdict) Valid() bool {
	switch v {
	case VerdictVerified, VerdictNeedsEvidence, VerdictOutOfScope, VerdictDuplicateRisk, VerdictRejected:
		return true
	default:
		return false
	}
}

type Verification struct {
	ID               string
	FindingID        FindingID
	Reproduced       bool
	InScope          bool
	ImpactVerified   bool
	DataMinimized    bool
	DuplicateChecked bool
	DuplicateRisk    float64
	PubliclyKnown    bool
	// AIAssisted and the human-verification trace are persisted so a later
	// audit can prove an AI-assisted finding was reviewed by a human.
	AIAssisted    bool
	HumanVerified bool
	// HumanVerificationRef is free-form legacy text. It is no longer sufficient
	// on its own: AI-assisted VERIFIED requires HumanReviewID, which is resolved
	// from the durable human_reviews store.
	HumanVerificationRef string
	HumanReviewID        string
	// ReceiptIDs are the completed execution receipts that prove the finding
	// came from an action that actually ran (not merely an ALLOW decision).
	ReceiptIDs []ReceiptID
	Verdict    VerificationVerdict
	VerifiedBy string
	Reasons    []string
	Evidence   []EvidenceID
	VerifiedAt time.Time
}

type ReportDraft struct {
	ID             string
	FindingID      FindingID
	ProgramID      ProgramID
	VerificationID string
	Fields         map[string]string
	AIDisclosure   map[string]string
	Attachments    []string
	// AttachmentHashes binds each attachment path to its content hash. It is
	// part of ContentHash so replacing an attachment after approval invalidates
	// the approval.
	AttachmentHashes map[string]string
	Status           string
	CreatedAt        time.Time
}

const (
	ReportStatusDraft          = "DRAFT"
	ReportStatusReadyForReview = "READY_FOR_REVIEW"
	ReportStatusApproved       = "APPROVED"
	ReportStatusSubmitted      = "SUBMITTED"
	ReportStatusAccepted       = "ACCEPTED"
	ReportStatusRejected       = "REJECTED"
	ReportStatusDuplicate      = "DUPLICATE"
)

func ReportStatusValid(s string) bool {
	switch s {
	case ReportStatusDraft, ReportStatusReadyForReview, ReportStatusApproved,
		ReportStatusSubmitted, ReportStatusAccepted, ReportStatusRejected, ReportStatusDuplicate:
		return true
	default:
		return false
	}
}

// HumanReviewVerdict is the reviewer's decision. Only APPROVED satisfies the
// AI-assisted verification gate.
type HumanReviewVerdict string

const (
	HumanReviewApproved HumanReviewVerdict = "APPROVED"
	HumanReviewRejected HumanReviewVerdict = "REJECTED"
)

func (v HumanReviewVerdict) Valid() bool {
	switch v {
	case HumanReviewApproved, HumanReviewRejected:
		return true
	default:
		return false
	}
}

// HumanReview is the durable object behind a human-verification claim. A bare
// string ("I checked it") is not evidence that a human reviewed the finding;
// the verifier resolves this object and checks the finding, reviewer, time,
// evidence and verdict.
type HumanReview struct {
	ID         string             `json:"id"`
	FindingID  FindingID          `json:"finding_id"`
	Reviewer   string             `json:"reviewer"`
	ReviewedAt time.Time          `json:"reviewed_at"`
	Verdict    HumanReviewVerdict `json:"verdict"`
	Evidence   []EvidenceID       `json:"evidence"`
	// NotesHash is the hash of the reviewer's notes. The notes themselves may
	// contain sensitive detail and stay local.
	NotesHash string `json:"notes_hash"`
}

// SubmissionStatus is the canonical status. NativeStatus keeps the platform's
// raw string so an unrecognized value is recorded as AMBIGUOUS rather than
// flowing into domain logic as success.
type SubmissionStatus string

const (
	SubmissionPending   SubmissionStatus = "PENDING"
	SubmissionSubmitted SubmissionStatus = "SUBMITTED"
	SubmissionAccepted  SubmissionStatus = "ACCEPTED"
	SubmissionRejected  SubmissionStatus = "REJECTED"
	SubmissionDuplicate SubmissionStatus = "DUPLICATE"
	SubmissionWithdrawn SubmissionStatus = "WITHDRAWN"
	SubmissionAmbiguous SubmissionStatus = "AMBIGUOUS"
	SubmissionDryRun    SubmissionStatus = "DRY_RUN"
)

func SubmissionStatusValid(s SubmissionStatus) bool {
	switch s {
	case SubmissionPending, SubmissionSubmitted, SubmissionAccepted, SubmissionRejected,
		SubmissionDuplicate, SubmissionWithdrawn, SubmissionAmbiguous, SubmissionDryRun:
		return true
	default:
		return false
	}
}

// NormalizeSubmissionStatus maps a platform/native status to the canonical set.
// Unknown non-empty statuses become AMBIGUOUS: a platform saying "审核中" or
// "done" must not be interpreted as SUBMITTED or ACCEPTED by accident.
func NormalizeSubmissionStatus(raw string) SubmissionStatus {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DRY_RUN":
		return SubmissionDryRun
	case "PENDING", "QUEUED", "IN_REVIEW", "REVIEWING":
		return SubmissionPending
	case "SUBMITTED", "SENT":
		return SubmissionSubmitted
	case "ACCEPTED", "VALID", "APPROVED":
		return SubmissionAccepted
	case "REJECTED", "INVALID", "DENIED":
		return SubmissionRejected
	case "DUPLICATE", "DUP":
		return SubmissionDuplicate
	case "WITHDRAWN", "CANCELLED":
		return SubmissionWithdrawn
	default:
		return SubmissionAmbiguous
	}
}

type Submission struct {
	ID          SubmissionID
	ReportID    string
	ProgramID   ProgramID
	SubmittedAt time.Time
	PlatformURL string
	Receipt     string
	// Status is canonical (SubmissionStatus). NativeStatus preserves the raw
	// platform value for audit.
	Status       SubmissionStatus
	NativeStatus string
	ApprovalID   string
	// ContentHash is the report content bound by the approval. Legacy rows may
	// leave it empty.
	ContentHash string
	Evidence    []EvidenceID
}

type LedgerKind string

const (
	LedgerExpected  LedgerKind = "EXPECTED"
	LedgerSubmitted LedgerKind = "SUBMITTED"
	LedgerAccepted  LedgerKind = "ACCEPTED"
	LedgerPaid      LedgerKind = "PAID"
	LedgerFee       LedgerKind = "FEE"
	LedgerTax       LedgerKind = "TAX"
	LedgerRefund    LedgerKind = "REFUND"
	// LedgerReversal is a cash clawback/correction of an earlier PAID/REFUND
	// entry. The amount is non-negative and reduces net cash.
	LedgerReversal LedgerKind = "REVERSAL"
)

const (
	LedgerStatusRecorded   = "RECORDED"
	LedgerStatusReconciled = "RECONCILED"
	LedgerStatusDisputed   = "DISPUTED"
)

func LedgerStatusValid(s string) bool {
	switch s {
	case LedgerStatusRecorded, LedgerStatusReconciled, LedgerStatusDisputed:
		return true
	default:
		return false
	}
}

type LedgerEntry struct {
	ID           string
	Platform     Platform
	ProgramID    ProgramID
	FindingID    *FindingID
	SubmissionID *SubmissionID
	Kind         LedgerKind
	Amount       Money
	Status       string
	OccurredAt   time.Time
	// ExternalRef is the platform payout/statement/bank reference. It is the
	// idempotency key for imported cash events (unique per platform+kind when
	// non-empty).
	ExternalRef string
	// ReversesID is required for REVERSAL and points at the original entry.
	ReversesID string
	Notes      string
	Evidence   []EvidenceID
}
