// Package openai implements a model.Provider for a loopback,
// OpenAI-compatible chat-completions server.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
	"github.com/Zn070515/BountyGrid/internal/model/loopback"
)

const (
	defaultName       = "local-openai"
	userAgent         = "bountygrid-model/1"
	maxModelNameBytes = 256
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
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = defaultName
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		return nil, fmt.Errorf("openai: model is required")
	}
	if len(modelName) > maxModelNameBytes {
		return nil, fmt.Errorf("openai: model name is longer than %d bytes", maxModelNameBytes)
	}
	if cfg.InputPricePerMTok < 0 || cfg.OutputPricePerMTok < 0 {
		return nil, fmt.Errorf("openai: prices must not be negative")
	}
	if cfg.MaxContextTokens < 0 || cfg.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("openai: token limits must not be negative")
	}
	client, err := loopback.New(loopback.Config{
		Endpoint:         cfg.Endpoint,
		APIKey:           cfg.APIKey,
		Timeout:          cfg.Timeout,
		MaxResponseBytes: cfg.MaxResponseBytes,
	})
	if err != nil {
		return nil, err
	}
	return &Provider{
		info: model.ProviderInfo{
			Name: name, Model: modelName, Local: true,
			MaxContextTokens:   cfg.MaxContextTokens,
			MaxOutputTokens:    cfg.MaxOutputTokens,
			InputPricePerMTok:  cfg.InputPricePerMTok,
			OutputPricePerMTok: cfg.OutputPricePerMTok,
			Endpoint:           client.Base(),
			Trust:              model.TrustInProcess,
			Strength:           cfg.Strength,
		},
		client: client,
	}, nil
}

// Info returns the validated provider snapshot.
func (p *Provider) Info() model.ProviderInfo { return p.info }

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete performs one non-streaming chat completion. Output is text; it never
// becomes a decision or a platform action.
func (p *Provider) Complete(ctx context.Context, req model.ModelRequest) (model.ModelResponse, error) {
	body, err := json.Marshal(chatRequest{
		Model:     p.info.Model,
		Messages:  toMessages(req.Input),
		MaxTokens: req.MaxTokens,
		Stream:    false,
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
	return model.ModelResponse{
		Content:  out.Choices[0].Message.Content,
		Usage:    model.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
		Model:    p.info.Model,
		Provider: p.info.Name,
	}, nil
}

func toMessages(in []model.Message) []chatMessage {
	out := make([]chatMessage, 0, len(in))
	for _, m := range in {
		out = append(out, chatMessage{Role: m.Role, Content: m.Content})
	}
	return out
}
