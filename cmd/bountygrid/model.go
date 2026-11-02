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

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

type modelOptions struct {
	Purpose     string
	DataClass   string
	MaxTokens   int
	Budget      int64
	Task        string
	ModelEgress string
	Redacted    bool
	Escalation  string

	Call          bool
	Endpoint      string
	ProviderModel string
	ProviderName  string
	APIKeyEnv     string
	Prompt        string
	PromptFile    string
	Program       string
	Step          string
	Timeout       time.Duration
	CallID        string
	Persist       bool
}

type modelPlan struct {
	Purpose     string        `json:"purpose"`
	DataClass   string        `json:"data_class"`
	ModelEgress string        `json:"model_egress"`
	Redacted    bool          `json:"redacted"`
	Escalation  string        `json:"escalation,omitempty"`
	MaxTokens   int           `json:"max_tokens"`
	Budget      domain.Money  `json:"budget"`
	Candidates  []modelChoice `json:"candidates"`
	Note        string        `json:"note,omitempty"`
}

type modelChoice struct {
	Provider        string       `json:"provider"`
	Model           string       `json:"model"`
	Local           bool         `json:"local"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	EstimatedCost   domain.Money `json:"estimated_cost"`
	WithinBudget    bool         `json:"within_budget"`
}

func parseModelOptions(args []string) (modelOptions, error) {
	var o modelOptions
	fs := flag.NewFlagSet("model", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Purpose, "purpose", "ANALYZE", "PLAN|ANALYZE|CODE|REPORT|EMBED")
	fs.StringVar(&o.DataClass, "data-class", "INTERNAL", "PUBLIC|INTERNAL|SENSITIVE")
	fs.IntVar(&o.MaxTokens, "max-tokens", 2000, "output token ceiling")
	fs.Int64Var(&o.Budget, "budget", 100, "per-call budget in fen (0 = unpriced)")
	fs.StringVar(&o.Task, "task", "", "optional ResearchTask ID")
	fs.StringVar(&o.ModelEgress, "model-egress", "UNKNOWN",
		"program egress policy: LOCAL_ONLY|REMOTE_REDACTED_ONLY|REMOTE_ALLOWED|UNKNOWN")
	fs.BoolVar(&o.Redacted, "redacted", false, "attest the input is redacted (needed for REMOTE_REDACTED_ONLY)")
	fs.StringVar(&o.Escalation, "escalation", "",
		"optional escalation: STALLED|LOW_CONFIDENCE|HIGH_VALUE_TASK|VERIFICATION_CONFLICT")
	fs.BoolVar(&o.Call, "call", false, "perform one real model call instead of printing a plan")
	fs.StringVar(&o.Endpoint, "endpoint", os.Getenv("BOUNTYGRID_MODEL_ENDPOINT"), "loopback OpenAI-compatible base URL")
	fs.StringVar(&o.ProviderModel, "provider-model", os.Getenv("BOUNTYGRID_MODEL_NAME"), "provider model name")
	fs.StringVar(&o.ProviderName, "provider-name", "local-openai", "registered provider name")
	fs.StringVar(&o.APIKeyEnv, "api-key-env", "", "environment variable holding the bearer token")
	fs.StringVar(&o.Prompt, "prompt", "", "user prompt for -call")
	fs.StringVar(&o.PromptFile, "prompt-file", "", "UTF-8 file with the user prompt for -call")
	fs.StringVar(&o.Program, "program", "", "program whose egress policy governs the call")
	fs.StringVar(&o.Step, "step", "", "task step name for a stable call id")
	fs.DurationVar(&o.Timeout, "timeout", 120*time.Second, "per-call timeout")
	fs.StringVar(&o.CallID, "call-id", "", "explicit idempotency key")
	fs.BoolVar(&o.Persist, "persist", false, "persist model calls and attempts in PostgreSQL")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.MaxTokens <= 0 {
		return o, fmt.Errorf("model: -max-tokens must be positive")
	}
	if o.Budget < 0 {
		return o, fmt.Errorf("model: -budget must not be negative")
	}
	if err := validateCallOptions(fs, o); err != nil {
		return o, err
	}
	return o, nil
}

func runModel(ctx context.Context, args []string) error {
	opts, err := parseModelOptions(args)
	if err != nil {
		return err
	}
	if opts.Call {
		return runModelCall(ctx, opts)
	}
	plan, err := buildModelPlan(opts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(plan)
}

func buildModelPlan(opts modelOptions) (modelPlan, error) {
	req := model.ModelRequest{
		TaskID:      domain.TaskID(opts.Task),
		Purpose:     model.Purpose(strings.ToUpper(strings.TrimSpace(opts.Purpose))),
		DataClass:   model.DataClass(strings.ToUpper(strings.TrimSpace(opts.DataClass))),
		ModelEgress: domain.ModelEgressPolicy(strings.ToUpper(strings.TrimSpace(opts.ModelEgress))),
		Redacted:    opts.Redacted,
		Escalation:  model.EscalationReason(strings.ToUpper(strings.TrimSpace(opts.Escalation))),
		MaxTokens:   opts.MaxTokens,
		Budget:      domain.Money{Amount: opts.Budget, Currency: "CNY"},
	}
	if !req.Purpose.Valid() {
		return modelPlan{}, fmt.Errorf("model: invalid purpose %q", opts.Purpose)
	}
	if !req.DataClass.Valid() {
		return modelPlan{}, fmt.Errorf("model: invalid data class %q", opts.DataClass)
	}
	if !req.ModelEgress.Valid() {
		return modelPlan{}, fmt.Errorf("model: invalid model egress policy %q", opts.ModelEgress)
	}
	if !req.Escalation.Valid() {
		return modelPlan{}, fmt.Errorf("model: invalid escalation reason %q", opts.Escalation)
	}
	// These flags come from the operator running the CLI, not from a model or
	// untrusted request. The CLI is a dry-run planning command, so it installs
	// an explicit operator resolver; production callers must use a store-backed
	// PolicyResolver instead of echoing request fields.
	r, err := model.New(model.Options{
		Providers: demoProviders(),
		PolicyResolver: model.PolicyResolverFunc(func(_ context.Context, req model.ModelRequest) (model.ModelPolicy, error) {
			return model.ModelPolicy{
				Egress: req.ModelEgress, Redacted: req.Redacted, Escalation: req.Escalation,
			}, nil
		}),
	})
	if err != nil {
		return modelPlan{}, err
	}
	out := modelPlan{
		Purpose: string(req.Purpose), DataClass: string(req.DataClass),
		ModelEgress: string(req.ModelEgress.Effective()), Redacted: req.Redacted,
		Escalation: string(req.Escalation),
		MaxTokens:  req.MaxTokens, Budget: req.Budget,
	}
	for _, info := range r.Candidates(req) {
		estimate := model.EstimateCost(info, req)
		out.Candidates = append(out.Candidates, modelChoice{
			Provider: info.Name, Model: info.Model, Local: info.Local,
			MaxOutputTokens: info.MaxOutputTokens, EstimatedCost: estimate,
			WithinBudget: req.Budget.Amount == 0 || estimate.Amount <= req.Budget.Amount,
		})
	}
	if len(out.Candidates) == 0 {
		out.Note = "no eligible provider for this purpose/data class/output ceiling"
	}
	return out, nil
}

// demoProviders is a routing catalog, not a model client. The command only
// prints the plan; no provider is called and no network I/O occurs.
func demoProviders() []model.Provider {
	return []model.Provider{
		&model.StaticProvider{Meta: model.ProviderInfo{
			Name: "local-small", Model: "local-small", Local: true,
			Purposes:        []model.Purpose{model.PurposeCode, model.PurposeEmbed, model.PurposeReport},
			MaxOutputTokens: 4096, Strength: 1,
		}},
		&model.StaticProvider{Meta: model.ProviderInfo{
			Name: "local-large", Model: "local-large", Local: true,
			MaxOutputTokens: 8192, Strength: 3,
		}},
		&model.StaticProvider{Meta: model.ProviderInfo{
			Name: "remote-strong", Model: "remote-strong", Local: false,
			Purposes:        []model.Purpose{model.PurposePlan, model.PurposeAnalyze, model.PurposeReport, model.PurposeCode},
			MaxOutputTokens: 16384, InputPricePerMTok: 1000, OutputPricePerMTok: 2000, Strength: 5,
		}},
	}
}

func validateCallOptions(fs *flag.FlagSet, o modelOptions) error {
	callOnly := []string{
		"endpoint", "provider-model", "provider-name", "api-key-env",
		"prompt", "prompt-file", "program", "step", "timeout", "call-id", "persist",
	}
	if !o.Call {
		for _, name := range callOnly {
			if flagWasSet(fs, name) {
				return fmt.Errorf("model: -%s requires -call", name)
			}
		}
		return nil
	}
	if o.Timeout <= 0 {
		return fmt.Errorf("model: -timeout must be positive")
	}
	if !model.Purpose(strings.ToUpper(strings.TrimSpace(o.Purpose))).Valid() {
		return fmt.Errorf("model: invalid purpose %q", o.Purpose)
	}
	if !model.DataClass(strings.ToUpper(strings.TrimSpace(o.DataClass))).Valid() {
		return fmt.Errorf("model: invalid data class %q", o.DataClass)
	}
	if !model.EscalationReason(strings.ToUpper(strings.TrimSpace(o.Escalation))).Valid() {
		return fmt.Errorf("model: invalid escalation %q", o.Escalation)
	}
	if (strings.TrimSpace(o.Prompt) == "") == (strings.TrimSpace(o.PromptFile) == "") {
		return fmt.Errorf("model: -call requires exactly one of -prompt and -prompt-file")
	}
	if strings.TrimSpace(o.Endpoint) == "" {
		return fmt.Errorf("model: -call requires -endpoint or BOUNTYGRID_MODEL_ENDPOINT")
	}
	if strings.TrimSpace(o.ProviderModel) == "" {
		return fmt.Errorf("model: -call requires -provider-model or BOUNTYGRID_MODEL_NAME")
	}
	if strings.TrimSpace(o.Program) != "" && !o.Persist {
		return fmt.Errorf("model: -program requires -persist")
	}
	if flagWasSet(fs, "model-egress") {
		return fmt.Errorf("model: -model-egress is not allowed with -call; the ceiling comes from the stored program")
	}
	if o.APIKeyEnv != "" {
		if _, ok := os.LookupEnv(o.APIKeyEnv); !ok || strings.TrimSpace(os.Getenv(o.APIKeyEnv)) == "" {
			return fmt.Errorf("model: -api-key-env %s is unset or empty", o.APIKeyEnv)
		}
	}
	return nil
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
