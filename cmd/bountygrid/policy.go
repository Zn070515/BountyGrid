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

	"github.com/Zn070515/BountyGrid/internal/authz"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type policyOptions struct {
	RulesFile string
	Action    string
	Program   string
	Asset     string
	Method    string
	Path      string
}

type policyCheckOutput struct {
	Version     string              `json:"rule_set_version,omitempty"`
	Fingerprint string              `json:"rule_set_fingerprint,omitempty"`
	Request     rules.Request       `json:"request"`
	Decision    string              `json:"decision,omitempty"`
	CanRelax    bool                `json:"can_relax,omitempty"`
	Matched     []rules.MatchedRule `json:"matched_rules,omitempty"`
}

func parsePolicyOptions(args []string) (policyOptions, error) {
	var o policyOptions
	fs := flag.NewFlagSet("policy check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.RulesFile, "rules", "", "JSON rule-set file")
	fs.StringVar(&o.Action, "action", "", "requested action, e.g. ACTIVE_HTTP")
	fs.StringVar(&o.Program, "program", "", "program ID, e.g. vulbox/444")
	fs.StringVar(&o.Asset, "asset", "", "asset URL or host")
	fs.StringVar(&o.Method, "method", "", "HTTP method")
	fs.StringVar(&o.Path, "path", "", "request path (optional; derived from -asset)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.RulesFile == "" {
		return o, errors.New("policy check: -rules is required")
	}
	if o.Action == "" {
		return o, errors.New("policy check: -action is required")
	}
	return o, nil
}

// loadOptionalRules returns nil when no rule file is configured, so existing
// command behavior is unchanged unless the operator opts in.
func loadOptionalRules(name string) (*rules.RuleSet, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	return rules.LoadFile(name)
}

func executePolicyCheck(o policyOptions) (policyCheckOutput, error) {
	rs, err := rules.LoadFile(o.RulesFile)
	if err != nil {
		return policyCheckOutput{}, err
	}
	req := rules.Request{
		Action:  domain.Action(o.Action),
		Program: domain.ProgramID(o.Program),
		Asset:   o.Asset,
		Method:  o.Method,
		Path:    o.Path,
	}
	res, err := rs.Evaluate(req)
	if err != nil {
		return policyCheckOutput{}, fmt.Errorf("policy check: %w", err)
	}
	return policyCheckOutput{
		Version: rs.Version, Fingerprint: rs.Fingerprint(), Request: req,
		Decision: string(res.Decision), CanRelax: res.CanRelax, Matched: res.Matched,
	}, nil
}

func runPolicy(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("policy: expected 'check' or 'compile' (usage: bountygrid policy check -rules <file> -action <action>; bountygrid policy compile -program <id> [-evidence <ids>])")
	}
	switch args[0] {
	case "check":
		opts, err := parsePolicyOptions(args[1:])
		if err != nil {
			return err
		}
		out, err := executePolicyCheck(opts)
		if err != nil {
			return err
		}
		return encodePolicyOutput(out)
	case "compile":
		opts, err := parsePolicyCompileOptions(args[1:])
		if err != nil {
			return err
		}
		return executePolicyCompile(ctx, opts)
	default:
		return fmt.Errorf("policy: unknown subcommand %q (expected 'check' or 'compile')", args[0])
	}
}

func encodePolicyOutput(out any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

type policyCompileOptions struct {
	Program     string
	Evidence    []string
	ContentRoot string
	Now         time.Time
}

type policyCompiledPermission struct {
	ID           string    `json:"id"`
	EvidenceID   string    `json:"evidence_id"`
	Action       string    `json:"action"`
	Hosts        []string  `json:"hosts"`
	Methods      []string  `json:"methods"`
	PathPrefixes []string  `json:"path_prefixes,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
	ValidUntil   time.Time `json:"valid_until"`
}

type policyCompileSkip struct {
	EvidenceID string `json:"evidence_id"`
	Reason     string `json:"reason"`
}

type policyCompileOutput struct {
	Program     string                     `json:"program"`
	Platform    string                     `json:"platform"`
	Evidence    []string                   `json:"evidence"`
	Permissions []policyCompiledPermission `json:"permissions"`
	Restricted  []string                   `json:"restricted_quotes,omitempty"`
	Skipped     []policyCompileSkip        `json:"skipped,omitempty"`
	Fingerprint string                     `json:"platform_policy_fingerprint,omitempty"`
	Count       int                        `json:"count"`
}

func parsePolicyCompileOptions(args []string) (policyCompileOptions, error) {
	var o policyCompileOptions
	var evidence, now string
	fs := flag.NewFlagSet("policy compile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Program, "program", "", "program ID, e.g. vulbox/444")
	fs.StringVar(&evidence, "evidence", "", "comma-separated evidence IDs (default: Program.Evidence)")
	fs.StringVar(&o.ContentRoot, "content-root", ".", "root directory containing redacted evidence files")
	fs.StringVar(&now, "now", "", "optional RFC3339 decision time")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.Program = strings.TrimSpace(o.Program)
	if o.Program == "" {
		return o, errors.New("policy compile: -program is required")
	}
	o.ContentRoot = strings.TrimSpace(o.ContentRoot)
	if o.ContentRoot == "" {
		return o, errors.New("policy compile: -content-root must not be blank")
	}
	for _, part := range strings.Split(evidence, ",") {
		if part = strings.TrimSpace(part); part != "" {
			o.Evidence = append(o.Evidence, part)
		}
	}
	if now != "" {
		t, err := time.Parse(time.RFC3339, now)
		if err != nil {
			return o, fmt.Errorf("policy compile: -now: %w", err)
		}
		o.Now = t.UTC()
	}
	return o, nil
}

// compilePlatformAuthority is the DB-free core of `policy compile`. It loads
// each Evidence, reads the redacted content through EvidenceContentReader,
// compiles explicit PERMISSION lines into facts, and lets authz verify them.
// Every failure is returned: authority is never granted by silently skipping an
// unreadable or malformed policy document. Non-authorizing SourceTypes are
// listed as skipped because they cannot carry platform authority at all.
func compilePlatformAuthority(
	ctx context.Context,
	store authz.EvidenceStore,
	contents authz.EvidenceContentReader,
	program domain.Program,
	evidenceIDs []domain.EvidenceID,
	now time.Time,
) (policyCompileOutput, *authz.PlatformPolicy, error) {
	out := policyCompileOutput{Program: string(program.ID), Platform: string(program.Platform)}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	seen := make(map[domain.EvidenceID]struct{}, len(evidenceIDs))
	var permissions []authz.PermissionFact
	for _, id := range evidenceIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ev, err := store.GetEvidence(ctx, id)
		if err != nil {
			return out, nil, fmt.Errorf("policy compile: load evidence %s: %w", id, err)
		}
		out.Evidence = append(out.Evidence, string(id))
		if ev.SourceType != domain.SourcePolicy && ev.SourceType != domain.SourceAnnouncement {
			out.Skipped = append(out.Skipped, policyCompileSkip{
				EvidenceID: string(id),
				Reason:     fmt.Sprintf("source type %s is not POLICY or ANNOUNCEMENT", ev.SourceType),
			})
			continue
		}
		content, err := contents.ReadContent(ctx, ev)
		if err != nil {
			return out, nil, fmt.Errorf("policy compile: read evidence %s: %w", id, err)
		}
		compiled, err := authz.CompilePermissions(program, ev, content)
		if err != nil {
			return out, nil, fmt.Errorf("policy compile: evidence %s: %w", id, err)
		}
		for _, restricted := range compiled.Restricted {
			out.Restricted = append(out.Restricted, restricted.Quote)
		}
		permissions = append(permissions, compiled.Permissions...)
	}
	pp, err := authz.BuildPlatformPolicy(ctx, program, permissions, store, contents, now)
	if err != nil {
		return out, nil, fmt.Errorf("policy compile: %w", err)
	}
	out.Fingerprint = pp.Fingerprint()
	out.Count = pp.Len()
	for _, p := range permissions {
		out.Permissions = append(out.Permissions, policyCompiledPermission{
			ID: p.ID(), EvidenceID: string(p.EvidenceID), Action: string(p.Action),
			Hosts: p.Hosts, Methods: p.Methods, PathPrefixes: p.PathPrefixes,
			ObservedAt: p.ObservedAt.UTC(), ValidUntil: p.ValidUntil.UTC(),
		})
	}
	return out, pp, nil
}

func executePolicyCompile(ctx context.Context, o policyCompileOptions) error {
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
	program, err := repo.GetProgram(ctx, domain.ProgramID(o.Program))
	if err != nil {
		return fmt.Errorf("policy compile: load program %s: %w", o.Program, err)
	}
	ids := make([]domain.EvidenceID, 0, len(o.Evidence))
	for _, id := range o.Evidence {
		ids = append(ids, domain.EvidenceID(id))
	}
	if len(ids) == 0 {
		ids = append(ids, program.Evidence...)
	}
	if len(ids) == 0 {
		return fmt.Errorf("policy compile: program %s has no associated evidence; pass -evidence", program.ID)
	}
	contents, err := authz.NewFileContentReader(o.ContentRoot)
	if err != nil {
		return err
	}
	out, _, err := compilePlatformAuthority(ctx, repo, contents, program, ids, o.Now)
	if err != nil {
		return err
	}
	return encodePolicyOutput(out)
}
