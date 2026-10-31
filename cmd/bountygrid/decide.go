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
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/policy"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type decideOptions struct {
	RequestFile string
	Authorize   string
}

type decideInput struct {
	Request       domain.ActionRequest  `json:"request"`
	Asset         *domain.Asset         `json:"asset,omitempty"`
	ResourceID    string                `json:"resource_id,omitempty"`
	HumanApproval *domain.HumanApproval `json:"human_approval,omitempty"`
	UsageToday    map[string]int64      `json:"usage_today,omitempty"`
	BudgetSpent   *domain.Money         `json:"budget_spent,omitempty"`
	Now           *time.Time            `json:"now,omitempty"`
}

func parseDecideOptions(args []string) (decideOptions, error) {
	var o decideOptions
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.RequestFile, "request", "", "JSON file with request/asset/approval")
	fs.StringVar(&o.Authorize, "authorize", "", "re-check an ALLOW decision id before execution")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.RequestFile == "" && o.Authorize == "" {
		return o, fmt.Errorf("decide: provide -request or -authorize")
	}
	if o.RequestFile != "" && o.Authorize != "" {
		return o, fmt.Errorf("decide: use -request or -authorize, not both")
	}
	return o, nil
}

func runDecide(ctx context.Context, args []string) error {
	opts, err := parseDecideOptions(args)
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
	engine := policy.NewEngine()
	engine.SetApprovalLedger(postgres.NewPostgresApprovalLedger(pool))
	g := gateway.New(repo, engine, postgres.NewAuditStore(pool), nil)

	if opts.Authorize != "" {
		if err := g.Authorize(ctx, domain.DecisionID(opts.Authorize)); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"authorized": true, "decision_id": opts.Authorize})
	}

	raw, err := os.ReadFile(opts.RequestFile)
	if err != nil {
		return fmt.Errorf("decide: read request: %w", err)
	}
	var in decideInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("decide: decode request: %w", err)
	}
	now := time.Now().UTC()
	if in.Now != nil {
		now = in.Now.UTC()
	}
	decision, err := g.Evaluate(ctx, gateway.EvaluateInput{
		Request: in.Request, Asset: in.Asset, ResourceID: in.ResourceID,
		HumanApproval: in.HumanApproval, UsageToday: in.UsageToday,
		BudgetSpent: in.BudgetSpent, Now: now,
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(decision)
}
