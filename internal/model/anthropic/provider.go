// Package anthropic implements a model.Provider for a loopback,
// Anthropic-compatible messages server.
package anthropic

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
	defaultName       = "local-anthropic"
	userAgent         = "bountygrid-model/1"
	anthropicVersion  = "2023-06-01"
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

// Provider is a stateless Anthropic-compatible client. It is safe for
// concurrent use.
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
		return nil, fmt.Errorf("anthropic: model is required")
	}
	if len(modelName) > maxModelNameBytes {
		return nil, fmt.Errorf("anthropic: model name is longer than %d bytes", maxModelNameBytes)
	}
	if cfg.InputPricePerMTok < 0 || cfg.OutputPricePerMTok < 0 {
		return nil, fmt.Errorf("anthropic: prices must not be negative")
	}
	if cfg.MaxContextTokens < 0 || cfg.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("anthropic: token limits must not be negative")
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
			Endpoint:           client.Base,
			Trust:              model.TrustInProcess,
			Strength:           cfg.Strength,
		},
		client: client,
	}, nil
}

// Info returns the validated provider snapshot.
func (p *Provider) Info() model.ProviderInfo { return p.info }

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type messagesResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete performs one non-streaming messages call. Output is text; it never
// becomes a decision or a platform action.
func (p *Provider) Complete(ctx context.Context, req model.ModelRequest) (model.ModelResponse, error) {
	system, messages, err := toAnthropicMessages(req.Input)
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	if len(messages) == 0 {
		return model.ModelResponse{}, fmt.Errorf("%w: anthropic requires at least one user or assistant message", domain.ErrNotSent)
	}
	// The Messages API rejects a conversation whose first turn is not a user
	// message. Rejecting it here keeps the failure pre-send (FAILED_BEFORE_SEND)
	// instead of turning a locally detectable 400 into an AMBIGUOUS, possibly
	// billed call.
	if messages[0].Role != "user" {
		return model.ModelResponse{}, fmt.Errorf("%w: anthropic requires the first message to use the user role", domain.ErrNotSent)
	}
	body, err := json.Marshal(messagesRequest{
		Model:     p.info.Model,
		MaxTokens: req.MaxTokens,
		System:    system,
		Messages:  messages,
	})
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: marshal request: %v", domain.ErrNotSent, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.client.Base+"/messages", bytes.NewReader(body))
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: build request: %v", domain.ErrNotSent, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.client.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.client.APIKey)
	}

	resp, err := p.client.HTTP.Do(httpReq)
	if err != nil {
		return model.ModelResponse{}, loopback.ClassifyDoError(err)
	}
	defer resp.Body.Close()

	payload, err := loopback.ReadLimited(resp.Body, p.client.MaxBody)
	if err != nil {
		return model.ModelResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return model.ModelResponse{}, fmt.Errorf("anthropic: %s returned HTTP %d: %s",
			p.info.Name, resp.StatusCode, loopback.Snippet(payload))
	}
	var out messagesResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return model.ModelResponse{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var text strings.Builder
	foundText := false
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
			foundText = true
		}
	}
	// A 200 with no text block (empty content, tool_use-only, thinking-only) is
	// not a usable completion for a text provider. Fail instead of recording an
	// empty response as COMPLETED; mirror openai's no-choices guard.
	if !foundText {
		return model.ModelResponse{}, fmt.Errorf("anthropic: response has no text content")
	}
	return model.ModelResponse{
		Content:  text.String(),
		Usage:    model.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		Model:    p.info.Model,
		Provider: p.info.Name,
	}, nil
}

func toAnthropicMessages(in []model.Message) (string, []anthropicMessage, error) {
	var systems []string
	out := make([]anthropicMessage, 0, len(in))
	for _, m := range in {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system":
			systems = append(systems, m.Content)
		case "user", "assistant":
			if n := len(out); n > 0 && out[n-1].Role == role {
				out[n-1].Content += "\n\n" + m.Content
				continue
			}
			out = append(out, anthropicMessage{Role: role, Content: m.Content})
		default:
			return "", nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
	}
	return strings.Join(systems, "\n\n"), out, nil
}
