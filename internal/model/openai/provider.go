// Package openai implements a model.Provider for a loopback,
// OpenAI-compatible chat-completions server.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
	"github.com/Zn070515/BountyGrid/internal/model/loopback"
)

const (
	defaultName = "local-openai"
	userAgent   = "bountygrid-model/1"
)

// Config is the provider's static configuration. Zero Timeout and
// MaxResponseBytes use the loopback defaults; negative values are rejected.
type Config struct {
	Name               string
	Model              string
	Endpoint           string
	APIKey             string
	Timeout            time.Duration
	MaxResponseBytes   int64
	MaxContextTokens   int
	MaxOutputTokens    int
	Strength           int
	InputPricePerMTok  int64
	OutputPricePerMTok int64
}

// Provider is a stateless OpenAI-compatible client. It is safe for concurrent
// use.
type Provider struct {
	info   model.ProviderInfo
	client *loopback.Client
}

var _ model.Provider = (*Provider)(nil)

// New validates the configuration and builds a provider. The endpoint must be
// loopback because this provider declares TrustInProcess and is allowed to see
// SENSITIVE data.
func New(cfg Config) (*Provider, error) {
	info, client, err := loopback.NewProvider(loopback.ProviderConfig{
		Name:               cfg.Name,
		Model:              cfg.Model,
		Endpoint:           cfg.Endpoint,
		APIKey:             cfg.APIKey,
		Timeout:            cfg.Timeout,
		MaxResponseBytes:   cfg.MaxResponseBytes,
		MaxContextTokens:   cfg.MaxContextTokens,
		MaxOutputTokens:    cfg.MaxOutputTokens,
		Strength:           cfg.Strength,
		InputPricePerMTok:  cfg.InputPricePerMTok,
		OutputPricePerMTok: cfg.OutputPricePerMTok,
	}, defaultName)
	if err != nil {
		return nil, err
	}
	return &Provider{info: info, client: client}, nil
}

// Info returns the validated provider snapshot.
func (p *Provider) Info() model.ProviderInfo { return p.info }

type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []chatMessage  `json:"messages"`
	MaxTokens          int            `json:"max_tokens"`
	Stream             bool           `json:"stream"`
	Temperature        *float64       `json:"temperature,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete performs one non-streaming chat completion. Output is text; it never
// becomes a decision or a platform action.
func (p *Provider) Complete(ctx context.Context, req model.ModelRequest) (model.ModelResponse, error) {
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return model.ModelResponse{}, fmt.Errorf("%w: temperature must be in [0, 2]", domain.ErrNotSent)
	}
	if req.ThinkingBudget > 0 {
		return model.ModelResponse{}, fmt.Errorf(
			"%w: openai provider does not support thinking budgets", domain.ErrNotSent)
	}
	body, err := json.Marshal(chatRequest{
		Model:              p.info.Model,
		Messages:           toMessages(req.Input),
		MaxTokens:          req.MaxTokens,
		Stream:             false,
		Temperature:        req.Temperature,
		ChatTemplateKwargs: thinkingKwargs(req.Thinking),
	})
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: marshal request: %v", domain.ErrNotSent, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.client.Base()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: build request: %v", domain.ErrNotSent, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	if p.client.APIKey() != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.client.APIKey())
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return model.ModelResponse{}, loopback.ClassifyDoError(err)
	}
	defer resp.Body.Close()

	payload, err := p.client.ReadBody(resp.Body)
	if err != nil {
		return model.ModelResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return model.ModelResponse{}, fmt.Errorf("openai: %s returned HTTP %d: %s",
			p.info.Name, resp.StatusCode, loopback.Snippet(payload))
	}
	var out chatResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return model.ModelResponse{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return model.ModelResponse{}, fmt.Errorf("openai: response has no choices")
	}
	choice := out.Choices[0]
	return model.ModelResponse{
		Content:    choice.Message.Content,
		Usage:      model.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
		Model:      p.info.Model,
		Provider:   p.info.Name,
		StopReason: choice.FinishReason,
		Truncated:  choice.FinishReason == "length",
	}, nil
}

func thinkingKwargs(mode model.ThinkingMode) map[string]any {
	switch mode {
	case model.ThinkingOn:
		return map[string]any{"enable_thinking": true}
	case model.ThinkingOff:
		return map[string]any{"enable_thinking": false}
	default:
		return nil
	}
}

func toMessages(in []model.Message) []chatMessage {
	out := make([]chatMessage, 0, len(in))
	for _, m := range in {
		out = append(out, chatMessage{Role: m.Role, Content: m.Content})
	}
	return out
}
