package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
}

// deriveStore is the PostgreSQL subset needed to rebuild derived states.
type deriveStore interface {
	ListPrograms(ctx context.Context, platform domain.Platform) ([]domain.Program, error)
	ListFactsByProgram(ctx context.Context, id domain.ProgramID) ([]domain.ObservedFact, error)
	PutState(ctx context.Context, s domain.DerivedState) error
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
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.TTL <= 0 {
		return o, fmt.Errorf("derive: -ttl must be positive")
	}
	return o, nil
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
	for _, p := range progs {
		if opts.ProgramID != "" && string(p.ID) != opts.ProgramID {
			continue
		}
		out.Programs++
		fsList, err := store.ListFactsByProgram(ctx, p.ID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			out.Skipped++
			continue
		}
		if len(fsList) == 0 {
			out.Skipped++
			continue
		}
		in, err := state.ProgramInputFromFacts(facts.NewFactSet(fsList))
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			out.Skipped++
			continue
		}
		st, err := state.DeriveProgramState(in, now, opts.TTL)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			out.Skipped++
			continue
		}
		put := func(subject domain.StateSubject, suffix, val string) error {
			ds := domain.DerivedState{
				ID:          domain.StateID(fmt.Sprintf("state/%s/%s", p.ID, suffix)),
				Subject:     subject,
				Platform:    p.Platform,
				ProgramID:   &p.ID,
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
			if err := store.PutState(ctx, ds); err != nil {
				return err
			}
			out.States++
			out.BySubject[string(subject)]++
			return nil
		}
		if err := put(domain.StateProgramState, "program_status", string(st.Status)); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			out.Skipped++
			continue
		}
		if sv, err := state.DeriveScopeVisibility(in); err == nil {
			if err := put(domain.StateScopeVisibility, "scope_visibility", string(sv)); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			}
		}
		if open, err := state.DeriveRegistrationOpen(in, now); err == nil {
			val := "CLOSED"
			if open {
				val = "OPEN"
			}
			if err := put(domain.StateRegistration, "registration", val); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			}
		}
	}
	return out, nil
}
