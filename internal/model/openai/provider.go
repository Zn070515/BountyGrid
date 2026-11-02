// Package openai implements a model.Provider for a loopback, OpenAI-compatible
// chat-completions server. It never leaves the machine: the endpoint must be a
// loopback address, redirects are not followed, and environment proxies are
// ignored.
package openai

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/model"
)

const (
	defaultName             = "local-openai"
	defaultTimeout          = 120 * time.Second
	defaultMaxResponseBytes = int64(8 << 20)
	userAgent               = "bountygrid-model/1"
	errorSnippetBytes       = 256
	maxModelNameBytes       = 256
)

// Config is the provider's static configuration. Zero Timeout and
// MaxResponseBytes use the package defaults; negative values are rejected.
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
	info    model.ProviderInfo
	base    string
	apiKey  string
	timeout time.Duration
	maxBody int64
	client  *http.Client
}

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
	base, err := normalizeBase(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("openai: timeout must not be negative")
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody == 0 {
		maxBody = defaultMaxResponseBytes
	}
	if maxBody < 0 {
		return nil, fmt.Errorf("openai: max response bytes must not be negative")
	}
	if cfg.InputPricePerMTok < 0 || cfg.OutputPricePerMTok < 0 {
		return nil, fmt.Errorf("openai: prices must not be negative")
	}
	if cfg.MaxContextTokens < 0 || cfg.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("openai: token limits must not be negative")
	}
	return &Provider{
		info: model.ProviderInfo{
			Name: name, Model: modelName, Local: true,
			MaxContextTokens:   cfg.MaxContextTokens,
			MaxOutputTokens:    cfg.MaxOutputTokens,
			InputPricePerMTok:  cfg.InputPricePerMTok,
			OutputPricePerMTok: cfg.OutputPricePerMTok,
			Endpoint:           base,
			Trust:              model.TrustInProcess,
			Strength:           cfg.Strength,
		},
		base:    base,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		timeout: timeout,
		maxBody: maxBody,
		client: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("openai: redirects are not followed")
			},
		},
	}, nil
}

// Info returns the validated provider snapshot.
func (p *Provider) Info() model.ProviderInfo { return p.info }

// normalizeBase validates the endpoint and returns the chat-completions base:
// scheme://host[:port]/v1 with no trailing slash.
func normalizeBase(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", fmt.Errorf("openai: endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("openai: invalid endpoint %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("openai: endpoint %q must use http or https", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("openai: endpoint %q must not carry user info, query or fragment", raw)
	}
	if !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("openai: endpoint %q is not loopback", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	base := u.String()
	if !strings.HasSuffix(u.Path, "/v1") {
		base += "/v1"
	}
	return base, nil
}

func isLoopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.Unmap().IsLoopback()
}
