package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "trace.jsonl")
}

func testAt() time.Time {
	return time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
}

func TestWriterAppendReadRoundTrip(t *testing.T) {
	path := testPath(t)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := testAt()
	want := []Event{
		{ID: "e1", TraceID: "trace-1", Kind: KindModelCall, EntityType: "model_call", EntityID: "c1", ActorKind: "MODEL", ActorID: "local-small", At: base, Payload: json.RawMessage(`{"purpose":"ANALYZE"}`)},
		{ID: "e2", TraceID: "trace-1", ParentID: "e1", Kind: KindActionDecision, EntityType: "decision", EntityID: "d1", At: base.Add(time.Second), Payload: json.RawMessage(`{"decision":"ALLOW"}`)},
		{ID: "e3", TraceID: "trace-1", ParentID: "e2", Kind: KindExecutionReceipt, EntityType: "receipt", EntityID: "r1", At: base.Add(2 * time.Second)},
	}
	for _, e := range want {
		if err := w.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(context.Background(), want[0]); err != ErrClosed {
		t.Fatalf("append after close = %v, want ErrClosed", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d events, want %d", len(got), len(want))
	}
	for i, e := range got {
		if e.ID != want[i].ID || e.ParentID != want[i].ParentID {
			t.Fatalf("event %d = %+v, want id/parent %s/%s", i, e, want[i].ID, want[i].ParentID)
		}
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.Hash == "" {
			t.Fatalf("event %d has empty hash", i)
		}
		recomputed, err := ComputeHash(e)
		if err != nil {
			t.Fatal(err)
		}
		if e.Hash != recomputed {
			t.Fatalf("event %d hash = %s, recomputed %s", i, e.Hash, recomputed)
		}
	}

	// Reopening continues the monotonic sequence instead of restarting at 1.
	w2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Append(context.Background(), Event{ID: "e4", TraceID: "trace-1", Kind: KindLedger, At: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[3].Seq != 4 {
		t.Fatalf("after reopen: %d events, last seq %d", len(got), got[len(got)-1].Seq)
	}
}

func TestWriterValidation(t *testing.T) {
	path := testPath(t)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cases := []Event{
		{ID: "", TraceID: "t", Kind: KindCustom, At: testAt()},
		{ID: "e", TraceID: "", Kind: KindCustom, At: testAt()},
		{ID: "e", TraceID: "t", Kind: "NOPE", At: testAt()},
		{ID: "e", TraceID: "t", Kind: KindCustom},
		{ID: "e", TraceID: "t", Kind: KindCustom, At: testAt(), Payload: json.RawMessage(`{"bad":`)},
	}
	for i, e := range cases {
		if err := w.Append(context.Background(), e); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestWriterAppendsAfterFileWithoutTrailingNewline(t *testing.T) {
	path := testPath(t)
	first := `{"id":"e1","trace_id":"t","seq":1,"kind":"CUSTOM","at":"2026-11-01T09:30:00Z","hash":"x"}`
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(context.Background(), Event{ID: "e2", TraceID: "t", Kind: KindCustom, At: testAt()}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "e1" || events[1].ID != "e2" {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Seq != 2 {
		t.Fatalf("seq = %d, want 2", events[1].Seq)
	}
}

func TestWriterRejectsMalformedExistingFile(t *testing.T) {
	path := testPath(t)
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("Open malformed file err = %v, want line 1 error", err)
	}
}

func TestComputeHashStable(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	a := Event{
		ID: "e1", TraceID: "trace-1", ParentID: "root", Seq: 1, Kind: KindCustom,
		EntityType: "x", EntityID: "y", ActorKind: "HUMAN", ActorID: "me",
		At:      time.Date(2026, 11, 1, 17, 30, 0, 0, zone),
		Payload: json.RawMessage(`{"b":2,"a":1}`),
	}
	b := a
	b.At = a.At.UTC()
	b.Payload = json.RawMessage("{\n  \"a\": 1,\n  \"b\": 2\n}")
	ha, err := ComputeHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ComputeHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("hash not stable across timezone/payload layout: %s vs %s", ha, hb)
	}

	c := b
	c.Payload = json.RawMessage(`{"a":1,"b":3}`)
	hc, err := ComputeHash(c)
	if err != nil {
		t.Fatal(err)
	}
	if hc == ha {
		t.Fatal("hash did not change when payload changed")
	}

	d := b
	d.Seq = 2
	hd, err := ComputeHash(d)
	if err != nil {
		t.Fatal(err)
	}
	if hd == ha {
		t.Fatal("hash did not change when seq changed")
	}
}

func TestComputeHashRejectsInvalidPayload(t *testing.T) {
	if _, err := ComputeHash(Event{ID: "e", TraceID: "t", Seq: 1, Kind: KindCustom, Payload: json.RawMessage(`{} {}`)}); err == nil {
		t.Fatal("trailing JSON data should be rejected")
	}
}

func TestRawStoreDedupAndAtomicity(t *testing.T) {
	root := t.TempDir()
	s, err := OpenRawStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []byte("redacted payload \x00 bytes")
	ref1, err := s.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ContentRef(data) {
		t.Fatalf("ref = %s, want %s", ref1, ContentRef(data))
	}
	ref2, err := s.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if ref2 != ref1 {
		t.Fatalf("dedupe ref = %s, want %s", ref2, ref1)
	}
	got, err := s.Get(ctx, ref1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}

	rawDir := filepath.Join(root, "raw")
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("raw dir has %d entries, want 1 (dedupe)", len(entries))
	}
	if strings.HasPrefix(entries[0].Name(), ".tmp-") {
		t.Fatalf("temp file left behind: %s", entries[0].Name())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(rawDir, entries[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("raw file mode = %o, want 600", perm)
		}
	}

	other := []byte("different payload")
	ref3, err := s.Put(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if ref3 == ref1 {
		t.Fatal("different content produced the same ref")
	}
	entries, err = os.ReadDir(rawDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("raw dir has %d entries, want 2", len(entries))
	}

	if _, err := s.Get(ctx, ContentRef([]byte("missing"))); err == nil {
		t.Fatal("missing payload should fail")
	}
	if _, err := s.Get(ctx, "../../etc/passwd"); err == nil {
		t.Fatal("invalid ref should fail")
	}
	if _, err := s.Get(ctx, "sha256:zz"); err == nil {
		t.Fatal("malformed ref should fail")
	}
}

func TestRawStoreContextCancellation(t *testing.T) {
	s, err := OpenRawStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, []byte("x")); err == nil {
		t.Fatal("cancelled Put should fail")
	}
	if _, err := s.Get(ctx, ContentRef([]byte("x"))); err == nil {
		t.Fatal("cancelled Get should fail")
	}
}

func TestReduceTreeRootsAndOrder(t *testing.T) {
	base := testAt()
	events := []Event{
		// Input deliberately out of seq order.
		{ID: "grand", TraceID: "trace-1", ParentID: "child", Seq: 3, Kind: KindToolDispatch, At: base.Add(2 * time.Second)},
		{ID: "root", TraceID: "trace-1", Seq: 1, Kind: KindModelCall, At: base},
		{ID: "child", TraceID: "trace-1", ParentID: "root", Seq: 2, Kind: KindActionDecision, At: base.Add(time.Second)},
		{ID: "orphan", TraceID: "trace-1", ParentID: "missing", Seq: 4, Kind: KindEvidence, At: base.Add(3 * time.Second)},
		{ID: "orphan2", TraceID: "trace-1", Seq: 5, Kind: KindFinding, At: base.Add(4 * time.Second)},
	}
	tr, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if tr.TraceID != "trace-1" {
		t.Fatalf("trace id = %q", tr.TraceID)
	}
	if len(tr.Events) != 5 {
		t.Fatalf("events = %d, want 5", len(tr.Events))
	}
	for i, e := range tr.Events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
	if len(tr.Roots) != 3 {
		t.Fatalf("roots = %d, want 3", len(tr.Roots))
	}
	if tr.Roots[0].Event.ID != "root" || tr.Roots[1].Event.ID != "orphan" || tr.Roots[2].Event.ID != "orphan2" {
		t.Fatalf("root order = %s,%s,%s", tr.Roots[0].Event.ID, tr.Roots[1].Event.ID, tr.Roots[2].Event.ID)
	}
	if len(tr.Roots[0].Children) != 1 || tr.Roots[0].Children[0].Event.ID != "child" {
		t.Fatalf("root children = %+v", tr.Roots[0].Children)
	}
	if len(tr.Roots[0].Children[0].Children) != 1 || tr.Roots[0].Children[0].Children[0].Event.ID != "grand" {
		t.Fatalf("grandchild = %+v", tr.Roots[0].Children[0].Children)
	}
	if tr.ByKind[KindModelCall] != 1 || tr.ByKind[KindActionDecision] != 1 ||
		tr.ByKind[KindToolDispatch] != 1 || tr.ByKind[KindEvidence] != 1 || tr.ByKind[KindFinding] != 1 {
		t.Fatalf("by kind = %+v", tr.ByKind)
	}
}

func TestReduceRejectsDuplicateID(t *testing.T) {
	events := []Event{
		{ID: "e", TraceID: "t", Seq: 1, Kind: KindCustom, At: testAt()},
		{ID: "e", TraceID: "t", Seq: 2, Kind: KindCustom, At: testAt()},
	}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ID err = %v", err)
	}
}

func TestReduceRejectsInvalidEvent(t *testing.T) {
	events := []Event{{ID: "e1", TraceID: "t", Seq: 1, Kind: Kind("BOGUS"), At: testAt()}}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("invalid kind err = %v", err)
	}
	events = []Event{{ID: "e1", TraceID: "t", Seq: 1, Kind: KindCustom}}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "at is zero") {
		t.Fatalf("zero time err = %v", err)
	}
	events = []Event{{ID: "e1", TraceID: "t", Seq: 1, Kind: KindCustom, At: testAt(), Payload: []byte("{")}}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("invalid payload err = %v", err)
	}
}

func TestReduceRejectsCycle(t *testing.T) {
	events := []Event{
		{ID: "e1", TraceID: "t", ParentID: "e2", Seq: 1, Kind: KindCustom, At: testAt()},
		{ID: "e2", TraceID: "t", ParentID: "e1", Seq: 2, Kind: KindCustom, At: testAt()},
	}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle err = %v", err)
	}
	self := []Event{{ID: "e1", TraceID: "t", ParentID: "e1", Seq: 1, Kind: KindCustom, At: testAt()}}
	if _, err := Reduce(self); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("self cycle err = %v", err)
	}
}

func TestReduceRejectsMixedTraceID(t *testing.T) {
	events := []Event{
		{ID: "e1", TraceID: "t1", Seq: 1, Kind: KindCustom, At: testAt()},
		{ID: "e2", TraceID: "t2", Seq: 2, Kind: KindCustom, At: testAt()},
	}
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "trace_id") {
		t.Fatalf("mixed trace err = %v", err)
	}
}

func TestReadMalformedJSONReportsLine(t *testing.T) {
	path := testPath(t)
	content := "{\"id\":\"e1\",\"trace_id\":\"t\"}\n{\"id\":\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("malformed read err = %v, want line 2", err)
	}
}

func TestReadSkipsBlankLines(t *testing.T) {
	path := testPath(t)
	content := "\n{\"id\":\"e1\",\"trace_id\":\"t\"}\n\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "e1" {
		t.Fatalf("events = %+v", events)
	}
}

func TestConcurrentAppends(t *testing.T) {
	path := testPath(t)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := w.Append(context.Background(), Event{
				ID:      fmt.Sprintf("e-%d", i),
				TraceID: "trace-1",
				Kind:    KindCustom,
				At:      testAt().Add(time.Duration(i) * time.Millisecond),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n {
		t.Fatalf("read %d events, want %d", len(events), n)
	}
	seen := make(map[int64]bool, n)
	for _, e := range events {
		if e.Seq < 1 || e.Seq > n {
			t.Fatalf("seq %d out of range", e.Seq)
		}
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	if _, err := Reduce(events); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndReduce(t *testing.T) {
	path := testPath(t)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(context.Background(), Event{ID: "e1", TraceID: "t", Kind: KindCustom, At: testAt()}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := LoadAndReduce(path)
	if err != nil {
		t.Fatal(err)
	}
	if tr.TraceID != "t" || len(tr.Roots) != 1 {
		t.Fatalf("trace = %+v", tr)
	}
}
