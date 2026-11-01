// Package sidecar is the browser transport used by the read-only Readers. It
// exposes open/wait/reload/eval/get/screenshot/state save/close.
//
// This is NOT a security boundary. Eval runs arbitrary JavaScript in the
// authenticated page: Readers use it for read-only UI operations (pagination,
// dismissing promotional drawers, opening rules modals, observed GET), but the
// primitive itself can mutate page state. Keep it confined to adapters; never
// hand a Client to a Harness, Model Router, or other untrusted component. The
// method names below describe the Reader's intended usage, not an enforced
// read-only guarantee.
package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrNavigationTimeout means the page did not reach network-idle in time. The
// page may still be usable; Readers should check the URL/content before
// treating the navigation as failed.
var ErrNavigationTimeout = errors.New("sidecar: navigation timed out")

// Runner executes one agent-browser command. capture selects whether stdout
// is needed; commands that start/stop the browser must not capture it because
// the browser daemon may inherit and hold the pipe.
type Runner interface {
	Run(ctx context.Context, stdin string, capture bool, args ...string) ([]byte, error)
}

type Config struct {
	Session   string
	StatePath string
	Headed    bool
	Binary    string
	Timeout   time.Duration
	// MinInterval spaces consecutive commands so a sync cannot hammer a
	// platform. Zero disables the limiter (tests).
	MinInterval time.Duration
	// Now and Sleep are injectable for the limiter's tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

type Client struct {
	runner Runner
	cfg    Config
	mu     sync.Mutex
	last   time.Time
}

func New(cfg Config, r Runner) (*Client, error) {
	if strings.TrimSpace(cfg.Session) == "" {
		return nil, fmt.Errorf("sidecar: session is empty")
	}
	if r == nil {
		binary := cfg.Binary
		if binary == "" {
			binary = "agent-browser"
		}
		r = execRunner{binary: binary}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	return &Client{runner: r, cfg: cfg}, nil
}

// NewClient uses the real agent-browser executable.
func NewClient(cfg Config) (*Client, error) { return New(cfg, nil) }

func (c *Client) Open(ctx context.Context, url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("sidecar: url is empty")
	}
	_, err := c.run(ctx, "", false, "open", url)
	if err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%w: %v", ErrNavigationTimeout, err)
		}
		return err
	}
	return nil
}

// WaitLoad is best-effort: platforms with long-polling never reach network
// idle, and the probes already tolerate that.
func (c *Client) WaitLoad(ctx context.Context) error {
	_, err := c.run(ctx, "", false, "wait", "--load", "networkidle")
	if err != nil && isTimeout(err) {
		return nil
	}
	return err
}

// WaitFn waits until a JavaScript predicate is true. Timeouts are best-effort;
// the caller's extraction decides whether the page is usable.
// Reload forces a fresh document fetch. Navigating to the same SPA hash URL
// can be a no-op, leaving the page in a stale/empty state.
func (c *Client) Reload(ctx context.Context) error {
	_, err := c.run(ctx, "", false, "reload")
	if err != nil && isTimeout(err) {
		return nil
	}
	return err
}

func (c *Client) WaitFn(ctx context.Context, expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("sidecar: wait expression is empty")
	}
	_, err := c.run(ctx, "", false, "wait", "--fn", expr)
	if err != nil && isTimeout(err) {
		return nil
	}
	return err
}

func isTimeout(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timed out") || strings.Contains(s, "timeout")
}

// Eval runs arbitrary JavaScript in the current authenticated page and returns
// its trimmed output. It is mutation-capable; adapters must restrict scripts to
// read-only observations and the specific pagination/modal controls documented
// in the Reader. No policy or sandbox is applied here.
func (c *Client) Eval(ctx context.Context, script string) ([]byte, error) {
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("sidecar: eval script is empty")
	}
	return c.run(ctx, script, true, "eval", "--stdin")
}

func (c *Client) GetURL(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "", true, "get", "url")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (c *Client) GetTitle(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "", true, "get", "title")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (c *Client) Screenshot(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("sidecar: screenshot path is empty")
	}
	_, err := c.run(ctx, "", false, "screenshot", path)
	return err
}

func (c *Client) SaveState(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("sidecar: state path is empty")
	}
	_, err := c.run(ctx, "", false, "state", "save", path)
	return err
}

func (c *Client) Close(ctx context.Context) error {
	_, err := c.run(ctx, "", false, "close")
	return err
}

func (c *Client) baseArgs() []string {
	args := []string{"--session", c.cfg.Session}
	if c.cfg.StatePath != "" {
		args = append(args, "--state", c.cfg.StatePath)
	}
	if c.cfg.Headed {
		args = append(args, "--headed")
	}
	return args
}

func (c *Client) run(ctx context.Context, stdin string, capture bool, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.waitTurnLocked(ctx); err != nil {
		return nil, err
	}
	full := append(c.baseArgs(), args...)
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	started := time.Now()
	out, err := c.runner.Run(callCtx, stdin, capture, full...)
	if err != nil {
		slog.Debug("sidecar.run", "session", c.cfg.Session, "command", args[0],
			"duration_ms", time.Since(started).Milliseconds(), "error", true)
		return nil, fmt.Errorf("sidecar: %s: %w", args[0], err)
	}
	slog.Debug("sidecar.run", "session", c.cfg.Session, "command", args[0],
		"duration_ms", time.Since(started).Milliseconds(), "error", false)
	return bytes.TrimSpace(out), nil
}

func (c *Client) waitTurnLocked(ctx context.Context) error {
	if c.cfg.MinInterval <= 0 {
		c.last = c.cfg.Now()
		return nil
	}
	if !c.last.IsZero() {
		if d := c.cfg.MinInterval - c.cfg.Now().Sub(c.last); d > 0 {
			if err := c.cfg.Sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	c.last = c.cfg.Now()
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type execRunner struct{ binary string }

func (r execRunner) Run(ctx context.Context, stdin string, capture bool, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	// The browser daemon inherits the CLI's stderr. A pipe (bytes.Buffer) would
	// stay open after the CLI exits and block Run/Output, so use a temp file.
	errFile, err := os.CreateTemp("", "bountygrid-ab-err-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(errFile.Name()) }()
	cmd.Stderr = errFile
	cmd.WaitDelay = 3 * time.Second

	var out []byte
	var runErr error
	if capture {
		out, runErr = cmd.Output()
	} else {
		// Starting/stopping the browser does not need stdout. Leave it on the
		// null device so a daemon cannot hold our pipe.
		cmd.Stdout = nil
		runErr = cmd.Run()
	}
	_ = errFile.Close()

	if runErr != nil {
		msg, _ := os.ReadFile(errFile.Name())
		if s := strings.TrimSpace(string(msg)); s != "" {
			return nil, fmt.Errorf("%w: %s", runErr, s)
		}
		return nil, runErr
	}
	return out, nil
}
