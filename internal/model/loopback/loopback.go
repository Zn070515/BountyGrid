// Package loopback contains the transport rules shared by local model
// providers: the endpoint must be loopback, redirects and environment proxies
// are disabled, responses are bounded, and pre-send failures are classified as
// domain.ErrNotSent.
package loopback

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/model"
)

const (
	DefaultTimeout          = 120 * time.Second
	DefaultMaxResponseBytes = int64(8 << 20)
	errorSnippetBytes       = 256
	maxModelNameBytes       = 256
	dialTimeout             = 10 * time.Second
)

// resolveNetIP is a test seam; production uses the system resolver.
var resolveNetIP = net.DefaultResolver.LookupNetIP

// ProviderConfig is the common static configuration of a loopback provider.
type ProviderConfig struct {
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

// NewProvider validates the common provider fields, builds the loopback client
// and returns the validated ProviderInfo. Message-format packages add only
// their request/response mapping on top.
func NewProvider(cfg ProviderConfig, defaultName string) (model.ProviderInfo, *Client, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = defaultName
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		return model.ProviderInfo{}, nil, fmt.Errorf("loopback: model is required")
	}
	if len(modelName) > maxModelNameBytes {
		return model.ProviderInfo{}, nil, fmt.Errorf("loopback: model name is longer than %d bytes", maxModelNameBytes)
	}
	if cfg.InputPricePerMTok < 0 || cfg.OutputPricePerMTok < 0 {
		return model.ProviderInfo{}, nil, fmt.Errorf("loopback: prices must not be negative")
	}
	if cfg.MaxContextTokens < 0 || cfg.MaxOutputTokens < 0 {
		return model.ProviderInfo{}, nil, fmt.Errorf("loopback: token limits must not be negative")
	}
	client, err := New(Config{
		Endpoint:         cfg.Endpoint,
		APIKey:           cfg.APIKey,
		Timeout:          cfg.Timeout,
		MaxResponseBytes: cfg.MaxResponseBytes,
	})
	if err != nil {
		return model.ProviderInfo{}, nil, err
	}
	return model.ProviderInfo{
		Name: name, Model: modelName, Local: true,
		MaxContextTokens:   cfg.MaxContextTokens,
		MaxOutputTokens:    cfg.MaxOutputTokens,
		InputPricePerMTok:  cfg.InputPricePerMTok,
		OutputPricePerMTok: cfg.OutputPricePerMTok,
		Endpoint:           client.Base(),
		Trust:              model.TrustInProcess,
		Strength:           cfg.Strength,
	}, client, nil
}

// Config is the transport configuration of a loopback provider.
type Config struct {
	Endpoint         string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int64
}

// Client is a validated loopback HTTP client. Providers add their own message
// mapping on top of it. The fields are unexported so a caller cannot repoint a
// validated client at a non-loopback host after Router.New accepted it.
type Client struct {
	base    string
	apiKey  string
	maxBody int64
	http    *http.Client
}

func (c *Client) Base() string   { return c.base }
func (c *Client) APIKey() string { return c.apiKey }
func (c *Client) MaxBody() int64 { return c.maxBody }

// Do sends a request through the validated loopback client.
func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.http.Do(req) }

// ReadBody reads a response body with the client's byte limit.
func (c *Client) ReadBody(r io.Reader) ([]byte, error) { return ReadLimited(r, c.maxBody) }

// New validates the transport configuration. Zero timeout and max response
// bytes use the package defaults; negative values are rejected.
func New(cfg Config) (*Client, error) {
	base, err := NormalizeBase(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("loopback: timeout must not be negative")
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody == 0 {
		maxBody = DefaultMaxResponseBytes
	}
	if maxBody < 0 {
		return nil, fmt.Errorf("loopback: max response bytes must not be negative")
	}
	return &Client{
		base:    base,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		maxBody: maxBody,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: dialLoopback,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("loopback: redirects are not followed")
			},
		},
	}, nil
}

// dialLoopback resolves the destination and refuses to connect unless every
// resolved address is loopback. It dials a validated IP directly so a second
// DNS answer cannot redirect the connection after the check.
func dialLoopback(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("loopback: invalid dial address %q: %w", addr, err)
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = resolveNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("loopback: resolve %s: %w", host, err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("loopback: %s resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !ip.Unmap().IsLoopback() {
			return nil, fmt.Errorf("loopback: %s resolved to non-loopback %s", host, ip)
		}
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	var lastErr error
	for _, ip := range ips {
		conn, derr := dialer.DialContext(ctx, network,
			net.JoinHostPort(ip.Unmap().String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	return nil, lastErr
}

// NormalizeBase validates the endpoint and returns the API base:
// scheme://host[:port]/v1 with no trailing slash.
func NormalizeBase(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", fmt.Errorf("loopback: endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("loopback: invalid endpoint %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("loopback: endpoint %q must use http or https", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("loopback: endpoint %q must not carry user info, query or fragment", raw)
	}
	if !IsLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("loopback: endpoint %q is not loopback", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	base := u.String()
	if !strings.HasSuffix(u.Path, "/v1") {
		base += "/v1"
	}
	return base, nil
}

// IsLoopbackHost reports whether host is localhost, a loopback IP literal, or
// the IPv6 loopback.
func IsLoopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.Unmap().IsLoopback()
}

// ClassifyDoError marks failures that provably happened before the request was
// written (dial, refused connection, TLS handshake) as domain.ErrNotSent. All
// other failures stay ambiguous.
func ClassifyDoError(err error) error {
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

// Snippet returns a bounded, model-safe fragment of an error body.
func Snippet(b []byte) string {
	if len(b) <= errorSnippetBytes {
		return string(b)
	}
	return string(b[:errorSnippetBytes]) + "...(truncated)"
}

// ReadLimited reads a response body with a hard byte limit.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > max {
		return nil, fmt.Errorf("loopback: response exceeds %d bytes", max)
	}
	return payload, nil
}
