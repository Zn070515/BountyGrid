package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
	"github.com/Zn070515/BountyGrid/internal/model/anthropic"
	"github.com/Zn070515/BountyGrid/internal/model/openai"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

const maxPromptFileBytes = 1 << 20

type modelCallOutput struct {
	CallID     string       `json:"call_id"`
	Provider   string       `json:"provider"`
	Model      string       `json:"model"`
	Content    string       `json:"content"`
	Usage      model.Usage  `json:"usage"`
	Cost       domain.Money `json:"cost"`
	LatencyMS  int64        `json:"latency_ms"`
	StopReason string       `json:"stop_reason,omitempty"`
	Truncated  bool         `json:"truncated,omitempty"`
}

func runModelCall(ctx context.Context, opts modelOptions) error {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	out, err := executeModelCall(ctx, opts)
	if err != nil {
		return err
	}
	return jsonEncoder().Encode(out)
}

func executeModelCall(ctx context.Context, opts modelOptions) (modelCallOutput, error) {
	prompt := opts.Prompt
	if opts.PromptFile != "" {
		var err error
		prompt, err = readPromptFile(opts.PromptFile)
		if err != nil {
			return modelCallOutput{}, err
		}
	}

	credential := ""
	if opts.APIKeyEnv != "" {
		credential = strings.TrimSpace(os.Getenv(opts.APIKeyEnv))
		if credential == "" {
			return modelCallOutput{}, fmt.Errorf("model: -api-key-env %s is unset or empty", opts.APIKeyEnv)
		}
	}
	format := strings.ToLower(strings.TrimSpace(opts.ProviderFormat))
	if format == "" {
		format = "openai"
	}
	provider, err := newLocalProvider(format, opts, credential)
	if err != nil {
		return modelCallOutput{}, err
	}

	var (
		callStore model.CallStore = model.NewMemoryCallStore()
		auditor   model.Auditor
		pool      *pgxpool.Pool
	)
	resolver := model.StorePolicyResolver{
		Attestation: model.ModelPolicy{
			Redacted:   opts.Redacted,
			Escalation: model.EscalationReason(strings.ToUpper(strings.TrimSpace(opts.Escalation))),
		},
	}
	if opts.Persist {
		d, err := dsn()
		if err != nil {
			return modelCallOutput{}, err
		}
		pool, err = postgres.Open(ctx, d)
		if err != nil {
			return modelCallOutput{}, err
		}
		defer pool.Close()
		repo := postgres.NewRepo(pool)
		callStore = postgres.NewModelCallStore(pool)
		auditor = postgres.NewAuditStore(pool)
		resolver.Store = repo
	}

	thinking := model.ThinkingAuto
	switch strings.ToLower(strings.TrimSpace(opts.Thinking)) {
	case "on":
		thinking = model.ThinkingOn
	case "off":
		thinking = model.ThinkingOff
	}
	var temperature *float64
	if opts.Temperature >= 0 {
		value := opts.Temperature
		temperature = &value
	}

	req := model.ModelRequest{
		TaskID:         domain.TaskID(strings.TrimSpace(opts.Task)),
		ProgramID:      domain.ProgramID(strings.TrimSpace(opts.Program)),
		StepID:         strings.TrimSpace(opts.Step),
		Purpose:        model.Purpose(strings.ToUpper(strings.TrimSpace(opts.Purpose))),
		DataClass:      model.DataClass(strings.ToUpper(strings.TrimSpace(opts.DataClass))),
		Input:          []model.Message{{Role: "user", Content: prompt}},
		MaxTokens:      opts.MaxTokens,
		Budget:         domain.Money{Amount: opts.Budget, Currency: "CNY"},
		CallID:         strings.TrimSpace(opts.CallID),
		Thinking:       thinking,
		ThinkingBudget: opts.ThinkingBudget,
		Temperature:    temperature,
	}
	if req.CallID == "" {
		if req.TaskID != "" && req.StepID != "" {
			req.CallID = model.StableCallID(req.TaskID, req.StepID, req.Purpose, model.PromptHash(req))
		} else {
			req.CallID = "call/cli/" + time.Now().UTC().Format(time.RFC3339Nano)
		}
	}

	router, err := model.New(model.Options{
		Providers:      []model.Provider{provider},
		Auditor:        auditor,
		CallStore:      callStore,
		PolicyResolver: resolver,
	})
	if err != nil {
		return modelCallOutput{}, err
	}
	resp, err := router.Route(ctx, req)
	if err != nil {
		return modelCallOutput{}, err
	}
	return modelCallOutput{
		CallID:     req.CallID,
		Provider:   resp.Provider,
		Model:      resp.Model,
		Content:    resp.Content,
		Usage:      resp.Usage,
		Cost:       resp.Cost,
		LatencyMS:  resp.Latency.Milliseconds(),
		StopReason: resp.StopReason,
		Truncated:  resp.Truncated,
	}, nil
}

func newLocalProvider(format string, opts modelOptions, credential string) (model.Provider, error) {
	switch format {
	case "openai":
		return openai.New(openai.Config{
			Name:     opts.ProviderName,
			Model:    opts.ProviderModel,
			Endpoint: opts.Endpoint,
			APIKey:   credential,
			Timeout:  opts.Timeout,
		})
	case "anthropic":
		return anthropic.New(anthropic.Config{
			Name:     opts.ProviderName,
			Model:    opts.ProviderModel,
			Endpoint: opts.Endpoint,
			APIKey:   credential,
			Timeout:  opts.Timeout,
		})
	default:
		return nil, fmt.Errorf("model: unsupported provider format %q", format)
	}
}

func readPromptFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("model: read prompt file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPromptFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("model: read prompt file: %w", err)
	}
	if len(b) > maxPromptFileBytes {
		return "", fmt.Errorf("model: prompt file exceeds %d bytes", maxPromptFileBytes)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("model: prompt file is not valid UTF-8")
	}
	prompt := string(b)
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("model: prompt file is empty")
	}
	return prompt, nil
}

func jsonEncoder() *json.Encoder {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc
}
