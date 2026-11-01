// Package trace implements an append-only, content-addressed event log used to
// reconstruct the causal history of a research run (model calls, policy
// decisions, tool dispatches, receipts, evidence, findings, reports,
// submissions and ledger entries).
//
// The trace log complements the PostgreSQL audit/state tables; it does not
// replace them. Callers that write directly to Writer or RawStore are
// responsible for redacting secrets first. SafeSink is the recommended entry
// point: it validates the event schema, enforces an optional data class, binds
// an event to an already-seen parent, and rejects payloads/raw bytes that match
// internal/redact patterns.
//
// Files are JSONL: exactly one Event per line, opened with mode 0600. Writing is
// append-only; the package never rewrites or truncates an existing line.
package trace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind identifies the domain step an Event records.
type Kind string

const (
	KindModelCall        Kind = "MODEL_CALL"
	KindActionDecision   Kind = "ACTION_DECISION"
	KindToolDispatch     Kind = "TOOL_DISPATCH"
	KindExecutionReceipt Kind = "EXECUTION_RECEIPT"
	KindEvidence         Kind = "EVIDENCE"
	KindFinding          Kind = "FINDING"
	KindVerification     Kind = "VERIFICATION"
	KindReport           Kind = "REPORT"
	KindSubmission       Kind = "SUBMISSION"
	KindLedger           Kind = "LEDGER"
	KindCustom           Kind = "CUSTOM"
)

// Valid reports whether k is one of the known event kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindModelCall, KindActionDecision, KindToolDispatch, KindExecutionReceipt,
		KindEvidence, KindFinding, KindVerification, KindReport, KindSubmission,
		KindLedger, KindCustom:
		return true
	default:
		return false
	}
}

// DataClass labels the sensitivity of an Event. It is optional on the wire so
// existing traces remain readable, but SafeSink can require it.
type DataClass string

const (
	DataClassPublic    DataClass = "PUBLIC"
	DataClassInternal  DataClass = "INTERNAL"
	DataClassSensitive DataClass = "SENSITIVE"
)

func (d DataClass) Valid() bool {
	switch d {
	case DataClassPublic, DataClassInternal, DataClassSensitive:
		return true
	default:
		return false
	}
}

// Event is one immutable trace record. ID must be unique within a trace;
// ParentID links the event to its cause (empty means a root). Seq is assigned by
// Writer and is monotonic within a trace file. Payload must already be redacted;
// it is expected to be valid JSON when present.
type Event struct {
	ID         string          `json:"id"`
	TraceID    string          `json:"trace_id"`
	ParentID   string          `json:"parent_id,omitempty"`
	Seq        int64           `json:"seq"`
	Kind       Kind            `json:"kind"`
	DataClass  DataClass       `json:"data_class,omitempty"`
	EntityType string          `json:"entity_type,omitempty"`
	EntityID   string          `json:"entity_id,omitempty"`
	ActorKind  string          `json:"actor_kind,omitempty"`
	ActorID    string          `json:"actor_id,omitempty"`
	At         time.Time       `json:"at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Hash       string          `json:"hash"`
}

// ErrClosed is returned when Append is called after Close.
var ErrClosed = errors.New("trace: writer is closed")

// Validate rejects an event that cannot be appended to a trace log. Payload
// validity is checked here; redaction is not.
func (e Event) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("trace: event id is empty")
	}
	if strings.TrimSpace(e.TraceID) == "" {
		return fmt.Errorf("trace: event %q: trace_id is empty", e.ID)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("trace: event %q: invalid kind %q", e.ID, e.Kind)
	}
	if e.DataClass != "" && !e.DataClass.Valid() {
		return fmt.Errorf("trace: event %q: invalid data class %q", e.ID, e.DataClass)
	}
	if e.At.IsZero() {
		return fmt.Errorf("trace: event %q: at is zero", e.ID)
	}
	if len(bytes.TrimSpace(e.Payload)) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("trace: event %q: payload is not valid json", e.ID)
	}
	return nil
}

// ComputeHash returns a stable content hash over the event's key fields and
// canonicalized payload. Timestamps are normalized to UTC and JSON object keys
// are sorted, so the same event hash survives a JSON round-trip. The Seq field
// is included because the writer assigns it before hashing.
func ComputeHash(e Event) (string, error) {
	payload, err := canonicalJSON(e.Payload)
	if err != nil {
		return "", fmt.Errorf("trace: event %q: %w", e.ID, err)
	}
	input := struct {
		ID         string          `json:"id"`
		TraceID    string          `json:"trace_id"`
		ParentID   string          `json:"parent_id"`
		Seq        int64           `json:"seq"`
		Kind       Kind            `json:"kind"`
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		ActorKind  string          `json:"actor_kind"`
		ActorID    string          `json:"actor_id"`
		At         string          `json:"at"`
		Payload    json.RawMessage `json:"payload,omitempty"`
		// Appended with omitempty so hashes of existing events without a data
		// class remain stable, while adding a class changes the hash.
		DataClass DataClass `json:"data_class,omitempty"`
	}{
		ID:         e.ID,
		TraceID:    e.TraceID,
		ParentID:   e.ParentID,
		Seq:        e.Seq,
		Kind:       e.Kind,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		ActorKind:  e.ActorKind,
		ActorID:    e.ActorID,
		At:         e.At.UTC().Format(time.RFC3339Nano),
		Payload:    payload,
		DataClass:  e.DataClass,
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("trace: event %q: hash input: %w", e.ID, err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// canonicalJSON decodes raw JSON and re-encodes it with sorted object keys and
// no insignificant whitespace. An empty payload stays empty. A non-JSON or
// multi-value payload is an error.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("payload is not valid json: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("payload contains trailing data")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}
	return b, nil
}

// Writer appends Events to a JSONL file. A Writer is safe for concurrent use;
// Seq assignment and the underlying write happen under one mutex.
type Writer struct {
	mu          sync.Mutex
	f           *os.File
	path        string
	seq         int64
	needNewline bool
	closed      bool
}

// Open opens (creating if needed) the trace file for append and continues the
// sequence after the last stored event. An existing malformed file is rejected
// rather than silently appended to.
func Open(path string) (*Writer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("trace: path is empty")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("trace: open %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("trace: chmod %s: %w", path, err)
	}
	var next int64
	needNewline := false
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		events, readErr := Read(path)
		if readErr != nil {
			f.Close()
			return nil, readErr
		}
		for _, e := range events {
			if e.Seq > next {
				next = e.Seq
			}
		}
		endsNL, nlErr := endsWithNewline(path)
		if nlErr != nil {
			f.Close()
			return nil, nlErr
		}
		needNewline = !endsNL
	} else if statErr != nil {
		f.Close()
		return nil, fmt.Errorf("trace: stat %s: %w", path, statErr)
	}
	return &Writer{f: f, path: path, seq: next, needNewline: needNewline}, nil
}

// endsWithNewline reports whether a non-empty file's last byte is '\n'. A
// complete line without a trailing newline is valid JSONL, but the next append
// must add the separator itself.
func endsWithNewline(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("trace: open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("trace: stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return true, nil
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return false, fmt.Errorf("trace: seek %s: %w", path, err)
	}
	var b [1]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false, fmt.Errorf("trace: read tail %s: %w", path, err)
	}
	return b[0] == '\n', nil
}

// Append validates e, assigns the next Seq, canonicalizes the payload, computes
// Hash and writes exactly one JSON line. Appends are not fsynced; Close syncs
// and reports flush/close errors.
func (w *Writer) Append(ctx context.Context, e Event) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := e.Validate(); err != nil {
		return err
	}
	payload, err := canonicalJSON(e.Payload)
	if err != nil {
		return fmt.Errorf("trace: event %q: %w", e.ID, err)
	}
	e.Payload = payload

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	w.seq++
	e.Seq = w.seq
	e.At = e.At.UTC()
	hash, err := ComputeHash(e)
	if err != nil {
		return err
	}
	e.Hash = hash
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("trace: event %q: marshal: %w", e.ID, err)
	}
	if w.needNewline {
		line = append([]byte{'\n'}, line...)
		w.needNewline = false
	}
	line = append(line, '\n')
	if _, err := w.f.Write(line); err != nil {
		return fmt.Errorf("trace: write %s: %w", w.path, err)
	}
	return nil
}

// Close flushes and closes the file. It is safe to call more than once.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	if err := w.f.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("trace: sync %s: %w", w.path, err))
	}
	if err := w.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("trace: close %s: %w", w.path, err))
	}
	return errors.Join(errs...)
}

// Read parses a JSONL trace file. Blank lines are ignored. Malformed JSON is
// reported with its 1-based line number.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("trace: read %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var events []Event
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("trace: %s line %d: invalid json: %w", path, line, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("trace: %s line %d: %w", path, line+1, err)
	}
	return events, nil
}

// SortEvents orders events by Seq then At using a stable sort, so equal keys
// keep their input order.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Seq != events[j].Seq {
			return events[i].Seq < events[j].Seq
		}
		return events[i].At.Before(events[j].At)
	})
}
