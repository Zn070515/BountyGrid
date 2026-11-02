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
