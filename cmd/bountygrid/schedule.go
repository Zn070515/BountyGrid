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
	"github.com/Zn070515/BountyGrid/internal/ledger"
	"github.com/Zn070515/BountyGrid/internal/opportunity"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type scheduleOptions struct {
	Platform string
	Top      int
	Persist  bool
}

type scheduleStore interface {
	ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error)
	ListStates(ctx context.Context, platform domain.Platform, subject domain.StateSubject) ([]domain.DerivedState, error)
	GetAccount(ctx context.Context, id domain.AccountID) (domain.Account, error)
	GetLatestCompetitionSnapshot(ctx context.Context, platform domain.Platform, scope string) (domain.CompetitionSnapshot, error)
	LatestResearchTaskTimes(ctx context.Context, platform domain.Platform) (map[domain.ProgramID]time.Time, error)
	PutOpportunity(ctx context.Context, o domain.Opportunity) error
	PutResearchTask(ctx context.Context, t domain.ResearchTask) error
	ListLedgerEntries(ctx context.Context, platform domain.Platform, programID domain.ProgramID, since, until *time.Time) ([]domain.LedgerEntry, error)
}

type scheduleOutput struct {
	Platform        string             `json:"platform"`
	Persisted       bool               `json:"persisted"`
	Evaluated       int                `json:"evaluated"`
	Research        int                `json:"research"`
	Human           int                `json:"human"`
	Excluded        int                `json:"excluded"`
	NoState         int                `json:"no_state"`
	ResearchQueue   []scheduledSummary `json:"research_queue,omitempty"`
	HumanQueue      []scheduledSummary `json:"human_queue,omitempty"`
	ExcludedReasons map[string]int     `json:"excluded_reasons,omitempty"`
}

type scheduledSummary struct {
	ProgramID          string   `json:"program_id"`
	Name               string   `json:"name"`
	Type               string   `json:"type"`
	Status             string   `json:"status"`
	Eligibility        string   `json:"eligibility"`
	ScopeAccess        string   `json:"scope_access"`
	Reason             string   `json:"reason"`
	Score              float64  `json:"score"`
	ExpectedReturn     float64  `json:"expected_return"`
	CompetitionPenalty float64  `json:"competition_penalty"`
	Confidence         float64  `json:"confidence"`
	Inputs             []string `json:"inputs,omitempty"`
}

func parseScheduleOptions(args []string) (scheduleOptions, error) {
	var o scheduleOptions
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Platform, "platform", "vulbox", "platform to schedule (vulbox/xianzhi/butian)")
	fs.IntVar(&o.Top, "top", 5, "maximum research tasks")
	fs.BoolVar(&o.Persist, "persist", false, "write Opportunities and ResearchTasks to PostgreSQL")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.Platform == "" {
		return o, errors.New("schedule: -platform is empty")
	}
	if o.Top <= 0 {
		return o, errors.New("schedule: -top must be positive")
	}
	return o, nil
}

func runSchedule(ctx context.Context, args []string) error {
	opts, err := parseScheduleOptions(args)
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
	out, err := executeSchedule(ctx, postgres.NewRepo(pool), opts, time.Now().UTC())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func executeSchedule(ctx context.Context, store scheduleStore, opts scheduleOptions, now time.Time) (scheduleOutput, error) {
	out := scheduleOutput{
		Platform: opts.Platform, Persisted: opts.Persist,
		ExcludedReasons: map[string]int{},
	}
	progs, err := store.ListPrograms(ctx, domain.Platform(opts.Platform))
	if err != nil {
		return out, err
	}
	states, err := store.ListStates(ctx, domain.Platform(opts.Platform), domain.StateProgramState)
	if err != nil {
		return out, err
	}
	stateByProgram := make(map[domain.ProgramID]domain.DerivedState, len(states))
	for _, s := range states {
		if s.ProgramID != nil {
			stateByProgram[*s.ProgramID] = s
		}
	}
	acct := domain.Account{
		ID: domain.AccountID(opts.Platform + "/account"), Platform: domain.Platform(opts.Platform), ObservedAt: now,
	}
	if a, err := store.GetAccount(ctx, acct.ID); err == nil {
		acct = a
	} else if !errors.Is(err, postgres.ErrNotFound) {
		return out, err
	}
	var comp *domain.CompetitionSnapshot
	if c, err := store.GetLatestCompetitionSnapshot(ctx, domain.Platform(opts.Platform), "PLATFORM"); err == nil {
		comp = &c
	} else if !errors.Is(err, postgres.ErrNotFound) {
		return out, err
	}

	existing, err := store.LatestResearchTaskTimes(ctx, domain.Platform(opts.Platform))
	if err != nil {
		return out, err
	}
	history, err := loadHistory(ctx, store, domain.Platform(opts.Platform))
	if err != nil {
		return out, err
	}

	var candidates []opportunity.Candidate
	for _, p := range progs {
		st, ok := stateByProgram[p.ID]
		if !ok || !domain.ProgramStatus(st.State).Valid() {
			out.NoState++
			out.ExcludedReasons["NO_STATE"]++
			continue
		}
		candidates = append(candidates, opportunity.Candidate{
			Program: p, Status: domain.ProgramStatus(st.State), Account: acct,
			Competition: comp, History: history[p.ID],
		})
	}
	res := opportunity.Schedule(opportunity.NewEngine(), candidates, opportunity.Options{
		TopK: opts.Top, MaxPerPlatform: 2, MaxPerProgram: 1,
		Existing: existing, ReserveEnterprise: true, Now: now,
	})
	out.Evaluated = res.Evaluated
	out.Research = res.ResearchCount
	out.Human = res.HumanCount
	out.Excluded = res.ExcludedCount + out.NoState
	for _, e := range res.Excluded {
		out.ExcludedReasons[e.Reason]++
	}
	for _, s := range res.ResearchQueue {
		out.ResearchQueue = append(out.ResearchQueue, summarizeScheduled(s))
	}
	humanLimit := opts.Top * 2
	for i, s := range res.HumanQueue {
		if i >= humanLimit {
			break
		}
		out.HumanQueue = append(out.HumanQueue, summarizeScheduled(s))
	}
	if opts.Persist {
		runID := now.UTC().Format("20060102T150405.000000000")
		for _, s := range res.ResearchQueue {
			if err := persistScheduled(ctx, store, s, "QUEUED", now, runID); err != nil {
				return out, err
			}
		}
		for _, s := range res.HumanQueue {
			if err := persistScheduled(ctx, store, s, "HUMAN_ACTION_REQUIRED", now, runID); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// loadHistory closes the ledger -> opportunity loop: actual acceptance and
// payout history for each program is fed back into the score. An empty ledger
// yields an empty map (no fabricated history).
func loadHistory(ctx context.Context, store scheduleStore, platform domain.Platform) (map[domain.ProgramID]*opportunity.History, error) {
	entries, err := store.ListLedgerEntries(ctx, platform, "", nil, nil)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return map[domain.ProgramID]*opportunity.History{}, nil
	}
	feedback, err := ledger.Feedback(entries)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.ProgramID]*opportunity.History, len(feedback))
	for _, f := range feedback {
		out[f.ProgramID] = &opportunity.History{
			Submitted: f.Submitted, Accepted: f.Accepted, Paid: f.PaidCount,
			AvgAccepted: f.AvgAccepted, AvgPaid: f.AvgPaid,
		}
	}
	return out, nil
}

func persistScheduled(ctx context.Context, store scheduleStore, s opportunity.Scheduled, status string, now time.Time, runID string) error {
	if err := store.PutOpportunity(ctx, s.Opportunity); err != nil {
		return fmt.Errorf("schedule: persist opportunity %s: %w", s.Opportunity.ID, err)
	}
	expires := now.Add(24 * time.Hour)
	task := domain.ResearchTask{
		ID:            domain.TaskID(fmt.Sprintf("task/%s/%s", s.Program.ID, runID)),
		OpportunityID: s.Opportunity.ID,
		ProgramID:     s.Program.ID,
		Status:        status,
		CreatedAt:     now,
		ExpiresAt:     &expires,
		ScopeSnapshot: s.Program.Scope,
		Constraints:   s.Program.Constraints,
	}
	if err := store.PutResearchTask(ctx, task); err != nil {
		return fmt.Errorf("schedule: persist task %s: %w", task.ID, err)
	}
	return nil
}

func summarizeScheduled(s opportunity.Scheduled) scheduledSummary {
	out := scheduledSummary{
		ProgramID: string(s.Program.ID), Name: s.Program.Name, Type: string(s.Program.Type),
		Status: string(s.Program.Status.Status), Eligibility: string(s.Program.Eligibility.Decision),
		ScopeAccess: string(s.Program.Scope.Access), Reason: s.Reason,
		CompetitionPenalty: s.Opportunity.CompetitionPenalty, Confidence: float64(s.Opportunity.Confidence),
		Inputs: s.Opportunity.Inputs,
	}
	if s.Opportunity.ExpectedReturnPerCost != nil {
		out.Score = *s.Opportunity.ExpectedReturnPerCost
	}
	if s.Opportunity.ExpectedReturn != nil {
		out.ExpectedReturn = *s.Opportunity.ExpectedReturn
	}
	return out
}
