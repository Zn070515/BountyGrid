# Local OpenAI-compatible Model Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a real loopback OpenAI-compatible `model.Provider`, a store-backed `PolicyResolver`, and a `bountygrid model -call` CLI that invokes it and optionally persists `ModelCall`/`ModelCallAttempt` plus the `model.call` audit event.

**Architecture:** A new `internal/model/openai` subpackage implements the existing `model.Provider` interface over HTTP; `internal/model` stays free of `net/http`. The router and durable call ledger are already implemented and unchanged. The CLI builds a one-provider router, resolves egress from the stored `Program.ModelEgressPolicy`, and persists through the existing `postgres.ModelCallStore` and `postgres.AuditStore`.

**Tech Stack:** Go 1.27.3, `net/http`, `net/http/httptest`, `encoding/json`, `net/netip`, `pgx/v5`, PostgreSQL 16.

**Spec:** `docs/superpowers/specs/2026-11-01-local-model-provider-design.md`

---

## Preconditions

- Work on branch `feat/local-model-provider` (already created from clean `main`).
- `GOPROXY=https://goproxy.cn,direct` is set for the shell (the toolchain is cached but module downloads still need it).
- PostgreSQL integration tests need `TEST_DATABASE_URL`; all other tests run without a database.

```powershell
$env:GOPROXY='https://goproxy.cn,direct'
go version   # go1.27.3 windows/amd64
```

---

### Task 1: Provider construction and endpoint validation

**Files:**
- Create: `internal/model/openai/provider.go`
- Create: `internal/model/openai/provider_test.go`

- [ ] **Step 1: Write the failing construction tests**

Create `internal/model/openai/provider_test.go`:

```go
package openai

import (
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/model"
)

func TestNewRejectsNonLoopback(t *testing.T) {
	for _, endpoint := range []string{
		"http://10.0.0.1:8080/v1",
		"http://example.com/v1",
		"https://192.168.1.2:8080/v1",
	} {
		if _, err := New(Config{Model: "m", Endpoint: endpoint}); err == nil {
			t.Fatalf("New(%q) succeeded, want a non-loopback error", endpoint)
		}
	}
}

func TestNewAcceptsLoopbackAndNormalizesBase(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080":     "http://127.0.0.1:8080/v1",
		"http://127.0.0.1:8080/":    "http://127.0.0.1:8080/v1",
		"http://127.0.0.1:8080/v1":  "http://127.0.0.1:8080/v1",
		"http://127.0.0.1:8080/v1/": "http://127.0.0.1:8080/v1",
		"http://localhost:8080/v1":  "http://localhost:8080/v1",
		"http://[::1]:8080/v1":      "http://[::1]:8080/v1",
	}
	for endpoint, want := range cases {
		p, err := New(Config{Model: "m", Endpoint: endpoint, APIKey: "secret"})
		if err != nil {
			t.Fatalf("New(%q): %v", endpoint, err)
		}
		info := p.Info()
		if info.Endpoint != want {
			t.Fatalf("New(%q).Endpoint = %q, want %q", endpoint, info.Endpoint, want)
		}
		if info.Trust != model.TrustInProcess || !info.Local {
			t.Fatalf("New(%q) trust = %s local=%v, want IN_PROCESS local", endpoint, info.Trust, info.Local)
		}
		if info.Name != defaultName || info.Model != "m" {
			t.Fatalf("New(%q) info = %+v", endpoint, info)
		}
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	long := strings.Repeat("a", 4096)
	cases := []Config{
		{Model: "", Endpoint: "http://127.0.0.1:8080/v1"},
		{Model: "m", Endpoint: ""},
		{Model: "m", Endpoint: "ftp://127.0.0.1:8080/v1"},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1?x=1"},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", Timeout: -time.Second},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", MaxResponseBytes: -1},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", InputPricePerMTok: -1},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", MaxContextTokens: -1},
		{Model: long, Endpoint: "http://127.0.0.1:8080/v1"},
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d: New accepted an invalid config", i)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/openai/ -run 'TestNew' -v`

Expected: build failure, `undefined: New` / `undefined: Config`.

- [ ] **Step 3: Implement construction**

Create `internal/model/openai/provider.go`:

```go
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
	Name             string
	Model            string
	Endpoint         string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxContextTokens int
	MaxOutputTokens  int
	Strength         int
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/openai/ -run 'TestNew' -v`

Expected: `PASS` for all three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/model/openai/provider.go internal/model/openai/provider_test.go
git commit -m "feat(model): add loopback OpenAI provider construction"
```

---

### Task 2: Successful completion mapping

**Files:**
- Modify: `internal/model/openai/provider.go`
- Modify: `internal/model/openai/provider_test.go`

- [ ] **Step 1: Write the failing success tests**

Append to `internal/model/openai/provider_test.go`:

```go
func TestCompleteSuccess(t *testing.T) {
	var gotPath, gotCT, gotUA string
	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}))
	defer srv.Close()

	p, err := New(Config{Model: "test-model", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), model.ModelRequest{
		Input:     []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotCT != "application/json" || gotUA != userAgent {
		t.Fatalf("headers content-type=%q user-agent=%q", gotCT, gotUA)
	}
	if got.Model != "test-model" || got.MaxTokens != 16 || got.Stream {
		t.Fatalf("request body = %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Provider != defaultName || resp.Model != "test-model" {
		t.Fatalf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
}

func TestCompleteBaseAlreadyV1(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want no doubled /v1", gotPath)
	}
}

func TestCompleteSendsBearerOnlyWhenConfigured(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatalf("Authorization = %q without a credential", auth)
	}

	p, err = New(Config{Model: "m", Endpoint: srv.URL, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", auth)
	}
}

func TestCompleteEmptyContentIsValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 5,
	})
	if err != nil {
		t.Fatalf("empty content was treated as an error: %v", err)
	}
	if resp.Content != "" || resp.Usage.OutputTokens != 5 {
		t.Fatalf("resp = %+v", resp)
	}
}
```

Extend the test file's import block with `context`, `encoding/json`, `io`, `net/http`, `net/http/httptest` (`model` is already imported).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/openai/ -run 'TestComplete' -v`

Expected: build failure, `p.Complete undefined`.

- [ ] **Step 3: Implement Complete**

Append to `internal/model/openai/provider.go`:

```go
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("%w: build request: %v", domain.ErrNotSent, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return model.ModelResponse{}, classifyDoError(err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBody+1))
	if err != nil {
		return model.ModelResponse{}, fmt.Errorf("openai: read response: %w", err)
	}
	if int64(len(payload)) > p.maxBody {
		return model.ModelResponse{}, fmt.Errorf("openai: response exceeds %d bytes", p.maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return model.ModelResponse{}, fmt.Errorf("openai: %s returned HTTP %d: %s",
			p.info.Name, resp.StatusCode, snippet(payload))
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
```

Add these imports to `provider.go`: `bytes`, `context`, `encoding/json`, `io`, `github.com/Zn070515/BountyGrid/internal/domain`.

`classifyDoError` and `snippet` are added in Task 3; until then this file does not compile. To keep Task 2 self-contained, add these two stubs at the end of `provider.go` now, then replace them in Task 3:

```go
func classifyDoError(err error) error { return err }

func snippet(b []byte) string {
	if len(b) <= errorSnippetBytes {
		return string(b)
	}
	return string(b[:errorSnippetBytes]) + "...(truncated)"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/openai/ -run 'TestComplete' -v`

Expected: `PASS` for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/model/openai/provider.go internal/model/openai/provider_test.go
git commit -m "feat(model): map OpenAI chat completions into ModelResponse"
```

---

### Task 3: Error classification and response limits

**Files:**
- Modify: `internal/model/openai/provider.go`
- Modify: `internal/model/openai/provider_test.go`

- [ ] **Step 1: Write the failing error tests**

Append to `internal/model/openai/provider_test.go`:

```go
func TestCompleteHTTP500IsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err == nil {
		t.Fatal("HTTP 500 returned no error")
	}
	if errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("HTTP 500 was classified as not sent: %v", err)
	}
}

func TestCompleteMalformedJSONIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err == nil || errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("malformed JSON err = %v, want an ambiguous error", err)
	}
}

func TestCompleteOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+strings.Repeat("x", 100)+`"}}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err == nil || errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("oversized err = %v, want an ambiguous size error", err)
	}
}

func TestCompleteDialFailureIsNotSent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	p, err := New(Config{Model: "m", Endpoint: "http://" + addr + "/v1", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("dial failure err = %v, want domain.ErrNotSent", err)
	}
}

func TestCompleteTimeoutIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err == nil {
		t.Fatal("timeout returned no error")
	}
	if errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("timeout was classified as not sent: %v", err)
	}
}

func TestCompleteDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"redirected"}}]}`)
	}))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer front.Close()

	p, err := New(Config{Model: "m", Endpoint: front.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err == nil {
		t.Fatal("redirect returned no error")
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect target was called %d times", hits.Load())
	}
	if errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("redirect error was classified as not sent: %v", err)
	}
}
```

Add `errors`, `net`, `strings`, `sync/atomic` and `github.com/Zn070515/BountyGrid/internal/domain` (`time` and `model` are already imported).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/openai/ -run 'TestComplete' -v`

Expected: the dial-failure test fails (`New` returns no error but `Complete` returns a plain error, not `domain.ErrNotSent`); the other error tests may already pass with the Task 2 stub. That is acceptable: the failing dial test is the TDD signal.

- [ ] **Step 3: Replace the stubs with the real classifier**

In `internal/model/openai/provider.go`, replace the `classifyDoError` stub with:

```go
func classifyDoError(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %v", domain.ErrNotSent, err)
	}
	return err
}
```

Keep `snippet` as defined in Task 2. Add imports: `crypto/tls`, `crypto/x509`, `net`, `syscall`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/openai/ -v`

Expected: `PASS` for all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/model/openai/provider.go internal/model/openai/provider_test.go
git commit -m "feat(model): classify pre-send failures as ErrNotSent"
```

---

### Task 4: Ignore environment proxies

**Files:**
- Modify: `internal/model/openai/provider_test.go`

- [ ] **Step 1: Write the failing proxy test**

Append to `internal/model/openai/provider_test.go`:

```go
func TestCompleteIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"direct"}}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err != nil {
		t.Fatalf("loopback call used the environment proxy: %v", err)
	}
	if resp.Content != "direct" {
		t.Fatalf("content = %q", resp.Content)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails only if the transport is not pinned**

Run: `go test ./internal/model/openai/ -run TestCompleteIgnoresEnvironmentProxy -v`

Expected: `PASS` (Task 1 already sets `Transport: &http.Transport{Proxy: nil}`). If it fails, fix the transport before proceeding.

- [ ] **Step 3: Commit**

```bash
git add internal/model/openai/provider_test.go
git commit -m "test(model): pin that the loopback provider ignores environment proxies"
```

---

### Task 5: Store-backed policy resolver

**Files:**
- Create: `internal/model/policy_store.go`
- Create: `internal/model/policy_store_test.go`

- [ ] **Step 1: Write the failing resolver tests**

Create `internal/model/policy_store_test.go`:

```go
package model

import (
	"context"
	"errors"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type fakeProgramStore struct {
	program domain.Program
	err     error
	calls   int
}

func (f *fakeProgramStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	f.calls++
	return f.program, f.err
}

func TestStorePolicyResolverWithoutProgramIsLocalOnly(t *testing.T) {
	store := &fakeProgramStore{}
	got, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressLocalOnly {
		t.Fatalf("egress = %q, want LOCAL_ONLY", got.Egress)
	}
	if got.Redacted || got.Escalation != EscalationNone {
		t.Fatalf("policy = %+v", got)
	}
	if store.calls != 0 {
		t.Fatalf("store was consulted without a program: %d calls", store.calls)
	}
}

func TestStorePolicyResolverRequiresStoreForProgram(t *testing.T) {
	_, err := StorePolicyResolver{}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err == nil {
		t.Fatal("a program without a store must fail closed")
	}
}

func TestStorePolicyResolverUsesProgramEgress(t *testing.T) {
	store := &fakeProgramStore{program: domain.Program{
		ID: "p/1", ModelEgressPolicy: domain.ModelEgressRemoteRedactedOnly,
	}}
	got, err := StorePolicyResolver{
		Store:       store,
		Attestation: ModelPolicy{Redacted: true, Escalation: EscalationStalled},
	}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressRemoteRedactedOnly {
		t.Fatalf("egress = %q", got.Egress)
	}
	if !got.Redacted || got.Escalation != EscalationStalled {
		t.Fatalf("attestation was not applied: %+v", got)
	}
}

func TestStorePolicyResolverStoreErrorFailsClosed(t *testing.T) {
	store := &fakeProgramStore{err: errors.New("db down")}
	_, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err == nil {
		t.Fatal("store error must be returned")
	}
}

func TestStorePolicyResolverIgnoresRequestHints(t *testing.T) {
	store := &fakeProgramStore{program: domain.Program{
		ID: "p/1", ModelEgressPolicy: domain.ModelEgressLocalOnly,
	}}
	req := ModelRequest{
		ProgramID:   "p/1",
		ModelEgress: domain.ModelEgressRemoteAllowed,
		Redacted:    true,
		Escalation:  EscalationHighValueTask,
	}
	got, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressLocalOnly {
		t.Fatalf("request hint changed egress to %q", got.Egress)
	}
	if got.Redacted || got.Escalation != EscalationNone {
		t.Fatalf("request hints leaked into policy: %+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/ -run TestStorePolicyResolver -v`

Expected: build failure, `undefined: StorePolicyResolver`.

- [ ] **Step 3: Implement the resolver**

Create `internal/model/policy_store.go`:

```go
package model

import (
	"context"
	"fmt"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// ProgramStore supplies the stored program that owns a model request. The
// PostgreSQL Repo satisfies it.
type ProgramStore interface {
	GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
}

// StorePolicyResolver derives the authoritative ModelPolicy for a request. The
// egress ceiling comes from the stored Program; Redacted and Escalation are
// operator attestations, never request fields. Without a ProgramID the resolver
// fails closed to LOCAL_ONLY and does not touch the store.
type StorePolicyResolver struct {
	Store       ProgramStore
	Attestation ModelPolicy
}

func (r StorePolicyResolver) ResolveModelPolicy(ctx context.Context, req ModelRequest) (ModelPolicy, error) {
	if req.ProgramID == "" {
		return ModelPolicy{Egress: domain.ModelEgressLocalOnly}, nil
	}
	if r.Store == nil {
		return ModelPolicy{}, fmt.Errorf("%w: program store is not configured", ErrInvalidRequest)
	}
	program, err := r.Store.GetProgram(ctx, req.ProgramID)
	if err != nil {
		return ModelPolicy{}, fmt.Errorf("model: load program %s: %w", req.ProgramID, err)
	}
	escalation := r.Attestation.Escalation
	if !escalation.Valid() {
		escalation = EscalationNone
	}
	return ModelPolicy{
		Egress:     program.ModelEgressPolicy.Effective(),
		Redacted:   r.Attestation.Redacted,
		Escalation: escalation,
	}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/ -run TestStorePolicyResolver -v`

Expected: `PASS` for all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/model/policy_store.go internal/model/policy_store_test.go
git commit -m "feat(model): resolve egress from the stored program"
```

---

### Task 6: CLI flags and validation

**Files:**
- Modify: `cmd/bountygrid/model.go`
- Modify: `cmd/bountygrid/model_test.go`

- [ ] **Step 1: Write the failing parsing tests**

Append to `cmd/bountygrid/model_test.go`:

```go
func TestParseModelOptionsCallFlags(t *testing.T) {
	opts, err := parseModelOptions([]string{
		"-call",
		"-endpoint", "http://127.0.0.1:8080/v1",
		"-provider-model", "qwen3.8-flash-next-iq3_s",
		"-provider-name", "strata",
		"-prompt", "hello",
		"-timeout", "30s",
		"-call-id", "call/test/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Call || opts.Endpoint != "http://127.0.0.1:8080/v1" ||
		opts.ProviderModel != "qwen3.8-flash-next-iq3_s" || opts.ProviderName != "strata" ||
		opts.Prompt != "hello" || opts.Timeout.Seconds() != 30 || opts.CallID != "call/test/1" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseModelOptionsCallRequiresPrompt(t *testing.T) {
	if _, err := parseModelOptions([]string{"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m"}); err == nil {
		t.Fatal("-call without a prompt should fail")
	}
}

func TestParseModelOptionsCallRejectsBothPrompts(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-prompt-file", "p.txt",
	}); err == nil {
		t.Fatal("-prompt with -prompt-file should fail")
	}
}

func TestParseModelOptionsNewFlagsRequireCall(t *testing.T) {
	for _, args := range [][]string{
		{"-endpoint", "http://127.0.0.1:8080/v1"},
		{"-provider-model", "m"},
		{"-prompt", "hi"},
		{"-persist"},
		{"-timeout", "5s"},
	} {
		if _, err := parseModelOptions(args); err == nil {
			t.Fatalf("%v without -call should fail", args)
		}
	}
}

func TestParseModelOptionsProgramRequiresPersist(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-program", "vulbox/444",
	}); err == nil {
		t.Fatal("-program without -persist should fail")
	}
}

func TestParseModelOptionsCallRejectsModelEgress(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-model-egress", "REMOTE_ALLOWED",
	}); err == nil {
		t.Fatal("-model-egress with -call should fail")
	}
}

func TestParseModelOptionsCallRejectsInvalidPurpose(t *testing.T) {
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-purpose", "NOPE",
	}); err == nil {
		t.Fatal("an invalid purpose with -call should fail")
	}
}

func TestParseModelOptionsAPIKeyEnvUnset(t *testing.T) {
	t.Setenv("BOUNTYGRID_TEST_KEY", "")
	if _, err := parseModelOptions([]string{
		"-call", "-endpoint", "http://127.0.0.1:8080/v1", "-provider-model", "m",
		"-prompt", "hi", "-api-key-env", "BOUNTYGRID_TEST_KEY",
	}); err == nil {
		t.Fatal("an empty api key env should fail")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/bountygrid/ -run TestParseModelOptions -v`

Expected: failures such as `flag provided but not defined: -call`.

- [ ] **Step 3: Add the fields, flags and validation**

Modify `cmd/bountygrid/model.go`.

Add to the import block: `"time"` (`"os"`, `"flag"`, `"fmt"` and `"strings"` are already imported).

Add these fields to `modelOptions`:

```go
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
```

Inside `parseModelOptions`, after the existing `fs.StringVar(&o.Escalation, ...)` line, add:

```go
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
```

After the existing `if o.Budget < 0` check and before `return o, nil`, add:

```go
	if err := validateCallOptions(fs, o); err != nil {
		return o, err
	}
```

Add at the end of the file:

```go
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
```

`strings` and `fmt` are already imported by `model.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/bountygrid/ -run 'TestParseModelOptions|TestBuildModelPlan' -v`

Expected: `PASS` for the new parsing tests and the existing plan tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/bountygrid/model.go cmd/bountygrid/model_test.go
git commit -m "feat(cli): add model -call flags and validation"
```

---

### Task 7: Execute one real call

**Files:**
- Modify: `cmd/bountygrid/model.go`
- Create: `cmd/bountygrid/model_call.go`
- Create: `cmd/bountygrid/model_call_test.go`

- [ ] **Step 1: Write the failing call tests**

Create `cmd/bountygrid/model_call_test.go`:

```go
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newModelTestServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+content+`"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExecuteModelCallSuccess(t *testing.T) {
	srv := newModelTestServer(t, "ok")
	out, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m", ProviderName: "local-openai",
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 32, Budget: 100, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "ok" || out.Provider != "local-openai" || out.Model != "m" {
		t.Fatalf("out = %+v", out)
	}
	if out.CallID == "" || !strings.HasPrefix(out.CallID, "call/cli/") {
		t.Fatalf("call id = %q", out.CallID)
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestExecuteModelCallSensitiveLoopbackSucceeds(t *testing.T) {
	srv := newModelTestServer(t, "ok")
	out, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m",
		Prompt: "secret", Purpose: "ANALYZE", DataClass: "SENSITIVE",
		MaxTokens: 32, Budget: 100, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "ok" {
		t.Fatalf("out = %+v", out)
	}
}

func TestExecuteModelCallRejectsNonLoopback(t *testing.T) {
	_, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: "http://10.0.0.1:8080/v1", ProviderModel: "m",
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 16, Budget: 100, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("a non-loopback endpoint should fail")
	}
}

func TestExecuteModelCallPersistRequiresDatabaseURL(t *testing.T) {
	srv := newModelTestServer(t, "ok")
	t.Setenv("DATABASE_URL", "")
	_, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m", Persist: true, Program: "p/1",
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 16, Budget: 100, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("-persist without DATABASE_URL should fail")
	}
}

func TestExecuteModelCallStableCallID(t *testing.T) {
	srv := newModelTestServer(t, "ok")
	out, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m",
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 16, Budget: 100, Timeout: 5 * time.Second,
		Task: "task-1", Step: "step-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.CallID, "call/stable/") {
		t.Fatalf("call id = %q, want a stable id", out.CallID)
	}
}

func TestReadPromptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPromptFile(path)
	if err != nil || got != "hello" {
		t.Fatalf("readPromptFile = %q, %v", got, err)
	}

	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPromptFile(bad); err == nil {
		t.Fatal("invalid UTF-8 should fail")
	}

	if err := os.WriteFile(bad, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPromptFile(bad); err == nil {
		t.Fatal("an oversized prompt file should fail")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/bountygrid/ -run 'TestExecuteModelCall|TestReadPromptFile' -v`

Expected: build failure, `undefined: executeModelCall` / `undefined: readPromptFile`.

- [ ] **Step 3: Implement the call path**

Create `cmd/bountygrid/model_call.go`:

```go
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
	"github.com/Zn070515/BountyGrid/internal/model/openai"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

const maxPromptFileBytes = 1 << 20

type modelCallOutput struct {
	CallID    string       `json:"call_id"`
	Provider  string       `json:"provider"`
	Model     string       `json:"model"`
	Content   string       `json:"content"`
	Usage     model.Usage  `json:"usage"`
	Cost      domain.Money `json:"cost"`
	LatencyMS int64        `json:"latency_ms"`
}

func runModelCall(ctx context.Context, opts modelOptions) error {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	out, err := executeModelCall(ctx, opts)
	if err != nil {
		return err
	}
	enc := jsonEncoder()
	return enc.Encode(out)
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
	provider, err := openai.New(openai.Config{
		Name:     opts.ProviderName,
		Model:    opts.ProviderModel,
		Endpoint: opts.Endpoint,
		APIKey:   credential,
		Timeout:  opts.Timeout,
	})
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

	req := model.ModelRequest{
		TaskID:    domain.TaskID(strings.TrimSpace(opts.Task)),
		ProgramID: domain.ProgramID(strings.TrimSpace(opts.Program)),
		StepID:    strings.TrimSpace(opts.Step),
		Purpose:   model.Purpose(strings.ToUpper(strings.TrimSpace(opts.Purpose))),
		DataClass: model.DataClass(strings.ToUpper(strings.TrimSpace(opts.DataClass))),
		Input:     []model.Message{{Role: "user", Content: prompt}},
		MaxTokens: opts.MaxTokens,
		Budget:    domain.Money{Amount: opts.Budget, Currency: "CNY"},
		CallID:    strings.TrimSpace(opts.CallID),
	}
	if req.CallID == "" {
		if req.TaskID != "" && strings.TrimSpace(req.StepID) != "" {
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
		CallID:    req.CallID,
		Provider:  resp.Provider,
		Model:     resp.Model,
		Content:   resp.Content,
		Usage:     resp.Usage,
		Cost:      resp.Cost,
		LatencyMS: resp.Latency.Milliseconds(),
	}, nil
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
```

Modify `runModel` in `cmd/bountygrid/model.go` to dispatch:

```go
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
```

(The existing body is unchanged except for the `if opts.Call` branch.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/bountygrid/ -run 'TestExecuteModelCall|TestReadPromptFile' -v`

Expected: `PASS` for all six tests.

Run the whole package to ensure the plan path still compiles:

Run: `go test ./cmd/bountygrid/ -v`

Expected: `PASS` (the existing plan tests are unchanged).

- [ ] **Step 5: Commit**

```bash
git add cmd/bountygrid/model.go cmd/bountygrid/model_call.go cmd/bountygrid/model_call_test.go
git commit -m "feat(cli): execute one local model call"
```

---

### Task 8: Persist calls and attempts end to end

**Files:**
- Create: `cmd/bountygrid/model_call_integration_test.go`

- [ ] **Step 1: Write the failing integration test**

Create `cmd/bountygrid/model_call_integration_test.go`:

```go
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

func TestExecuteModelCallPersists(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	t.Setenv("DATABASE_URL", dsn)

	ctx := context.Background()
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer srv.Close()

	callID := "call/it/model/" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM model_calls WHERE id=$1`, callID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE event_type='model.call' AND payload->>'call_id'=$1`, callID)
	})

	out, err := executeModelCall(ctx, modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m", Persist: true,
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 16, Budget: 100, Timeout: 5 * time.Second,
		CallID: callID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.CallID != callID {
		t.Fatalf("call id = %q, want %q", out.CallID, callID)
	}

	var calls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM model_calls WHERE id=$1`, callID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model_calls rows = %d, want 1", calls)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM model_call_attempts WHERE call_id=$1`, callID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("model_call_attempts rows = %d, want 1", attempts)
	}

	var audits int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE event_type='model.call' AND payload->>'call_id'=$1`,
		callID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("model.call audit rows = %d, want 1", audits)
	}
}
```

- [ ] **Step 2: Run the test to verify it skips without a database**

Run: `go test ./cmd/bountygrid/ -run TestExecuteModelCallPersists -v`

Expected: `SKIP` with `TEST_DATABASE_URL is not set`.

- [ ] **Step 3: Run against the local database**

Set the integration DSN from `.env.local` (never print it) and run:

```powershell
$env:TEST_DATABASE_URL = (Select-String -Path .env.local -Pattern '^DATABASE_URL=(.+)$').Matches[0].Groups[1].Value.Trim('"')
go test ./cmd/bountygrid/ -run TestExecuteModelCallPersists -v
```

Expected: `PASS`; one row in each table. Remove `TEST_DATABASE_URL` afterwards.

- [ ] **Step 4: Commit**

```bash
git add cmd/bountygrid/model_call_integration_test.go
git commit -m "test(cli): persist model calls and attempts end to end"
```

---

### Task 9: Documentation

**Files:**
- Modify: `architecture/model-audit.md`
- Modify: `README.md`

- [ ] **Step 1: Add the provider section to the architecture page**

In `architecture/model-audit.md`, append a new section at the end:

```markdown
## 本地 OpenAI 兼容 Provider

`internal/model/openai` 实现一个只走 loopback 的 `model.Provider`：

- **端点**：只接受 `http(s)` 且 host 为 `127.0.0.0/8`、`::1` 或 `localhost`；base 统一为
  `scheme://host[:port]/v1`，请求路径为 `/v1/chat/completions`。
- **信任边界**：`Trust=IN_PROCESS`，因此 `SENSITIVE` 数据可以调用；非 loopback 端点
  需要在 `LOCAL_NETWORK`（trusted CIDR）或 `THIRD_PARTY`（escalation）下另行设计。
- **传输**：不跟随重定向，不使用 `HTTP_PROXY`/`HTTPS_PROXY`/PAC。
- **错误语义**：JSON marshal、URL 构造、dial/TCP 连接与 TLS 握手的失败发生在请求写出
  之前，包装 `domain.ErrNotSent`（`FAILED_BEFORE_SEND`，可安全重试）；超时、HTTP 非 200、
  响应过大或 JSON 损坏都是普通错误（`AMBIGUOUS`，不自动重试）。
- **输出**：只返回 `choices[0].message.content` 与 usage；模型输出仍然只是文本，不会
  变成 `ActionDecision` 或平台动作。

`StorePolicyResolver` 从存储的 `Program.ModelEgressPolicy` 取 ceiling；没有 `ProgramID`
时 fail closed 到 `LOCAL_ONLY`。`Redacted`/`Escalation` 是 operator attestation，永远不读
请求里的 hint。

`bountygrid model -call` 使用该 Provider：`-endpoint`/`-provider-model`（或
`BOUNTYGRID_MODEL_ENDPOINT`/`BOUNTYGRID_MODEL_NAME`）、`-prompt`/`-prompt-file`、可选
`-program -persist`。持久化时写入 `model_calls`、`model_call_attempts` 和 `model.call`
审计事件；不持久化时使用内存存储。
```

- [ ] **Step 2: Add the README implementation note**

In `README.md`, in the `### Implementation notes` list, replace the entire `- **Model** (`model`) ...` bullet (it currently ends with `no real provider;`) with:

```markdown
- **Model** (`model`): data class/provenance, program `ModelEgressPolicy`, local-first escalation, context/capability, cumulative budget, and logical `ModelCall` + `ModelCallAttempt` state; `internal/model/openai` now provides a real loopback OpenAI-compatible provider used by `model -call`; see [`architecture/harness-model-router.md`](architecture/harness-model-router.md), [`architecture/model-audit.md`](architecture/model-audit.md).
```

- [ ] **Step 3: Commit**

```bash
git add architecture/model-audit.md README.md
git commit -m "docs(model): document the local OpenAI-compatible provider"
```

---

### Task 10: Full verification

**Files:** none.

- [ ] **Step 1: Format check**

Run: `gofmt -l cmd/bountygrid internal/model`

Expected: no output. If files are listed, run `gofmt -w` on them and re-check.

- [ ] **Step 2: Tests**

Run: `go test ./...`

Expected: all packages `ok` (integration tests skip without `TEST_DATABASE_URL`).

- [ ] **Step 3: Vet**

Run: `go vet ./...`

Expected: no output, exit 0.

- [ ] **Step 4: Build**

Run: `CGO_ENABLED=0 go build ./...` (PowerShell: `$env:CGO_ENABLED='0'; go build ./...`)

Expected: exit 0.

- [ ] **Step 5: Manual smoke test against the local Strata server**

If `strata` is serving on `127.0.0.1:8080`:

```powershell
go run ./cmd/bountygrid model -call `
  -endpoint http://127.0.0.1:8080/v1 `
  -provider-model qwen3.8-flash-next-iq3_s `
  -prompt "Reply with exactly: OK" `
  -max-tokens 128
```

Expected: JSON with `"content": "OK"`, non-zero `usage`, `call_id` starting with `call/cli/`. If `content` is empty while `usage.output_tokens` is non-zero, the model spent the budget on reasoning; raise `-max-tokens` and retry with a new call ID.

- [ ] **Step 6: Commit any fixes**

```bash
git add -u
git commit -m "chore(model): verification fixes"
```

Only commit if Step 1–5 produced changes. Otherwise skip this commit.

---

## Plan self-review notes

- Every spec section maps to a task: provider (Tasks 1–4), resolver (Task 5),
  CLI flags/validation (Task 6), call execution (Task 7), persistence (Task 8),
  docs (Task 9), acceptance (Task 10).
- Error classification, endpoint normalization, proxy/redirect hardening and the
  `ErrNotSent` boundary are implemented in Tasks 1–3 and tested explicitly.
- `internal/model` remains free of `net/http`; only `internal/model/openai`
  imports it.
- The call path never registers `demoProviders()`, so plan-only catalog entries
  can never be invoked by `-call`.
