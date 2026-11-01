package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/Zn070515/BountyGrid/internal/state"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type deriveOptions struct {
	Platform  string
	ProgramID string
	TTL       time.Duration
	Workers   int
}

// deriveStore is the PostgreSQL subset needed to rebuild derived states.
type deriveStore interface {
	ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error)
	ListFactsByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error)
	ListFactsByPlatform(ctx context.Context, platform domain.Platform) ([]domain.ObservedFact, error)
	PutStates(ctx context.Context, states []domain.DerivedState) error
}

type deriveOutput struct {
	Platform  string         `json:"platform"`
	Programs  int            `json:"programs"`
	States    int            `json:"states"`
	Skipped   int            `json:"skipped"`
	BySubject map[string]int `json:"by_subject"`
	Errors    []string       `json:"errors,omitempty"`
}

func parseDeriveOptions(args []string) (deriveOptions, error) {
	var o deriveOptions
	fs := flag.NewFlagSet("derive", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Platform, "platform", "vulbox", "platform to derive (vulbox/xianzhi/butian); empty = all")
	fs.StringVar(&o.ProgramID, "program", "", "derive only this full program id (e.g. vulbox/444)")
	fs.DurationVar(&o.TTL, "ttl", 30*time.Minute, "derived state TTL")
	fs.IntVar(&o.Workers, "workers", defaultDeriveWorkers(), "parallel derivation workers (1 = sequential)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.TTL <= 0 {
		return o, fmt.Errorf("derive: -ttl must be positive")
	}
	if o.Workers <= 0 {
		return o, fmt.Errorf("derive: -workers must be positive")
	}
	return o, nil
}

func defaultDeriveWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

func runDerive(ctx context.Context, args []string) error {
	opts, err := parseDeriveOptions(args)
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
	out, err := executeDerive(ctx, repo, opts, time.Now().UTC())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func executeDerive(ctx context.Context, store deriveStore, opts deriveOptions, now time.Time) (deriveOutput, error) {
	out := deriveOutput{Platform: opts.Platform, BySubject: map[string]int{}}
	progs, err := store.ListPrograms(ctx, domain.Platform(opts.Platform))
	if err != nil {
		return out, err
	}

	factsByProgram := map[domain.ProgramID][]domain.ObservedFact{}
	if opts.ProgramID != "" {
		fsList, err := store.ListFactsByProgram(ctx, domain.ProgramID(opts.ProgramID))
		if err != nil {
			return out, err
		}
		factsByProgram[domain.ProgramID(opts.ProgramID)] = fsList
	} else {
		all, err := store.ListFactsByPlatform(ctx, domain.Platform(opts.Platform))
		if err != nil {
			return out, err
		}
		for _, f := range all {
			if f.ProgramID != nil {
				factsByProgram[*f.ProgramID] = append(factsByProgram[*f.ProgramID], f)
			}
		}
	}

	var work []domain.Program
	for _, p := range progs {
		if opts.ProgramID != "" && string(p.ID) != opts.ProgramID {
			continue
		}
		work = append(work, p)
	}
	out.Programs = len(work)

	results := make([]deriveResult, len(work))
	workers := opts.Workers
	if workers > len(work) {
		workers = len(work)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := ctx.Err(); err != nil {
					results[idx].errors = []string{err.Error()}
					results[idx].skipped = true
					continue
				}
				results[idx] = deriveOne(work[idx], factsByProgram[work[idx].ID], now, opts.TTL)
			}
		}()
	}
	for i := range work {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}

	var states []domain.DerivedState
	for _, r := range results {
		out.Errors = append(out.Errors, r.errors...)
		if r.skipped {
			out.Skipped++
		}
		for subject, n := range r.bySubject {
			out.BySubject[subject] += n
		}
		states = append(states, r.states...)
	}
	if err := store.PutStates(ctx, states); err != nil {
		return out, fmt.Errorf("derive: persist states: %w", err)
	}
	out.States = len(states)
	return out, nil
}

type deriveResult struct {
	states    []domain.DerivedState
	bySubject map[string]int
	errors    []string
	skipped   bool
}

func deriveOne(p domain.Program, fsList []domain.ObservedFact, now time.Time, ttl time.Duration) deriveResult {
	r := deriveResult{bySubject: map[string]int{}}
	if len(fsList) == 0 {
		r.skipped = true
		return r
	}
	in, err := state.ProgramInputFromFacts(facts.NewFactSet(fsList))
	if err != nil {
		r.errors = []string{fmt.Sprintf("%s: %v", p.ID, err)}
		r.skipped = true
		return r
	}
	st, err := state.DeriveProgramState(in, now, ttl)
	if err != nil {
		r.errors = []string{fmt.Sprintf("%s: %v", p.ID, err)}
		r.skipped = true
		return r
	}
	pid := p.ID
	add := func(subject domain.StateSubject, suffix, val string) error {
		ds := domain.DerivedState{
			ID:          domain.StateID(fmt.Sprintf("state/%s/%s", p.ID, suffix)),
			Subject:     subject,
			Platform:    p.Platform,
			ProgramID:   &pid,
			State:       val,
			Inputs:      in.FactIDs,
			ObservedAt:  st.ObservedAt,
			DerivedAt:   now,
			ValidUntil:  st.ValidUntil,
			Confidence:  st.Confidence,
			RuleVersion: "state/derive@0.1.0",
		}
		if err := ds.Validate(); err != nil {
			return err
		}
		r.states = append(r.states, ds)
		r.bySubject[string(subject)]++
		return nil
	}
	if err := add(domain.StateProgramState, "program_status", string(st.Status)); err != nil {
		r.errors = []string{fmt.Sprintf("%s: %v", p.ID, err)}
		r.skipped = true
		return r
	}
	if sv, err := state.DeriveScopeVisibility(in); err == nil {
		if err := add(domain.StateScopeVisibility, "scope_visibility", string(sv)); err != nil {
			r.errors = append(r.errors, fmt.Sprintf("%s: %v", p.ID, err))
		}
	}
	if open, err := state.DeriveRegistrationOpen(in, now); err == nil {
		val := "CLOSED"
		if open {
			val = "OPEN"
		}
		if err := add(domain.StateRegistration, "registration", val); err != nil {
			r.errors = append(r.errors, fmt.Sprintf("%s: %v", p.ID, err))
		}
	}
	return r
}
