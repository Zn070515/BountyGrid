package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/authz"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/gateway"
	"github.com/Zn070515/BountyGrid/internal/policy"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type decideOptions struct {
	RequestFile      string
	Authorize        string
	ContextFile      string
	RulesFile        string
	PlatformEvidence []string
	ContentRoot      string
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
	var platformEvidence string
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.RequestFile, "request", "", "JSON file with request (AssetID only), approval, usage and execution_environment")
	fs.StringVar(&o.Authorize, "authorize", "", "re-check an ALLOW decision id before execution")
	fs.StringVar(&o.ContextFile, "context", "", "with -authorize: JSON file with fresh usage_today/budget_spent/execution_environment")
	fs.StringVar(&o.RulesFile, "rules", "", "optional JSON rule-set overlay")
	fs.StringVar(&platformEvidence, "platform-evidence", "", "comma-separated POLICY/ANNOUNCEMENT evidence ids compiled into platform authority")
	fs.StringVar(&o.ContentRoot, "content-root", ".", "root directory containing redacted evidence files")
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
	for _, part := range strings.Split(platformEvidence, ",") {
		if part = strings.TrimSpace(part); part != "" {
			o.PlatformEvidence = append(o.PlatformEvidence, part)
		}
	}
	o.ContentRoot = strings.TrimSpace(o.ContentRoot)
	if o.ContentRoot == "" {
		return o, fmt.Errorf("decide: -content-root must not be blank")
	}
	return o, nil
}

// platformAuthorityStore is the narrow persistence surface needed to compile
// platform authority: evidence lookup plus the program whose policy revision
// and freshness window the permissions bind to.
type platformAuthorityStore interface {
	authz.EvidenceStore
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
}

// attachPlatformAuthority compiles explicit PERMISSION lines from Evidence and
// attaches the verified policy to the engine. It returns an error instead of
// silently evaluating without the authority the caller requested.
func attachPlatformAuthority(ctx context.Context, store platformAuthorityStore, engine *policy.Engine, programID domain.ProgramID, evidenceIDs []string, contentRoot string, now time.Time) error {
	program, err := store.GetProgram(ctx, programID)
	if err != nil {
		return fmt.Errorf("decide: load program %s: %w", programID, err)
	}
	contents, err := authz.NewFileContentReader(contentRoot)
	if err != nil {
		return fmt.Errorf("decide: platform evidence: %w", err)
	}
	ids := make([]domain.EvidenceID, 0, len(evidenceIDs))
	for _, id := range evidenceIDs {
		ids = append(ids, domain.EvidenceID(id))
	}
	_, pp, err := compilePlatformAuthority(ctx, store, contents, program, ids, now)
	if err != nil {
		return fmt.Errorf("decide: platform evidence: %w", err)
	}
	engine.SetPlatformPolicy(pp)
	return nil
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
		if len(opts.PlatformEvidence) > 0 {
			// The decision records the fingerprint of the authority it was made
			// under. Recompile the same authority before AuthorizeWith, otherwise
			// the fingerprint check rejects an otherwise valid decision.
			stored, err := repo.GetDecision(ctx, domain.DecisionID(opts.Authorize))
			if err != nil {
				return fmt.Errorf("decide: load decision %s: %w", opts.Authorize, err)
			}
			if stored.Request.ProgramID == "" {
				return fmt.Errorf("decide: decision %s has no stored request; cannot bind platform evidence", opts.Authorize)
			}
			now := ai.Now
			if now.IsZero() {
				now = time.Now().UTC()
			}
			if err := attachPlatformAuthority(ctx, repo, engine, stored.Request.ProgramID, opts.PlatformEvidence, opts.ContentRoot, now); err != nil {
				return err
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
	// Platform authority is compiled from Evidence at decision time, never from
	// a rule file. The flag is opt-in, and every permission still has to survive
	// authz.BuildPlatformPolicy; without it the decision uses the same
	// conservative ceiling as before.
	if len(opts.PlatformEvidence) > 0 {
		if err := attachPlatformAuthority(ctx, repo, engine, in.Request.ProgramID, opts.PlatformEvidence, opts.ContentRoot, now); err != nil {
			return err
		}
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
