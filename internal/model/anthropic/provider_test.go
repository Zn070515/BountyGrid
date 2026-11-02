package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

func TestNewRejectsNonLoopback(t *testing.T) {
	for _, endpoint := range []string{"http://10.0.0.1:8080/v1", "http://example.com/v1"} {
		if _, err := New(Config{Model: "m", Endpoint: endpoint}); err == nil {
			t.Fatalf("New(%q) succeeded, want a non-loopback error", endpoint)
		}
	}
}

func TestNewAcceptsLoopbackAndNormalizesBase(t *testing.T) {
	p, err := New(Config{Model: "m", Endpoint: "http://127.0.0.1:8080/"})
	if err != nil {
		t.Fatal(err)
	}
	info := p.Info()
	if info.Endpoint != "http://127.0.0.1:8080/v1" {
		t.Fatalf("endpoint = %q", info.Endpoint)
	}
	if info.Trust != model.TrustInProcess || !info.Local || info.Name != defaultName {
		t.Fatalf("info = %+v", info)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cases := []Config{
		{Model: "", Endpoint: "http://127.0.0.1:8080/v1"},
		{Model: "m", Endpoint: "ftp://127.0.0.1:8080/v1"},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", Timeout: -time.Second},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", MaxResponseBytes: -1},
		{Model: "m", Endpoint: "http://127.0.0.1:8080/v1", OutputPricePerMTok: -1},
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d: New accepted an invalid config", i)
		}
	}
}

func TestCompleteSuccess(t *testing.T) {
	var gotPath, gotVersion, gotKey string
	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","content":[{"type":"text","text":"hello"},{"type":"thinking","text":"ignored"},{"type":"text","text":" world"}],"usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer srv.Close()

	p, err := New(Config{Model: "m", Endpoint: srv.URL, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotVersion != anthropicVersion {
		t.Fatalf("anthropic-version = %q", gotVersion)
	}
	if gotKey != "secret" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if got.Model != "m" || got.MaxTokens != 16 || got.System != "be concise" {
		t.Fatalf("body = %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if resp.Content != "hello world" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Provider != defaultName || resp.Model != "m" {
		t.Fatalf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
}

func TestCompleteMergesConsecutiveRoles(t *testing.T) {
	var got struct {
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()

	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{
			{Role: "system", Content: "S1"},
			{Role: "user", Content: "a"},
			{Role: "user", Content: "b"},
			{Role: "assistant", Content: "c"},
			{Role: "system", Content: "S2"},
		},
		MaxTokens: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.System != "S1\n\nS2" {
		t.Fatalf("system = %q", got.System)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content != "a\n\nb" || got.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", got.Messages)
	}
}

func TestCompleteRejectsUnsupportedRole(t *testing.T) {
	p, err := New(Config{Model: "m", Endpoint: "http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "tool", Content: "x"}}, MaxTokens: 4,
	})
	if !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("err = %v, want ErrNotSent", err)
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
	if err == nil || errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("err = %v, want an ambiguous HTTP error", err)
	}
}

func TestCompleteDialFailureIsNotSent(t *testing.T) {
	p, err := New(Config{Model: "m", Endpoint: "http://127.0.0.1:1/v1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if !errors.Is(err, domain.ErrNotSent) {
		t.Fatalf("err = %v, want ErrNotSent", err)
	}
}

func TestCompleteOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"`+strings.Repeat("x", 100)+`"}]}`)
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
		t.Fatalf("err = %v, want a size error", err)
	}
}

func TestCompleteIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"direct"}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{Model: "m", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), model.ModelRequest{
		Input: []model.Message{{Role: "user", Content: "hi"}}, MaxTokens: 4,
	})
	if err != nil || resp.Content != "direct" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
}
