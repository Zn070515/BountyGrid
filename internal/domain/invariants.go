package domain

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/Zn070515/BountyGrid/internal/timeutil"
)

func (e Evidence) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("evidence: id is empty")
	}
	if e.RawRef == "" {
		return fmt.Errorf("evidence %s: raw_ref is empty", e.ID)
	}
	if !e.Platform.Valid() {
		return fmt.Errorf("evidence %s: invalid platform %q", e.ID, e.Platform)
	}
	if !e.SourceType.Valid() {
		return fmt.Errorf("evidence %s: invalid source type %q", e.ID, e.SourceType)
	}
	if !e.Transport.Valid() {
		return fmt.Errorf("evidence %s: invalid transport %q", e.ID, e.Transport)
	}
	if e.URL == "" {
		return fmt.Errorf("evidence %s: url is empty", e.ID)
	}
	if e.CapturedAt.IsZero() {
		return fmt.Errorf("evidence %s: captured_at is zero", e.ID)
	}
	if e.ContentHash == "" {
		return fmt.Errorf("evidence %s: content_hash is empty", e.ID)
	}
	return nil
}

func (f ObservedFact) Validate() error {
	if f.ID == "" {
		return fmt.Errorf("fact: id is empty")
	}
	if !f.Platform.Valid() {
		return fmt.Errorf("fact %s: invalid platform %q", f.ID, f.Platform)
	}
	if f.Predicate == "" {
		return fmt.Errorf("fact %s: predicate is empty", f.ID)
	}
	if f.ObservedAt.IsZero() {
		return fmt.Errorf("fact %s: observed_at is zero", f.ID)
	}
	if len(f.EvidenceRefs) == 0 {
		return fmt.Errorf("fact %s: evidence_refs is empty", f.ID)
	}
	if !f.Confidence.Valid() {
		return fmt.Errorf("fact %s: confidence %v out of range", f.ID, f.Confidence)
	}
	if f.ExtractorVersion == "" {
		return fmt.Errorf("fact %s: extractor_version is empty", f.ID)
	}
	switch f.Subject {
	case SubjectAccount:
		if f.AccountID == nil {
			return fmt.Errorf("fact %s: account subject requires account_id", f.ID)
		}
	case SubjectProgram:
		if f.ProgramID == nil {
			return fmt.Errorf("fact %s: program subject requires program_id", f.ID)
		}
	case SubjectAsset:
		if f.AssetID == nil {
			return fmt.Errorf("fact %s: asset subject requires asset_id", f.ID)
		}
	case SubjectPayout:
		if f.AccountID == nil && f.ProgramID == nil {
			return fmt.Errorf("fact %s: payout subject requires account_id or program_id", f.ID)
		}
	default:
		return fmt.Errorf("fact %s: invalid subject %q", f.ID, f.Subject)
	}
	return nil
}

func (s DerivedState) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("state: id is empty")
	}
	if !s.Platform.Valid() {
		return fmt.Errorf("state %s: invalid platform %q", s.ID, s.Platform)
	}
	if s.State == "" {
		return fmt.Errorf("state %s: state is empty", s.ID)
	}
	if len(s.Inputs) == 0 {
		return fmt.Errorf("state %s: inputs is empty", s.ID)
	}
	if s.ObservedAt.IsZero() || s.DerivedAt.IsZero() {
		return fmt.Errorf("state %s: timestamps are zero", s.ID)
	}
	if s.ValidUntil.IsZero() || !s.ValidUntil.After(s.DerivedAt) {
		return fmt.Errorf("state %s: valid_until must be after derived_at", s.ID)
	}
	if !s.Confidence.Valid() {
		return fmt.Errorf("state %s: confidence %v out of range", s.ID, s.Confidence)
	}
	if s.RuleVersion == "" {
		return fmt.Errorf("state %s: rule_version is empty", s.ID)
	}
	return nil
}

func (g EligibilityGate) Validate() error {
	if !g.Kind.Valid() {
		return fmt.Errorf("gate: invalid kind %q", g.Kind)
	}
	if !g.Satisfied.Valid() {
		return fmt.Errorf("gate %s: invalid satisfaction %q", g.Kind, g.Satisfied)
	}
	if g.Required && g.Satisfied == GateNotRequired {
		return fmt.Errorf("gate %s: required gate cannot be NOT_REQUIRED", g.Kind)
	}
	if g.Blocking && !g.Required {
		return fmt.Errorf("gate %s: blocking gate must be required", g.Kind)
	}
	return nil
}

func (s EligibilityState) Validate() error {
	if !s.Decision.Valid() {
		return fmt.Errorf("eligibility: invalid decision %q", s.Decision)
	}
	if s.EvaluatedAt.IsZero() {
		return fmt.Errorf("eligibility: evaluated_at is zero")
	}
	if s.PolicyVersion == "" {
		return fmt.Errorf("eligibility: policy_version is empty")
	}
	for _, g := range s.Gates {
		if err := g.Validate(); err != nil {
			return err
		}
	}

	blockingUnsatisfied := false
	blockingHuman := false
	unknown := false
	for _, g := range s.Gates {
		if !g.Required {
			continue
		}
		if g.Satisfied == GateUnknown {
			unknown = true
		}
		if g.Blocking && g.Satisfied == GateUnsatisfied {
			blockingUnsatisfied = true
			if g.Kind.HumanResolvable() {
				blockingHuman = true
			}
		}
	}

	switch s.Decision {
	case Eligible:
		if blockingUnsatisfied || unknown {
			return fmt.Errorf("eligibility: ELIGIBLE but has blocking/unknown gates")
		}
	case Ineligible:
		if !blockingUnsatisfied || blockingHuman {
			return fmt.Errorf("eligibility: INELIGIBLE requires a non-human blocking gate")
		}
	case HumanActionNeeded:
		if !blockingHuman {
			return fmt.Errorf("eligibility: HUMAN_ACTION_REQUIRED requires a human-resolvable blocking gate")
		}
	case EligibilityUnknown:
		if blockingUnsatisfied || !unknown {
			return fmt.Errorf("eligibility: UNKNOWN requires an unknown gate and no blocking gate")
		}
	}
	return nil
}

func (a Account) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("account: id is empty")
	}
	if !a.Platform.Valid() {
		return fmt.Errorf("account %s: invalid platform %q", a.ID, a.Platform)
	}
	if a.ObservedAt.IsZero() {
		return fmt.Errorf("account %s: observed_at is zero", a.ID)
	}
	for _, g := range a.Gates {
		if err := g.Validate(); err != nil {
			return err
		}
	}
	for _, g := range a.Capability.BlockingReasons {
		if err := g.Validate(); err != nil {
			return err
		}
	}
	if a.Capability.CanSubmit {
		for _, g := range a.Gates {
			// Program-scoped gates constrain one program's submission, not the
			// account-wide capability (e.g. Butian practical certification).
			if g.ProgramID != nil || !g.Required || !g.Blocking || g.Satisfied != GateUnsatisfied {
				continue
			}
			if g.Kind.BlocksSubmit() {
				return fmt.Errorf("account %s: CanSubmit with blocking gate %s", a.ID, g.Kind)
			}
		}
		if a.ComplianceExamCompleted != nil && !*a.ComplianceExamCompleted {
			return fmt.Errorf("account %s: CanSubmit with incomplete compliance exam", a.ID)
		}
	}
	if a.Capability.CanPayout {
		for _, g := range a.Gates {
			if g.ProgramID != nil || !g.Required || !g.Blocking || g.Satisfied != GateUnsatisfied {
				continue
			}
			if g.Kind.BlocksPayout() {
				return fmt.Errorf("account %s: CanPayout with blocking gate %s", a.ID, g.Kind)
			}
		}
		if a.BankBound != nil && !*a.BankBound {
			return fmt.Errorf("account %s: CanPayout with unbound bank card", a.ID)
		}
	}
	return nil
}

func (s Scope) Validate() error {
	if !s.Visibility.Valid() {
		return fmt.Errorf("scope: invalid disclosure %q", s.Visibility)
	}
	if !s.Access.Valid() {
		return fmt.Errorf("scope: invalid access %q", s.Access)
	}
	if s.Visibility == ScopePublic && s.Access != ScopeAccessGranted {
		return fmt.Errorf("scope: public disclosure requires GRANTED access")
	}
	if s.Visibility == ScopeUnknown && s.ContentsKnown {
		return fmt.Errorf("scope: unknown disclosure cannot have known contents")
	}
	if s.ObservedAt.IsZero() {
		return fmt.Errorf("scope: observed_at is zero")
	}
	if !s.ValidUntil.IsZero() && !s.ValidUntil.After(s.ObservedAt) {
		return fmt.Errorf("scope: valid_until must be after observed_at")
	}
	if s.Access != ScopeAccessGranted {
		if s.ContentsKnown || len(s.Include) > 0 || len(s.Exclude) > 0 || len(s.Conditional) > 0 {
			return fmt.Errorf("scope: non-granted access must not contain contents")
		}
	}
	if !s.ContentsKnown && (len(s.Include) > 0 || len(s.Exclude) > 0 || len(s.Conditional) > 0) {
		return fmt.Errorf("scope: contents_known=false with non-empty asset lists")
	}
	for _, a := range append(append(append([]Asset{}, s.Include...), s.Exclude...), s.Conditional...) {
		if a.ID == "" || a.Value == "" {
			return fmt.Errorf("scope: asset id/value is empty")
		}
	}
	return nil
}

func (p Program) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("program: id is empty")
	}
	if !p.Platform.Valid() {
		return fmt.Errorf("program %s: invalid platform %q", p.ID, p.Platform)
	}
	if p.Name == "" {
		return fmt.Errorf("program %s: name is empty", p.ID)
	}
	if !p.Type.Valid() {
		return fmt.Errorf("program %s: invalid type %q", p.ID, p.Type)
	}
	if !p.Status.Status.Valid() {
		return fmt.Errorf("program %s: invalid status %q", p.ID, p.Status.Status)
	}
	if p.Status.ObservedAt.IsZero() {
		return fmt.Errorf("program %s: status observed_at is zero", p.ID)
	}
	if p.Status.ValidUntil.IsZero() || !p.Status.ValidUntil.After(p.Status.ObservedAt) {
		return fmt.Errorf("program %s: status valid_until must be after observed_at", p.ID)
	}
	if !p.Status.Confidence.Valid() {
		return fmt.Errorf("program %s: status confidence %v out of range", p.ID, p.Status.Confidence)
	}
	if !p.ModelEgressPolicy.Valid() {
		return fmt.Errorf("program %s: invalid model egress policy %q", p.ID, p.ModelEgressPolicy)
	}
	if err := p.Constraints.Validate(); err != nil {
		return fmt.Errorf("program %s: %w", p.ID, err)
	}
	if err := p.Eligibility.Validate(); err != nil {
		return fmt.Errorf("program %s: %w", p.ID, err)
	}
	for _, g := range p.Gates {
		if err := g.Validate(); err != nil {
			return fmt.Errorf("program %s: %w", p.ID, err)
		}
	}
	if err := p.Scope.Validate(); err != nil {
		return fmt.Errorf("program %s: %w", p.ID, err)
	}
	if !p.Reward.TaxBasis.Valid() {
		return fmt.Errorf("program %s: invalid tax basis %q", p.ID, p.Reward.TaxBasis)
	}
	for _, c := range []CapabilityStatus{
		p.Automation.AIAnalysis, p.Automation.AutonomousAgent, p.Automation.ActiveHTTP,
		p.Automation.BrowserAutomation, p.Automation.AutomatedScanning, p.Automation.DirectoryEnumeration,
		p.Automation.SubdomainDiscovery, p.Automation.HighConcurrency,
		p.Automation.BruteForce, p.Automation.CredentialStuffing,
		p.Automation.FileUpload, p.Automation.StateMutation, p.Automation.RCEVerification,
		p.Automation.SensitiveDataRead, p.Automation.InternalNetwork,
		p.Automation.LateralMovement, p.Automation.DoS,
	} {
		if !c.Valid() {
			return fmt.Errorf("program %s: invalid capability status %q", p.ID, c)
		}
	}
	return nil
}

// Validate rejects malformed execution constraints. A zero/negative limit is
// ambiguous and would otherwise fail open or block everything unexpectedly.
func (c Constraints) Validate() error {
	if c.MaxRPS != nil && *c.MaxRPS <= 0 {
		return fmt.Errorf("constraints: max_rps must be positive")
	}
	if c.MaxConcurrency != nil && *c.MaxConcurrency <= 0 {
		return fmt.Errorf("constraints: max_concurrency must be positive")
	}
	for _, l := range c.PerInterfaceDailyLimits {
		if l.Interface == "" {
			return fmt.Errorf("constraints: daily limit interface is empty")
		}
		if l.Amount <= 0 {
			return fmt.Errorf("constraints: daily limit %s must be positive", l.Interface)
		}
	}
	if c.BudgetCap != nil {
		if c.BudgetCap.Amount <= 0 {
			return fmt.Errorf("constraints: budget cap must be positive")
		}
		if c.BudgetCap.Currency == "" {
			return fmt.Errorf("constraints: budget cap currency is empty")
		}
	}
	for _, w := range c.AllowedTimeWindows {
		if w.Start.IsZero() || w.End.IsZero() || !w.End.After(w.Start) {
			return fmt.Errorf("constraints: invalid allowed time window")
		}
		if w.TZ != "" {
			if _, err := timeutil.LoadLocation(w.TZ); err != nil {
				return fmt.Errorf("constraints: invalid time window tz %q", w.TZ)
			}
		}
	}
	for _, ip := range c.AllowedEgressIPs {
		if strings.TrimSpace(ip) == "" {
			return fmt.Errorf("constraints: allowed egress ip is empty")
		}
		if _, err := netip.ParseAddr(strings.TrimSpace(ip)); err != nil {
			return fmt.Errorf("constraints: invalid allowed egress ip %q", ip)
		}
	}
	return nil
}

func (r ActionRequest) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("action request: id is empty")
	}
	if !r.Actor.Kind.Valid() {
		return fmt.Errorf("action request %s: invalid actor kind %q", r.ID, r.Actor.Kind)
	}
	if !r.Action.Valid() {
		return fmt.Errorf("action request %s: invalid action %q", r.ID, r.Action)
	}
	if r.AccountID == "" || r.ProgramID == "" {
		return fmt.Errorf("action request %s: account_id/program_id required", r.ID)
	}
	if r.AssetID != nil && *r.AssetID == "" {
		return fmt.Errorf("action request %s: asset_id is set but empty", r.ID)
	}
	if r.RequestedAt.IsZero() {
		return fmt.Errorf("action request %s: requested_at is zero", r.ID)
	}
	return nil
}

func (s CompetitionSnapshot) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("competition snapshot: id is empty")
	}
	if !s.Platform.Valid() {
		return fmt.Errorf("competition snapshot %s: invalid platform %q", s.ID, s.Platform)
	}
	if s.Scope == "" {
		return fmt.Errorf("competition snapshot %s: scope is empty", s.ID)
	}
	if s.ObservedAt.IsZero() {
		return fmt.Errorf("competition snapshot %s: observed_at is zero", s.ID)
	}
	if !s.Confidence.Valid() {
		return fmt.Errorf("competition snapshot %s: confidence %v out of range", s.ID, s.Confidence)
	}
	if !s.ValidUntil.IsZero() && !s.ValidUntil.After(s.ObservedAt) {
		return fmt.Errorf("competition snapshot %s: valid_until must be after observed_at", s.ID)
	}
	for name, v := range map[string]*float64{
		"top1_share": s.Top1Share, "top3_share": s.Top3Share, "top10_share": s.Top10Share,
		"duplicate_pressure": s.DuplicatePressure, "saturation": s.Saturation,
	} {
		if v != nil && (*v < 0 || *v > 1) {
			return fmt.Errorf("competition snapshot %s: %s out of range", s.ID, name)
		}
	}
	// Concentration shares must be monotonic: top1 cannot exceed top3, and
	// top3 cannot exceed top10. A violation means the input is corrupt.
	if s.Top1Share != nil && s.Top3Share != nil && *s.Top1Share > *s.Top3Share {
		return fmt.Errorf("competition snapshot %s: top1_share > top3_share", s.ID)
	}
	if s.Top3Share != nil && s.Top10Share != nil && *s.Top3Share > *s.Top10Share {
		return fmt.Errorf("competition snapshot %s: top3_share > top10_share", s.ID)
	}
	if s.Top1Share != nil && s.Top10Share != nil && *s.Top1Share > *s.Top10Share {
		return fmt.Errorf("competition snapshot %s: top1_share > top10_share", s.ID)
	}
	return nil
}

func (d ActionDecision) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("decision: id is empty")
	}
	if d.RequestID == "" {
		return fmt.Errorf("decision %s: request_id is empty", d.ID)
	}
	if d.Request.ID != "" {
		if d.Request.ID != d.RequestID {
			return fmt.Errorf("decision %s: request id mismatch", d.ID)
		}
		if d.Request.ProgramID != d.ProgramID {
			return fmt.Errorf("decision %s: request program mismatch", d.ID)
		}
		if d.Request.Action != d.Action {
			return fmt.Errorf("decision %s: request action mismatch", d.ID)
		}
	}
	if d.Environment != nil {
		if err := d.Environment.Validate(); err != nil {
			return fmt.Errorf("decision %s: %w", d.ID, err)
		}
	}
	if !d.Decision.Valid() {
		return fmt.Errorf("decision %s: invalid decision %q", d.ID, d.Decision)
	}
	if d.DecidedAt.IsZero() {
		return fmt.Errorf("decision %s: decided_at is zero", d.ID)
	}
	if d.PolicyVersion == "" {
		return fmt.Errorf("decision %s: policy_version is empty", d.ID)
	}
	if d.Decision == DecisionAllow {
		// ALLOW is executable and must carry an expiry so the Action Gateway can
		// re-check it before execution.
		if d.ExpiresAt == nil || d.ExpiresAt.IsZero() || !d.ExpiresAt.After(d.DecidedAt) {
			return fmt.Errorf("decision %s: ALLOW requires expires_at after decided_at", d.ID)
		}
	}
	if d.Decision == DecisionAllow && d.Action.HumanOnly() {
		if d.HumanApproval == nil {
			return fmt.Errorf("decision %s: ALLOW for human-only action without approval", d.ID)
		}
		if !d.HumanApproval.SingleUse {
			return fmt.Errorf("decision %s: human-only approval must be single-use", d.ID)
		}
	}
	if d.HumanApproval != nil {
		if d.HumanApproval.ApprovedBy == "" || d.HumanApproval.ApprovedAt.IsZero() {
			return fmt.Errorf("decision %s: approval missing approver/time", d.ID)
		}
		if d.HumanApproval.Action != d.Action {
			return fmt.Errorf("decision %s: approval action mismatch", d.ID)
		}
		if d.Decision == DecisionAllow {
			if d.HumanApproval.ProgramID != d.ProgramID {
				return fmt.Errorf("decision %s: approval program mismatch", d.ID)
			}
			if d.HumanApproval.RequestID != d.RequestID {
				return fmt.Errorf("decision %s: approval request mismatch", d.ID)
			}
			if d.HumanApproval.ApprovedAt.After(d.DecidedAt) {
				return fmt.Errorf("decision %s: approval is from the future", d.ID)
			}
			if d.HumanApproval.ExpiresAt.IsZero() || !d.HumanApproval.ExpiresAt.After(d.DecidedAt) {
				return fmt.Errorf("decision %s: approval expired before decision", d.ID)
			}
		}
	}
	return nil
}

func (d ReportDraft) Validate() error {
	if d.ID == "" || d.FindingID == "" || d.ProgramID == "" || d.VerificationID == "" {
		return fmt.Errorf("report draft: id/finding_id/program_id/verification_id required")
	}
	if !ReportStatusValid(d.Status) {
		return fmt.Errorf("report draft %s: invalid status %q", d.ID, d.Status)
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("report draft %s: created_at is zero", d.ID)
	}
	for _, a := range d.Attachments {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("report draft %s: empty attachment path", d.ID)
		}
	}
	return nil
}

func (v Verification) Validate() error {
	if v.ID == "" || v.FindingID == "" {
		return fmt.Errorf("verification: id/finding_id required")
	}
	if !v.Verdict.Valid() {
		return fmt.Errorf("verification %s: invalid verdict %q", v.ID, v.Verdict)
	}
	if v.VerifiedBy == "" {
		return fmt.Errorf("verification %s: verified_by is empty", v.ID)
	}
	if v.VerifiedAt.IsZero() {
		return fmt.Errorf("verification %s: verified_at is zero", v.ID)
	}
	if v.DuplicateRisk < 0 || v.DuplicateRisk > 1 {
		return fmt.Errorf("verification %s: duplicate_risk out of range", v.ID)
	}
	if v.Verdict == VerdictOutOfScope && v.InScope {
		return fmt.Errorf("verification %s: OUT_OF_SCOPE cannot be in_scope", v.ID)
	}
	if v.Verdict == VerdictVerified {
		// VERIFIED is executable downstream: every dimension must be present and
		// evidence-backed. Rules may set this; a model alone cannot.
		if !v.Reproduced || !v.InScope || !v.ImpactVerified || !v.DataMinimized {
			return fmt.Errorf("verification %s: VERIFIED requires reproduced/in_scope/impact/data_minimized", v.ID)
		}
		if len(v.Evidence) == 0 {
			return fmt.Errorf("verification %s: VERIFIED requires evidence", v.ID)
		}
		if len(v.ReceiptIDs) == 0 {
			return fmt.Errorf("verification %s: VERIFIED requires an execution receipt", v.ID)
		}
		if !v.DuplicateChecked {
			return fmt.Errorf("verification %s: VERIFIED requires duplicate_check", v.ID)
		}
		if v.AIAssisted {
			if !v.HumanVerified {
				return fmt.Errorf("verification %s: VERIFIED AI-assisted finding requires human verification", v.ID)
			}
			if strings.TrimSpace(v.HumanReviewID) == "" {
				return fmt.Errorf("verification %s: VERIFIED AI-assisted finding requires a durable human review", v.ID)
			}
		}
	}
	return nil
}

func (s Submission) Validate() error {
	if s.ID == "" || s.ReportID == "" || s.ProgramID == "" {
		return fmt.Errorf("submission: id/report_id/program_id required")
	}
	if s.SubmittedAt.IsZero() {
		return fmt.Errorf("submission %s: submitted_at is zero", s.ID)
	}
	if s.ApprovalID == "" {
		return fmt.Errorf("submission %s: human approval id required", s.ID)
	}
	if s.Receipt == "" {
		return fmt.Errorf("submission %s: receipt is empty", s.ID)
	}
	if !SubmissionStatusValid(s.Status) {
		return fmt.Errorf("submission %s: invalid canonical status %q", s.ID, s.Status)
	}
	return nil
}

func (r HumanReview) Validate() error {
	if strings.TrimSpace(r.ID) == "" || r.FindingID == "" {
		return fmt.Errorf("human review: id/finding_id required")
	}
	if strings.TrimSpace(r.Reviewer) == "" {
		return fmt.Errorf("human review %s: reviewer is empty", r.ID)
	}
	if r.ReviewedAt.IsZero() {
		return fmt.Errorf("human review %s: reviewed_at is zero", r.ID)
	}
	if !r.Verdict.Valid() {
		return fmt.Errorf("human review %s: invalid verdict %q", r.ID, r.Verdict)
	}
	if len(r.Evidence) == 0 {
		return fmt.Errorf("human review %s: evidence is required", r.ID)
	}
	if strings.TrimSpace(r.NotesHash) == "" {
		return fmt.Errorf("human review %s: notes_hash is empty", r.ID)
	}
	return nil
}

func (l LedgerEntry) Validate() error {
	if l.ID == "" || l.ProgramID == "" {
		return fmt.Errorf("ledger: id/program_id required")
	}
	if !l.Platform.Valid() {
		return fmt.Errorf("ledger %s: invalid platform %q", l.ID, l.Platform)
	}
	switch l.Kind {
	case LedgerExpected, LedgerSubmitted, LedgerAccepted, LedgerPaid,
		LedgerFee, LedgerTax, LedgerRefund, LedgerReversal:
	default:
		return fmt.Errorf("ledger %s: invalid kind %q", l.ID, l.Kind)
	}
	if !LedgerStatusValid(l.Status) {
		return fmt.Errorf("ledger %s: invalid status %q", l.ID, l.Status)
	}
	if l.Amount.Amount < 0 {
		return fmt.Errorf("ledger %s: negative amount", l.ID)
	}
	if l.Amount.Currency == "" {
		return fmt.Errorf("ledger %s: currency is empty", l.ID)
	}
	if l.OccurredAt.IsZero() {
		return fmt.Errorf("ledger %s: occurred_at is zero", l.ID)
	}
	// Cash events must carry an external reference so a re-import cannot create
	// a second identical payment under a new ID.
	switch l.Kind {
	case LedgerPaid, LedgerRefund, LedgerReversal:
		if strings.TrimSpace(l.ExternalRef) == "" {
			return fmt.Errorf("ledger %s: %s requires external_ref", l.ID, l.Kind)
		}
	}
	switch l.Kind {
	case LedgerSubmitted:
		if l.SubmissionID == nil || *l.SubmissionID == "" {
			return fmt.Errorf("ledger %s: SUBMITTED requires submission_id", l.ID)
		}
	case LedgerAccepted, LedgerFee, LedgerTax:
		if (l.FindingID == nil || *l.FindingID == "") &&
			(l.SubmissionID == nil || *l.SubmissionID == "") {
			return fmt.Errorf("ledger %s: %s requires finding_id or submission_id", l.ID, l.Kind)
		}
	case LedgerReversal:
		if strings.TrimSpace(l.ReversesID) == "" {
			return fmt.Errorf("ledger %s: REVERSAL requires reverses_id", l.ID)
		}
		if l.ReversesID == l.ID {
			return fmt.Errorf("ledger %s: REVERSAL cannot reverse itself", l.ID)
		}
	}
	return nil
}
