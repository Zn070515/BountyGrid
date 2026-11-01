package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/capability"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/harness"
	"github.com/Zn070515/BountyGrid/internal/policy"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type harnessOptions struct {
	Tool         string
	RequestFile  string
	BudgetCNY    int64
	AllowedHosts string
	Workspace    string
	Destructive  bool
	RulesFile    string
}

func parseHarnessOptions(args []string) (harnessOptions, error) {
	var o harnessOptions
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Tool, "tool", "view_rules", "registered tool name")
	fs.StringVar(&o.RequestFile, "request", "", "JSON file with the ActionRequest and context")
	fs.Int64Var(&o.BudgetCNY, "budget", 100, "maximum tool cost in CNY")
	fs.StringVar(&o.AllowedHosts, "allowed-hosts", "", "comma-separated egress allowlist")
	fs.StringVar(&o.Workspace, "workspace", "", "directory root for file tools (os.Root-confined)")
	fs.BoolVar(&o.Destructive, "allow-destructive", false, "allow tools marked destructive")
	fs.StringVar(&o.RulesFile, "rules", "", "optional JSON rule-set overlay")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.RequestFile == "" {
		return o, errors.New("harness: -request is required")
	}
	if strings.TrimSpace(o.Tool) == "" {
		return o, errors.New("harness: -tool is empty")
	}
	return o, nil
}

func runHarness(ctx context.Context, args []string) error {
	opts, err := parseHarnessOptions(args)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(opts.RequestFile)
	if err != nil {
		return fmt.Errorf("harness: read request: %w", err)
	}
	var in decideInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("harness: decode request: %w", err)
	}
	now := time.Now().UTC()
	if in.Now != nil {
		now = in.Now.UTC()
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

	hosts := []string{}
	for _, h := range strings.Split(opts.AllowedHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	h := harness.New(harness.NewBuiltinRegistry(), g, postgres.NewAuditStore(pool), &harness.Sandbox{AllowedHosts: hosts},
		&harness.Budget{MaxCost: domain.Money{Amount: opts.BudgetCNY, Currency: "CNY"}})
	h.SetAllowDestructive(opts.Destructive)
	h.SetReceiptStore(repo)
	// Daily HTTP usage is business authorization state: use the durable
	// PostgreSQL counter, not a per-process counter that resets on restart.
	h.SetBroker(capability.NewMemoryBroker(
		capability.WithDailyQuota(capability.PostgresDailyQuota{Store: repo}),
	))
	if opts.Workspace != "" {
		if err := h.SetWorkspace(opts.Workspace); err != nil {
			return err
		}
		defer func() { _ = h.Close() }()
	}

	decision, res, err := h.Execute(ctx, opts.Tool, gateway.EvaluateInput{
		Request: in.Request, ResourceID: in.ResourceID,
		HumanApproval: in.HumanApproval, UsageToday: in.UsageToday,
		BudgetSpent: in.BudgetSpent, Environment: in.Environment, Now: now,
	})
	var denied harness.DeniedError
	if errors.As(err, &denied) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(denied.Decision)
	}
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"tool": opts.Tool, "decision": decision.Decision, "decision_id": decision.ID,
		"output": res.Output, "evidence": res.Evidence,
	})
}
