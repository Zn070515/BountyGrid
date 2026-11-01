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

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
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
		Decision: string(res.Decision), Matched: res.Matched,
	}, nil
}

func runPolicy(_ context.Context, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("policy: expected 'check' (usage: bountygrid policy check -rules <file> -action <action>)")
	}
	opts, err := parsePolicyOptions(args[1:])
	if err != nil {
		return err
	}
	out, err := executePolicyCheck(opts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
