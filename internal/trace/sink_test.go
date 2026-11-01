package trace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestSafeSink(t *testing.T) (*SafeSink, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	raw, err := OpenRawStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewSafeSink(w, raw), path
}

func TestSafeSinkAcceptsRootAndChild(t *testing.T) {
	s, path := newTestSafeSink(t)
	ctx := context.Background()
	root := Event{
		ID: "e1", TraceID: "trace-1", Kind: KindModelCall, DataClass: DataClassInternal,
		At: testAt(), Payload: json.RawMessage(`{"purpose":"ANALYZE"}`),
	}
	child := Event{
		ID: "e2", TraceID: "trace-1", ParentID: "e1", Kind: KindActionDecision,
		DataClass: DataClassPublic, At: testAt(), Payload: json.RawMessage(`{"decision":"ALLOW"}`),
	}
	if err := s.Append(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, child); err != nil {
		t.Fatal(err)
	}
	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "e1" || events[1].ParentID != "e1" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSafeSinkRequiresDataClass(t *testing.T) {
	s, _ := newTestSafeSink(t)
	s.RequireDataClass = true
	ctx := context.Background()
	base := Event{ID: "e1", TraceID: "t", Kind: KindCustom, At: testAt()}

	if err := s.Append(ctx, base); !errors.Is(err, ErrDataClassRequired) {
		t.Fatalf("missing data class err = %v, want ErrDataClassRequired", err)
	}
	invalid := base
	invalid.DataClass = DataClass("BOGUS")
	if err := s.Append(ctx, invalid); err == nil {
		t.Fatal("invalid data class must be rejected by Event.Validate")
	}
	valid := base
	valid.DataClass = DataClassInternal
	if err := s.Append(ctx, valid); err != nil {
		t.Fatal(err)
	}
}

func TestSafeSinkRejectsDanglingParent(t *testing.T) {
	s, path := newTestSafeSink(t)
	ctx := context.Background()
	child := Event{
		ID: "e2", TraceID: "trace-1", ParentID: "missing", Kind: KindCustom,
		At: testAt(), Payload: json.RawMessage(`{"ok":true}`),
	}
	if err := s.Append(ctx, child); !errors.Is(err, ErrDanglingParent) {
		t.Fatalf("dangling parent err = %v, want ErrDanglingParent", err)
	}
	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("rejected event was written: %+v", events)
	}
	s.AllowDanglingParents = true
	if err := s.Append(ctx, child); err != nil {
		t.Fatalf("AllowDanglingParents should permit the child: %v", err)
	}
}

func TestSafeSinkRejectsSecrets(t *testing.T) {
	s, path := newTestSafeSink(t)
	ctx := context.Background()
	cases := []Event{
		{ID: "phone", TraceID: "t", Kind: KindCustom, At: testAt(), Payload: json.RawMessage(`{"contact":"13800138000"}`)},
		{ID: "email", TraceID: "t", Kind: KindCustom, At: testAt(), Payload: json.RawMessage(`{"contact":"user@example.com"}`)},
	}
	for _, e := range cases {
		if err := s.Append(ctx, e); !errors.Is(err, ErrSensitiveData) {
			t.Fatalf("%s err = %v, want ErrSensitiveData", e.ID, err)
		}
	}
	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("sensitive events were written: %+v", events)
	}
}

func TestSafeSinkPutRawRejectsSecrets(t *testing.T) {
	s, _ := newTestSafeSink(t)
	ctx := context.Background()
	if _, err := s.PutRaw(ctx, []byte("caller phone 13800138000")); !errors.Is(err, ErrSensitiveData) {
		t.Fatalf("sensitive raw err = %v, want ErrSensitiveData", err)
	}
	clean := []byte("redacted evidence body")
	ref, err := s.PutRaw(ctx, clean)
	if err != nil {
		t.Fatal(err)
	}
	if ref != ContentRef(clean) {
		t.Fatalf("ref = %s, want %s", ref, ContentRef(clean))
	}
	got, err := s.Raw.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(clean) {
		t.Fatalf("raw roundtrip = %q", got)
	}
	entries, err := os.ReadDir(s.Raw.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("raw dir has %d entries, want only the clean payload", len(entries))
	}
}

func TestSafeSinkResumeSeedsExistingParents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSafeSink(w, nil)
	if err := s.Append(ctx, Event{
		ID: "e1", TraceID: "t", Kind: KindModelCall, At: testAt(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	s2 := NewSafeSink(w2, nil)
	child := Event{ID: "e2", TraceID: "t", ParentID: "e1", Kind: KindActionDecision, At: testAt()}
	if err := s2.Append(ctx, child); !errors.Is(err, ErrDanglingParent) {
		t.Fatalf("before resume = %v, want ErrDanglingParent", err)
	}
	if err := s2.Resume(path); err != nil {
		t.Fatal(err)
	}
	if err := s2.Append(ctx, child); err != nil {
		t.Fatalf("after resume = %v", err)
	}
}

func TestSafeSinkResumeValidated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSafeSink(w, nil)
	if err := s.Append(ctx, Event{ID: "e1", TraceID: "t", Kind: KindModelCall, At: testAt()}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	s2 := NewSafeSink(w2, nil)
	if err := s2.ResumeValidated(path); err != nil {
		t.Fatal(err)
	}
	if err := s2.Append(ctx, Event{ID: "e2", TraceID: "t", ParentID: "e1", Kind: KindActionDecision, At: testAt()}); err != nil {
		t.Fatal(err)
	}
}

func TestSafeSinkResumeValidatedRejectsBadHash(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "trace.jsonl")
	body := `{"id":"e1","trace_id":"t","seq":1,"kind":"CUSTOM","at":"2026-11-01T12:00:00Z","hash":"sha256:bad"}`
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := NewSafeSink(w, nil).ResumeValidated(path); err == nil {
		t.Fatal("bad hash should fail validation")
	}
}
