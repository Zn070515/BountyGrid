package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Zn070515/BountyGrid/internal/trace"
)

type traceOptions struct {
	Command string
	File    string
}

type traceNodeSummary struct {
	ID         string             `json:"id"`
	Seq        int64              `json:"seq"`
	Kind       trace.Kind         `json:"kind"`
	EntityType string             `json:"entity_type,omitempty"`
	EntityID   string             `json:"entity_id,omitempty"`
	ActorKind  string             `json:"actor_kind,omitempty"`
	ActorID    string             `json:"actor_id,omitempty"`
	At         time.Time          `json:"at"`
	Hash       string             `json:"hash,omitempty"`
	Children   []traceNodeSummary `json:"children,omitempty"`
}

type traceReduceSummary struct {
	TraceID string             `json:"trace_id"`
	Events  int                `json:"events"`
	ByKind  map[trace.Kind]int `json:"by_kind"`
	Roots   []traceNodeSummary `json:"roots"`
}

type traceValidateSummary struct {
	TraceID string             `json:"trace_id"`
	Valid   bool               `json:"valid"`
	Events  int                `json:"events"`
	Roots   int                `json:"roots"`
	ByKind  map[trace.Kind]int `json:"by_kind"`
}

func parseTraceOptions(args []string) (traceOptions, error) {
	var o traceOptions
	if len(args) == 0 {
		return o, fmt.Errorf("trace: missing subcommand (reduce|validate)")
	}
	o.Command = args[0]
	switch o.Command {
	case "reduce", "validate":
	default:
		return o, fmt.Errorf("trace: unknown subcommand %q (want reduce|validate)", o.Command)
	}
	fs := flag.NewFlagSet("trace "+o.Command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.File, "file", "", "path to a trace JSONL file")
	if err := fs.Parse(args[1:]); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("trace: unexpected arguments %v", fs.Args())
	}
	if o.File == "" {
		return o, fmt.Errorf("trace: -file is required")
	}
	return o, nil
}

func runTrace(ctx context.Context, args []string) error {
	opts, err := parseTraceOptions(args)
	if err != nil {
		return err
	}
	return executeTrace(ctx, opts, os.Stdout)
}

func executeTrace(ctx context.Context, opts traceOptions, out io.Writer) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	t, err := trace.LoadAndReduce(opts.File)
	if err != nil {
		return err
	}
	var summary any
	switch opts.Command {
	case "reduce":
		summary = traceReduceSummary{
			TraceID: t.TraceID,
			Events:  len(t.Events),
			ByKind:  t.ByKind,
			Roots:   summarizeNodes(t.Roots),
		}
	case "validate":
		summary = traceValidateSummary{
			TraceID: t.TraceID,
			Valid:   true,
			Events:  len(t.Events),
			Roots:   len(t.Roots),
			ByKind:  t.ByKind,
		}
	default:
		return fmt.Errorf("trace: unknown subcommand %q", opts.Command)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(summary)
}

func summarizeNodes(nodes []*trace.Node) []traceNodeSummary {
	out := make([]traceNodeSummary, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, traceNodeSummary{
			ID:         n.Event.ID,
			Seq:        n.Event.Seq,
			Kind:       n.Event.Kind,
			EntityType: n.Event.EntityType,
			EntityID:   n.Event.EntityID,
			ActorKind:  n.Event.ActorKind,
			ActorID:    n.Event.ActorID,
			At:         n.Event.At,
			Hash:       n.Event.Hash,
			Children:   summarizeNodes(n.Children),
		})
	}
	return out
}
