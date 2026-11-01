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
	ContextFile string
	RulesFile   string
}

type decideInput struct {
	Request       domain.ActionRequest         `json:"request"`
	ResourceID    string                       `json:"resource_id,omitempty"`
	HumanApproval *domain.HumanApproval        `json:"human_approval,omitempty"`
	UsageToday    map[string]int64             `json:"usage_today,omitempty"`
	BudgetSpent   *domain.Money                `json:"budget_spent,omitempty"`
	Environment   *domain.ExecutionEnvironment `json:"execution_environment,omitempty"`
	Now           *time.Time                   `json:"now,omitempty"`
}

func parseDecideOptions(args []string) (decideOptions, error) {
	var o decideOptions
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.RequestFile, "request", "", "JSON file with request (AssetID only), approval, usage and execution_environment")
	fs.StringVar(&o.Authorize, "authorize", "", "re-check an ALLOW decision id before execution")
	fs.StringVar(&o.ContextFile, "context", "", "with -authorize: JSON file with fresh usage_today/budget_spent/execution_environment")
	fs.StringVar(&o.RulesFile, "rules", "", "optional JSON rule-set overlay")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.RequestFile == "" && o.Authorize == "" {
		return o, fmt.Errorf("decide: provide -request or -authorize")
	}
	if o.RequestFile != "" && o.Authorize != "" {
		return o, fmt.Errorf("decide: use -request or -authorize, not both")
	}
	if o.ContextFile != "" && o.Authorize == "" {
		return o, fmt.Errorf("decide: -context is only valid with -authorize")
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
	rs, err := loadOptionalRules(opts.RulesFile)
	if err != nil {
		return err
	}
	engine := policy.NewEngineWithRules(rs)
	g := gateway.New(repo, engine, postgres.NewAuditStore(pool), postgres.NewPostgresApprovalLedger(pool))

	if opts.Authorize != "" {
		ai := gateway.AuthorizeInput{}
		if opts.ContextFile != "" {
			in, err := loadDecideInput(opts.ContextFile)
			if err != nil {
				return err
			}
			ai.UsageToday = in.UsageToday
			ai.BudgetSpent = in.BudgetSpent
			ai.Environment = in.Environment
			if in.Now != nil {
				ai.Now = in.Now.UTC()
			}
		}
		if err := g.AuthorizeWith(ctx, domain.DecisionID(opts.Authorize), ai); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"authorized": true, "decision_id": opts.Authorize})
	}

	in, err := loadDecideInput(opts.RequestFile)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if in.Now != nil {
		now = in.Now.UTC()
	}
	decision, err := g.Evaluate(ctx, gateway.EvaluateInput{
		Request: in.Request, ResourceID: in.ResourceID,
		HumanApproval: in.HumanApproval, UsageToday: in.UsageToday,
		BudgetSpent: in.BudgetSpent, Environment: in.Environment, Now: now,
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(decision)
}

func loadDecideInput(path string) (decideInput, error) {
	var in decideInput
	raw, err := os.ReadFile(path)
	if err != nil {
		return in, fmt.Errorf("decide: read input: %w", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("decide: decode input: %w", err)
	}
	return in, nil
}
