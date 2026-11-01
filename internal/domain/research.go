package domain

import "time"

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
}

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
	ID             string
	FindingID      FindingID
	Reproduced     bool
	InScope        bool
	ImpactVerified bool
	DataMinimized  bool
	DuplicateRisk  float64
	Verdict        VerificationVerdict
	VerifiedBy     string
	Reasons        []string
	Evidence       []EvidenceID
	VerifiedAt     time.Time
}

type ReportDraft struct {
	ID           string
	FindingID    FindingID
	ProgramID    ProgramID
	Fields       map[string]string
	AIDisclosure map[string]string
	Attachments  []string
	Status       string
	CreatedAt    time.Time
}

type Submission struct {
	ID          SubmissionID
	ReportID    string
	ProgramID   ProgramID
	SubmittedAt time.Time
	PlatformURL string
	Receipt     string
	Status      string
	ApprovalID  string
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
)

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
	Evidence     []EvidenceID
}
