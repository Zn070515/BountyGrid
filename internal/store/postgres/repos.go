package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("postgres: not found")

type Repo struct {
	Pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{Pool: pool} }

func mustJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal: %w", err)
	}
	return string(b), nil
}

// ---------- Account ----------

func (r *Repo) UpsertAccount(ctx context.Context, a domain.Account) error {
	if err := a.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(a)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO accounts
			(id, platform, registered, identity_verified, level, reputation_score,
			 compliance_exam_completed, practical_certification, bank_bound, wechat_bound,
			 data, observed_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,now())
		ON CONFLICT (id) DO UPDATE SET
			platform=EXCLUDED.platform, registered=EXCLUDED.registered,
			identity_verified=EXCLUDED.identity_verified, level=EXCLUDED.level,
			reputation_score=EXCLUDED.reputation_score,
			compliance_exam_completed=EXCLUDED.compliance_exam_completed,
			practical_certification=EXCLUDED.practical_certification,
			bank_bound=EXCLUDED.bank_bound, wechat_bound=EXCLUDED.wechat_bound,
			data=EXCLUDED.data, observed_at=EXCLUDED.observed_at, updated_at=now()`,
		a.ID, a.Platform, a.Registered, a.IdentityVerified, a.Level, a.ReputationScore,
		a.ComplianceExamCompleted, a.PracticalCertification, a.BankBound, a.WechatBound,
		data, a.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert account: %w", err)
	}
	return nil
}

func (r *Repo) GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM accounts WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.Account{}, ErrNotFound
		}
		return domain.Account{}, fmt.Errorf("postgres: get account: %w", err)
	}
	var a domain.Account
	if err := json.Unmarshal(data, &a); err != nil {
		return domain.Account{}, fmt.Errorf("postgres: decode account: %w", err)
	}
	return a, nil
}

// ---------- Program ----------

func (r *Repo) UpsertProgram(ctx context.Context, p domain.Program) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, err := mustJSON(p)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO programs
			(id, platform, native_id, name, type, status, status_reason,
			 status_observed_at, status_valid_until, next_check_at, data, observed_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,now())
		ON CONFLICT (id) DO UPDATE SET
			platform=EXCLUDED.platform, native_id=EXCLUDED.native_id,
			name=EXCLUDED.name, type=EXCLUDED.type, status=EXCLUDED.status,
			status_reason=EXCLUDED.status_reason,
			status_observed_at=EXCLUDED.status_observed_at,
			status_valid_until=EXCLUDED.status_valid_until,
			next_check_at=EXCLUDED.next_check_at, data=EXCLUDED.data,
			observed_at=EXCLUDED.observed_at, updated_at=now()`,
		p.ID, p.Platform, p.NativeID, p.Name, p.Type, p.Status.Status, p.Status.Reason,
		p.Status.ObservedAt, p.Status.ValidUntil, p.NextCheckAt, data, p.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert program: %w", err)
	}
	return nil
}

func (r *Repo) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM programs WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.Program{}, ErrNotFound
		}
		return domain.Program{}, fmt.Errorf("postgres: get program: %w", err)
	}
	var p domain.Program
	if err := json.Unmarshal(data, &p); err != nil {
		return domain.Program{}, fmt.Errorf("postgres: decode program: %w", err)
	}
	return p, nil
}

func (r *Repo) ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT data FROM programs
		WHERE ($1 = '' OR platform = $1)
		ORDER BY id`, string(platform))
	if err != nil {
		return nil, fmt.Errorf("postgres: list programs: %w", err)
	}
	defer rows.Close()
	var out []domain.Program
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p domain.Program
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("postgres: decode program: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) ListProgramsDue(ctx context.Context, now time.Time, limit int) ([]domain.Program, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT data FROM programs
		WHERE next_check_at IS NULL OR next_check_at <= $1
		ORDER BY next_check_at NULLS FIRST LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list programs due: %w", err)
	}
	defer rows.Close()
	var out []domain.Program
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p domain.Program
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("postgres: decode program: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- Evidence ----------

func (r *Repo) PutEvidence(ctx context.Context, e domain.Evidence) error {
	if err := e.Validate(); err != nil {
		return err
	}
	redactions, err := mustJSON(e.Redactions)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO evidence
			(id, platform, source_type, transport, url, page_title, captured_at,
			 content_hash, raw_ref, redactions)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`,
		e.ID, e.Platform, e.SourceType, e.Transport, e.URL, e.PageTitle,
		e.CapturedAt, e.ContentHash, e.RawRef, redactions,
	)
	if err != nil {
		return fmt.Errorf("postgres: put evidence: %w", err)
	}
	return nil
}

func (r *Repo) GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	var e domain.Evidence
	var redactions []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT id, platform, source_type, transport, url, page_title, captured_at,
		       content_hash, raw_ref, redactions
		FROM evidence WHERE id=$1`, id,
	).Scan(&e.ID, &e.Platform, &e.SourceType, &e.Transport, &e.URL, &e.PageTitle,
		&e.CapturedAt, &e.ContentHash, &e.RawRef, &redactions)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.Evidence{}, ErrNotFound
		}
		return domain.Evidence{}, err
	}
	if len(redactions) > 0 {
		_ = json.Unmarshal(redactions, &e.Redactions)
	}
	return e, nil
}

// ---------- Facts ----------

func (r *Repo) PutFacts(ctx context.Context, fs []domain.ObservedFact) error {
	seen := make(map[domain.FactID]struct{}, len(fs))
	for _, f := range fs {
		if err := f.Validate(); err != nil {
			return err
		}
		if _, dup := seen[f.ID]; dup {
			return errors.New("postgres: duplicate fact id in batch")
		}
		seen[f.ID] = struct{}{}
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin facts: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, f := range fs {
		value, err := mustJSON(f.Value)
		if err != nil {
			return err
		}
		refs, err := mustJSON(f.EvidenceRefs)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO observed_facts
				(id, subject, platform, account_id, program_id, asset_id, predicate,
				 value, unit, observed_at, evidence_refs, confidence, extractor_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11::jsonb,$12,$13)
			ON CONFLICT (id) DO NOTHING`,
			f.ID, f.Subject, f.Platform, f.AccountID, f.ProgramID, f.AssetID,
			f.Predicate, value, f.Unit, f.ObservedAt, refs, f.Confidence, f.ExtractorVersion,
		)
		if err != nil {
			return fmt.Errorf("postgres: put fact %s: %w", f.ID, err)
		}
		if tag.RowsAffected() == 0 {
			// Re-running an extractor is idempotent; reusing an ID for different
			// content is a conflict and must not overwrite immutable facts.
			existing, err := getFact(ctx, tx, f.ID)
			if err != nil {
				return err
			}
			if !existing.Equal(f) {
				return fmt.Errorf("postgres: fact %s: id exists with different content", f.ID)
			}
		}
	}
	return tx.Commit(ctx)
}

func (r *Repo) ListFactsByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, subject, platform, account_id, program_id, asset_id, predicate,
		       value, unit, observed_at, evidence_refs, confidence, extractor_version
		FROM observed_facts
		WHERE program_id=$1 AND superseded_by IS NULL
		ORDER BY observed_at ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ObservedFact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func getFact(ctx context.Context, tx pgx.Tx, id domain.FactID) (domain.ObservedFact, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, subject, platform, account_id, program_id, asset_id, predicate,
		       value, unit, observed_at, evidence_refs, confidence, extractor_version
		FROM observed_facts WHERE id=$1`, id)
	f, err := scanFact(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ObservedFact{}, ErrNotFound
		}
		return domain.ObservedFact{}, err
	}
	return f, nil
}

func scanFact(row pgx.Row) (domain.ObservedFact, error) {
	var f domain.ObservedFact
	var value, refs []byte
	var accountID, programID, assetID *string
	if err := row.Scan(&f.ID, &f.Subject, &f.Platform, &accountID, &programID,
		&assetID, &f.Predicate, &value, &f.Unit, &f.ObservedAt, &refs,
		&f.Confidence, &f.ExtractorVersion); err != nil {
		return domain.ObservedFact{}, err
	}
	if accountID != nil {
		v := domain.AccountID(*accountID)
		f.AccountID = &v
	}
	if programID != nil {
		v := domain.ProgramID(*programID)
		f.ProgramID = &v
	}
	if assetID != nil {
		v := domain.AssetID(*assetID)
		f.AssetID = &v
	}
	if len(value) > 0 {
		_ = json.Unmarshal(value, &f.Value)
	}
	if len(refs) > 0 {
		_ = json.Unmarshal(refs, &f.EvidenceRefs)
	}
	return f, nil
}

func (r *Repo) SupersedeFact(ctx context.Context, id, by domain.FactID) error {
	if id == by {
		return errors.New("postgres: self-supersede is not allowed")
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin supersede: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var same bool
	err = tx.QueryRow(ctx, `
		SELECT a.subject = b.subject AND a.predicate = b.predicate
		       AND COALESCE(a.account_id,'') = COALESCE(b.account_id,'')
		       AND COALESCE(a.program_id,'') = COALESCE(b.program_id,'')
		       AND COALESCE(a.asset_id,'') = COALESCE(b.asset_id,'')
		FROM observed_facts a, observed_facts b
		WHERE a.id=$1 AND b.id=$2`, id, by).Scan(&same)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !same {
		return errors.New("postgres: supersede must keep subject, predicate and subject entity")
	}

	var cycle bool
	err = tx.QueryRow(ctx, `
		WITH RECURSIVE chain(id) AS (
			VALUES ($2::text)
			UNION
			SELECT f.superseded_by FROM observed_facts f
			JOIN chain c ON f.id = c.id
			WHERE f.superseded_by IS NOT NULL
		)
		SELECT EXISTS (SELECT 1 FROM chain WHERE id = $1)`, id, by).Scan(&cycle)
	if err != nil {
		return fmt.Errorf("postgres: supersede cycle check: %w", err)
	}
	if cycle {
		return errors.New("postgres: supersede cycle")
	}

	tag, err := tx.Exec(ctx, `UPDATE observed_facts SET superseded_by=$2 WHERE id=$1`, id, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Any derived state that consumed this fact is no longer valid. The state
	// rows keep their inputs for audit; the expiry forces re-derivation.
	if _, err := tx.Exec(ctx, `
		UPDATE derived_states SET valid_until = now()
		WHERE inputs ? $1 AND valid_until > now()`, string(id)); err != nil {
		return fmt.Errorf("postgres: invalidate derived states: %w", err)
	}
	return tx.Commit(ctx)
}

// ---------- Derived state ----------

func (r *Repo) PutState(ctx context.Context, s domain.DerivedState) error {
	if err := s.Validate(); err != nil {
		return err
	}
	state, err := mustJSON(s.State)
	if err != nil {
		return err
	}
	inputs, err := mustJSON(s.Inputs)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO derived_states
			(id, subject, platform, account_id, program_id, state, inputs,
			 observed_at, derived_at, valid_until, confidence, rule_version)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			subject=EXCLUDED.subject, platform=EXCLUDED.platform,
			account_id=EXCLUDED.account_id, program_id=EXCLUDED.program_id,
			state=EXCLUDED.state, inputs=EXCLUDED.inputs,
			observed_at=EXCLUDED.observed_at, derived_at=EXCLUDED.derived_at,
			valid_until=EXCLUDED.valid_until, confidence=EXCLUDED.confidence,
			rule_version=EXCLUDED.rule_version`,
		s.ID, s.Subject, s.Platform, s.AccountID, s.ProgramID, state, inputs,
		s.ObservedAt, s.DerivedAt, s.ValidUntil, s.Confidence, s.RuleVersion)
	if err != nil {
		return fmt.Errorf("postgres: put state: %w", err)
	}
	return nil
}

// ---------- Decisions ----------

func (r *Repo) GetDecision(ctx context.Context, id domain.DecisionID) (domain.ActionDecision, error) {
	var data []byte
	err := r.Pool.QueryRow(ctx, `SELECT data FROM action_decisions WHERE id=$1`, id).Scan(&data)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.ActionDecision{}, ErrNotFound
		}
		return domain.ActionDecision{}, fmt.Errorf("postgres: get decision: %w", err)
	}
	var d domain.ActionDecision
	if err := json.Unmarshal(data, &d); err != nil {
		return domain.ActionDecision{}, fmt.Errorf("postgres: decode decision: %w", err)
	}
	return d, nil
}

// ListStates returns the freshest valid state per program for a subject.
func (r *Repo) ListStates(ctx context.Context, platform domain.Platform, subject domain.StateSubject) ([]domain.DerivedState, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT DISTINCT ON (program_id)
		       id, subject, platform, account_id, program_id, state, inputs,
		       observed_at, derived_at, valid_until, confidence, rule_version
		FROM derived_states
		WHERE ($1 = '' OR platform = $1) AND subject = $2 AND valid_until > now()
		ORDER BY program_id, derived_at DESC`, string(platform), string(subject))
	if err != nil {
		return nil, fmt.Errorf("postgres: list states: %w", err)
	}
	defer rows.Close()
	var out []domain.DerivedState
	for rows.Next() {
		var s domain.DerivedState
		var subj, plat string
		var accountID, programID *string
		var stateJSON, inputsJSON []byte
		if err := rows.Scan(&s.ID, &subj, &plat, &accountID, &programID, &stateJSON, &inputsJSON,
			&s.ObservedAt, &s.DerivedAt, &s.ValidUntil, &s.Confidence, &s.RuleVersion); err != nil {
			return nil, err
		}
		s.Subject = domain.StateSubject(subj)
		s.Platform = domain.Platform(plat)
		if accountID != nil {
			v := domain.AccountID(*accountID)
			s.AccountID = &v
		}
		if programID != nil {
			v := domain.ProgramID(*programID)
			s.ProgramID = &v
		}
		var stateStr string
		if err := json.Unmarshal(stateJSON, &stateStr); err == nil {
			s.State = stateStr
		} else {
			s.State = string(stateJSON)
		}
		_ = json.Unmarshal(inputsJSON, &s.Inputs)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repo) PutDecision(ctx context.Context, d domain.ActionDecision) error {
	if err := d.Validate(); err != nil {
		return err
	}
	reasons, err := mustJSON(d.Reasons)
	if err != nil {
		return err
	}
	gates, err := mustJSON(d.GatesEvaluated)
	if err != nil {
		return err
	}
	approval, err := mustJSON(d.HumanApproval)
	if err != nil {
		return err
	}
	constraints, err := mustJSON(d.Constraints)
	if err != nil {
		return err
	}
	data, err := mustJSON(d)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO action_decisions
			(id, request_id, program_id, action, decision, reasons, gates_evaluated,
			 policy_version, decided_at, human_approval, expires_at, constraints, data)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10::jsonb,$11,$12::jsonb,$13::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		d.ID, d.RequestID, d.ProgramID, d.Action, d.Decision, reasons, gates,
		d.PolicyVersion, d.DecidedAt, approval, d.ExpiresAt, constraints, data)
	if err != nil {
		return fmt.Errorf("postgres: put decision: %w", err)
	}
	return nil
}

// ---------- Opportunity / task / finding / report / submission / ledger ----------

func (r *Repo) PutOpportunity(ctx context.Context, o domain.Opportunity) error {
	data, err := mustJSON(o)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO opportunities
			(id, program_id, asset_id, expected_return, expected_return_per_cost,
			 competition_penalty, confidence, evaluated_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
		ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data, evaluated_at=EXCLUDED.evaluated_at`,
		o.ID, o.ProgramID, o.AssetID, o.ExpectedReturn, o.ExpectedReturnPerCost,
		o.CompetitionPenalty, o.Confidence, o.EvaluatedAt, data)
	return err
}

func (r *Repo) PutResearchTask(ctx context.Context, t domain.ResearchTask) error {
	data, err := mustJSON(t)
	if err != nil {
		return err
	}
	var budget *int64
	var currency *string
	if t.Budget != nil {
		budget = &t.Budget.Amount
		currency = &t.Budget.Currency
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO research_tasks
			(id, opportunity_id, program_id, asset_id, status, budget_cents, currency,
			 created_at, expires_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
		ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`,
		t.ID, t.OpportunityID, t.ProgramID, t.AssetID, t.Status, budget, currency,
		t.CreatedAt, t.ExpiresAt, data)
	return err
}

func (r *Repo) PutFinding(ctx context.Context, f domain.Finding) error {
	data, err := mustJSON(f)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO findings
			(id, task_id, program_id, asset_id, title, vuln_type, severity, status, created_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
		ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`,
		f.ID, f.TaskID, f.ProgramID, f.AssetID, f.Title, f.VulnType, f.Severity,
		f.Status, f.CreatedAt, data)
	return err
}

func (r *Repo) PutReportDraft(ctx context.Context, d domain.ReportDraft) error {
	data, err := mustJSON(d)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO report_drafts (id, finding_id, program_id, status, created_at, data)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)
		ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`,
		d.ID, d.FindingID, d.ProgramID, d.Status, d.CreatedAt, data)
	return err
}

func (r *Repo) PutSubmission(ctx context.Context, s domain.Submission) error {
	data, err := mustJSON(s)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO submissions
			(id, report_id, program_id, submitted_at, platform_url, receipt, status, approval_id, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
		ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`,
		s.ID, s.ReportID, s.ProgramID, s.SubmittedAt, s.PlatformURL, s.Receipt,
		s.Status, s.ApprovalID, data)
	return err
}

func (r *Repo) PutLedgerEntry(ctx context.Context, e domain.LedgerEntry) error {
	data, err := mustJSON(e)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO ledger_entries
			(id, platform, program_id, finding_id, submission_id, kind, amount_cents,
			 currency, status, occurred_at, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Platform, e.ProgramID, e.FindingID, e.SubmissionID, e.Kind,
		e.Amount.Amount, e.Amount.Currency, e.Status, e.OccurredAt, data)
	return err
}
