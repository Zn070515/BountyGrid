package trace

import (
	"fmt"
	"sort"
)

// Node is one event in the causal tree. Children are ordered by Seq then At.
type Node struct {
	Event    Event   `json:"event"`
	Children []*Node `json:"children,omitempty"`
}

// Trace is the reduced causal view of one JSONL file.
type Trace struct {
	TraceID string
	// Events is every input event ordered by Seq then At.
	Events []Event
	// Roots contains events with an empty or unknown ParentID, in Seq order.
	Roots []*Node
	// ByKind counts events per Kind.
	ByKind map[Kind]int
}

// Reduce builds a causally linked tree from events. It enforces that all events
// share one TraceID, IDs are non-empty and unique, and the parent relation has
// no cycles. A ParentID that does not resolve to an event makes the event a
// root, which keeps a truncated or externally merged trace readable while still
// failing closed on structural corruption (duplicate IDs, cycles).
func Reduce(events []Event) (*Trace, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("trace: no events")
	}
	ordered := make([]Event, len(events))
	copy(ordered, events)
	SortEvents(ordered)

	traceID := ordered[0].TraceID
	if traceID == "" {
		return nil, fmt.Errorf("trace: event %q has empty trace_id", ordered[0].ID)
	}
	nodes := make(map[string]*Node, len(ordered))
	byKind := make(map[Kind]int, len(ordered))
	for _, e := range ordered {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		if e.TraceID != traceID {
			return nil, fmt.Errorf("trace: event %q has trace_id %q, want %q", e.ID, e.TraceID, traceID)
		}
		if e.ID == "" {
			return nil, fmt.Errorf("trace: event with seq %d has empty id", e.Seq)
		}
		if _, dup := nodes[e.ID]; dup {
			return nil, fmt.Errorf("trace: duplicate event id %q", e.ID)
		}
		nodes[e.ID] = &Node{Event: e}
		byKind[e.Kind]++
	}

	if err := detectCycles(nodes); err != nil {
		return nil, err
	}

	roots := make([]*Node, 0)
	for _, e := range ordered {
		n := nodes[e.ID]
		if e.ParentID != "" {
			if parent, ok := nodes[e.ParentID]; ok {
				parent.Children = append(parent.Children, n)
				continue
			}
		}
		roots = append(roots, n)
	}
	SortEventsInNodes(roots)
	for _, n := range nodes {
		SortEventsInNodes(n.Children)
	}

	return &Trace{
		TraceID: traceID,
		Events:  ordered,
		Roots:   roots,
		ByKind:  byKind,
	}, nil
}

// SortEventsInNodes orders a node slice by Seq then At; it is the tree-level
// counterpart of SortEvents.
func SortEventsInNodes(nodes []*Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Event.Seq != nodes[j].Event.Seq {
			return nodes[i].Event.Seq < nodes[j].Event.Seq
		}
		return nodes[i].Event.At.Before(nodes[j].Event.At)
	})
}

func detectCycles(nodes map[string]*Node) error {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(nodes))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("trace: cycle at event %q", id)
		case done:
			return nil
		}
		state[id] = visiting
		parent := nodes[id].Event.ParentID
		if parent != "" {
			if _, ok := nodes[parent]; ok {
				if err := visit(parent); err != nil {
					return err
				}
			}
		}
		state[id] = done
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// LoadAndReduce reads a JSONL trace file and reduces it.
func LoadAndReduce(path string) (*Trace, error) {
	events, err := Read(path)
	if err != nil {
		return nil, err
	}
	return Reduce(events)
}
