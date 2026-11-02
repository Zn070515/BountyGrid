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

func TestExecuteModelCallAnthropicFormat(t *testing.T) {
	var gotPath, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer srv.Close()

	out, err := executeModelCall(context.Background(), modelOptions{
		Call: true, Endpoint: srv.URL, ProviderModel: "m", ProviderFormat: "anthropic",
		Prompt: "hi", Purpose: "ANALYZE", DataClass: "INTERNAL",
		MaxTokens: 32, Budget: 100, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", gotVersion)
	}
	if out.Content != "ok" || out.Provider != "local-anthropic" {
		t.Fatalf("out = %+v", out)
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", out.Usage)
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
