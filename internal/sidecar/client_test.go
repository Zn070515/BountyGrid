package sidecar

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	args    []string
	stdin   string
	capture bool
}

type fakeRunner struct {
	mu    sync.Mutex
	calls []call
	out   []byte
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, stdin string, capture bool, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{args: append([]string{}, args...), stdin: stdin, capture: capture})
	return f.out, f.err
}

func (f *fakeRunner) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return call{}
	}
	return f.calls[len(f.calls)-1]
}

func newTestClient(t *testing.T, r Runner, cfg Config) *Client {
	t.Helper()
	c, err := New(cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRequiresSession(t *testing.T) {
	if _, err := New(Config{}, &fakeRunner{}); err == nil {
		t.Fatal("empty session should be rejected")
	}
}

func TestOpenBuildsSessionAndStateArgs(t *testing.T) {
	f := &fakeRunner{out: []byte("ok\n")}
	c := newTestClient(t, f, Config{Session: "s1", StatePath: "state.json", MinInterval: 0})
	if err := c.Open(context.Background(), "https://example.test/x"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.last().args, " ")
	want := "--session s1 --state state.json open https://example.test/x"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if f.last().capture {
		t.Fatal("open must not capture stdout")
	}
}

func TestEvalPassesScriptOnStdinAndTrimsOutput(t *testing.T) {
	f := &fakeRunner{out: []byte("  \"ok\"\n")}
	c := newTestClient(t, f, Config{Session: "s1", MinInterval: 0})
	got, err := c.Eval(context.Background(), "return 1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"ok"` {
		t.Fatalf("output = %q, want %q", got, `"ok"`)
	}
	last := f.last()
	if last.stdin != "return 1" {
		t.Fatalf("stdin = %q", last.stdin)
	}
	if !strings.HasSuffix(strings.Join(last.args, " "), "eval --stdin") {
		t.Fatalf("args = %v", last.args)
	}
	if !last.capture {
		t.Fatal("eval must capture stdout")
	}
}

func TestEvalRejectsEmptyScript(t *testing.T) {
	c := newTestClient(t, &fakeRunner{}, Config{Session: "s1"})
	if _, err := c.Eval(context.Background(), "   "); err == nil {
		t.Fatal("empty script should be rejected")
	}
}

func TestGetURLAndTitle(t *testing.T) {
	f := &fakeRunner{out: []byte("https://example.test/page\n")}
	c := newTestClient(t, f, Config{Session: "s1", MinInterval: 0})
	got, err := c.GetURL(context.Background())
	if err != nil || got != "https://example.test/page" {
		t.Fatalf("url = %q, err=%v", got, err)
	}
	f.out = []byte("Example\n")
	got, err = c.GetTitle(context.Background())
	if err != nil || got != "Example" {
		t.Fatalf("title = %q, err=%v", got, err)
	}
}

func TestSaveStateRequiresPath(t *testing.T) {
	c := newTestClient(t, &fakeRunner{}, Config{Session: "s1"})
	if err := c.SaveState(context.Background(), ""); err == nil {
		t.Fatal("empty state path should be rejected")
	}
}

func TestRunnerErrorIsWrapped(t *testing.T) {
	f := &fakeRunner{err: errors.New("boom")}
	c := newTestClient(t, f, Config{Session: "s1", MinInterval: 0})
	err := c.Open(context.Background(), "https://example.test")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want wrapped runner error", err)
	}
}

func TestRateLimitSleepsBetweenCommands(t *testing.T) {
	f := &fakeRunner{}
	var slept []time.Duration
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	sleep := func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}
	c := newTestClient(t, f, Config{
		Session: "s1", MinInterval: 2 * time.Second,
		Now: func() time.Time { return now }, Sleep: sleep,
	})
	if _, err := c.GetURL(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetTitle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept = %v, want one 2s wait", slept)
	}
}

func TestCloseUsesFixedCommand(t *testing.T) {
	f := &fakeRunner{}
	c := newTestClient(t, f, Config{Session: "s1", MinInterval: 0})
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.last().args, " "); got != "--session s1 close" {
		t.Fatalf("args = %q", got)
	}
}
