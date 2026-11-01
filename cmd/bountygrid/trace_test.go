package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/trace"
)

func writeTestTrace(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := trace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	events := []trace.Event{
		{ID: "e1", TraceID: "trace-cli", Kind: trace.KindModelCall, EntityType: "model_call", EntityID: "c1", At: base, Payload: json.RawMessage(`{"purpose":"PLAN"}`)},
		{ID: "e2", TraceID: "trace-cli", ParentID: "e1", Kind: trace.KindActionDecision, EntityType: "decision", EntityID: "d1", At: base.Add(time.Second), Payload: json.RawMessage(`{"decision":"ALLOW"}`)},
		{ID: "e3", TraceID: "trace-cli", ParentID: "e2", Kind: trace.KindToolDispatch, EntityType: "tool", EntityID: "t1", At: base.Add(2 * time.Second)},
		{ID: "e4", TraceID: "trace-cli", Kind: trace.KindEvidence, EntityType: "evidence", EntityID: "ev1", At: base.Add(3 * time.Second)},
	}
	for _, e := range events {
		if err := w.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseTraceOptions(t *testing.T) {
	opts, err := parseTraceOptions([]string{"reduce", "-file", "trace.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Command != "reduce" || opts.File != "trace.jsonl" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseTraceOptions([]string{"validate", "-file", "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTraceOptions(nil); err == nil {
		t.Fatal("missing subcommand should fail")
	}
	if _, err := parseTraceOptions([]string{"bogus", "-file", "x"}); err == nil {
		t.Fatal("unknown subcommand should fail")
	}
	if _, err := parseTraceOptions([]string{"reduce"}); err == nil {
		t.Fatal("missing -file should fail")
	}
	if _, err := parseTraceOptions([]string{"reduce", "-file", "x", "extra"}); err == nil {
		t.Fatal("extra args should fail")
	}
}

func TestExecuteTraceReduce(t *testing.T) {
	path := writeTestTrace(t)
	var buf bytes.Buffer
	if err := executeTrace(context.Background(), traceOptions{Command: "reduce", File: path}, &buf); err != nil {
		t.Fatal(err)
	}
	var got struct {
		TraceID string         `json:"trace_id"`
		Events  int            `json:"events"`
		ByKind  map[string]int `json:"by_kind"`
		Roots   []struct {
			ID       string `json:"id"`
			Children []struct {
				ID       string `json:"id"`
				Children []struct {
					ID string `json:"id"`
				} `json:"children"`
			} `json:"children"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("bad json %s: %v", buf.String(), err)
	}
	if got.TraceID != "trace-cli" || got.Events != 4 {
		t.Fatalf("summary = %+v", got)
	}
	if got.ByKind["MODEL_CALL"] != 1 || got.ByKind["TOOL_DISPATCH"] != 1 {
		t.Fatalf("by_kind = %+v", got.ByKind)
	}
	if len(got.Roots) != 2 || got.Roots[0].ID != "e1" || got.Roots[1].ID != "e4" {
		t.Fatalf("roots = %+v", got.Roots)
	}
	if len(got.Roots[0].Children) != 1 || got.Roots[0].Children[0].ID != "e2" {
		t.Fatalf("children = %+v", got.Roots[0].Children)
	}
	if len(got.Roots[0].Children[0].Children) != 1 || got.Roots[0].Children[0].Children[0].ID != "e3" {
		t.Fatalf("grandchildren = %+v", got.Roots[0].Children[0].Children)
	}
}

func TestExecuteTraceValidate(t *testing.T) {
	path := writeTestTrace(t)
	var buf bytes.Buffer
	if err := executeTrace(context.Background(), traceOptions{Command: "validate", File: path}, &buf); err != nil {
		t.Fatal(err)
	}
	var got struct {
		TraceID string         `json:"trace_id"`
		Valid   bool           `json:"valid"`
		Events  int            `json:"events"`
		Roots   int            `json:"roots"`
		ByKind  map[string]int `json:"by_kind"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("bad json %s: %v", buf.String(), err)
	}
	if !got.Valid || got.TraceID != "trace-cli" || got.Events != 4 || got.Roots != 2 {
		t.Fatalf("summary = %+v", got)
	}
	if got.ByKind["ACTION_DECISION"] != 1 {
		t.Fatalf("by_kind = %+v", got.ByKind)
	}
}

func TestExecuteTraceRejectsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := executeTrace(context.Background(), traceOptions{Command: "reduce", File: path}, &buf); err == nil {
		t.Fatal("malformed trace should fail")
	}
}

func TestRunTrace(t *testing.T) {
	path := writeTestTrace(t)
	// runTrace writes to os.Stdout; this exercises the option parsing and
	// dispatch path without asserting on process output.
	if err := runTrace(context.Background(), []string{"validate", "-file", path}); err != nil {
		t.Fatal(err)
	}
}
