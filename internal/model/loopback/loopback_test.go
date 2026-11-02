package loopback

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestDialRejectsNonLoopbackResolution(t *testing.T) {
	old := resolveNetIP
	resolveNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}
	defer func() { resolveNetIP = old }()
	if _, err := dialLoopback(context.Background(), "tcp", "localhost:1"); err == nil ||
		!strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("err = %v, want non-loopback rejection", err)
	}
}

func TestDialRejectsMixedResolution(t *testing.T) {
	old := resolveNetIP
	resolveNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.0.2.1")}, nil
	}
	defer func() { resolveNetIP = old }()
	if _, err := dialLoopback(context.Background(), "tcp", "localhost:1"); err == nil ||
		!strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("err = %v, want mixed resolution rejection", err)
	}
}

func TestDialAcceptsLoopbackResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	old := resolveNetIP
	resolveNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	defer func() { resolveNetIP = old }()

	client, err := New(Config{Endpoint: "http://localhost:" + port + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, client.Base()+"/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}
