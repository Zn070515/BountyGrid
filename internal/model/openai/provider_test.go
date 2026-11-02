package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
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
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
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
	if resp.StopReason != "stop" || resp.Truncated {
		t.Fatalf("stop=%q truncated=%v", resp.StopReason, resp.Truncated)
	}
}

func TestCompleteLengthIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`)
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
		t.Fatal(err)
	}
	if resp.StopReason != "length" || !resp.Truncated {
		t.Fatalf("stop=%q truncated=%v", resp.StopReason, resp.Truncated)
	}
}

func TestCompleteRejectsOutOfRangeTemperature(t *testing.T) {
	p, err := New(Config{Model: "m", Endpoint: "http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatal(err)
	}
	temperature := 2.5
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
		Temperature: &temperature,
	})
	if !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("err = %v, want ErrNotSent", err)
	}
}

func TestCompleteRejectsThinkingBudgetBeforeSend(t *testing.T) {
	p, err := New(Config{Model: "m", Endpoint: "http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
		ThinkingBudget: 1024,
	})
	if !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("err = %v, want ErrNotSent", err)
	}
}

func TestCompleteSendsThinkingAndTemperature(t *testing.T) {
	var got struct {
		Temperature        *float64       `json:"temperature"`
		ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	temperature := 0.2
	if _, err := p.Complete(context.Background(), model.ModelRequest{
		Input:       []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   8,
		Thinking:    model.ThinkingOff,
		Temperature: &temperature,
	}); err != nil {
		t.Fatal(err)
	}
	if got.Temperature == nil || *got.Temperature != 0.2 {
		t.Fatalf("temperature = %v", got.Temperature)
	}
	if got.ChatTemplateKwargs["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs = %+v", got.ChatTemplateKwargs)
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
