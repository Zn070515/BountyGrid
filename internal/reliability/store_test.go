package reliability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func storedSession(t *testing.T) *ResearchSession {
	t.Helper()
	// The generated ID contains "/" ("sess/..."), which used to be rejected by
	// the file store. Keep the default so the round-trip test covers it.
	s := NewResearchSession("sess/round-trip", "task-store", testNow)
	s.ProgramID = "prog-store"
	s.Objective = "confirm the scope listing"
	s.Budget = Budget{MaxSteps: 5, StepsUsed: 1, MaxToolCalls: 4, ToolCallsUsed: 1, MaxTokens: 1000, TokensUsed: 25}

	if err := s.AddObservation(NewObservation("web_fetch", []byte(`{"url":"/scope"}`), "resp-hash",
		"scope listing returned three hosts", []domain.EvidenceID{"ev-1"}, testNow)); err != nil {
		t.Fatal(err)
	}
	fact := mustFact(t, "fact-store", LevelClaim, []domain.EvidenceID{"ev-1"})
	fact.ProgramID = "prog-store"
	if err := s.AddFact(fact); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHypothesis(Hypothesis{
		ID: "hyp-store", Statement: "the export endpoint is in scope",
		Evidence: []domain.EvidenceID{"ev-1"}, Confidence: 0.6,
		Status: HypothesisOpen, CreatedAt: testNow, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAttempt(Attempt{
		Step: 1, ToolName: "web_fetch", Action: domain.ActionReadScope,
		ArgsHash: "sha256:args", ObservationID: "obs-store",
		StartedAt: testNow, FinishedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUnknown(Unknown{ID: "unknown-store", Question: "is the export route authorized?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCandidateFinding(CandidateFinding{
		ID: "candidate-store", TaskID: "task-store", ProgramID: "prog-store",
		Title: "IDOR on export", VulnType: "IDOR", Confidence: 0.5, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFileSessionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := storedSession(t)
	if err := store.Save(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	ids, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{want.ID}) {
		t.Fatalf("list = %v, want [%s]", ids, want.ID)
	}
}

func TestFileSessionStoreListSortsAndIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sess/b", "sess/a"} {
		s := NewResearchSession(id, "task-list", testNow)
		if err := store.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"notes.txt":           "not a session",
		".session-x.tmp":      "temp",
		"session-!!!.json":    "bad base64",
		"session-.json":       "empty id",
		"session-QUJD.json":   `{"version":1}`, // decodes to "ABC" but is not listed as ours
		"unrelated-session-x": "other",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// session-QUJD.json decodes to a valid id, so List reports it; the point is
	// that foreign/un-decodable files are skipped and the order is sorted.
	want := []string{"ABC", "sess/a", "sess/b"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("list = %v, want %v", ids, want)
	}
}

func TestFileSessionStoreRejectsInvalidIDs(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewResearchSession("valid", "task-invalid", testNow)
	for _, id := range []string{"", " padded", strings.Repeat("a", maxSessionIDBytes+1), "a\x00b"} {
		s.ID = id
		if err := store.Save(t.Context(), s); err == nil {
			t.Fatalf("id %q was accepted", id)
		}
	}
}

func TestFileSessionStoreLoadErrors(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}

	s := NewResearchSession("sess-corrupt", "task-load", testNow)
	path, err := store.path(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), s.ID); err == nil {
		t.Fatal("corrupt file was accepted")
	}

	writeEnvelope := func(version int, id string) {
		raw, err := json.Marshal(sessionEnvelope{Version: version, SavedAt: testNow, Session: *s})
		if err != nil {
			t.Fatal(err)
		}
		if id != "" {
			var probe map[string]any
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatal(err)
			}
			probe["session"].(map[string]any)["ID"] = id
			raw, err = json.Marshal(probe)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnvelope(sessionSchemaVersion+1, "")
	if _, err := store.Load(t.Context(), s.ID); err == nil {
		t.Fatal("unsupported version was accepted")
	}
	writeEnvelope(sessionSchemaVersion, "sess-other")
	if _, err := store.Load(t.Context(), s.ID); err == nil {
		t.Fatal("id mismatch was accepted")
	}
}

func TestFileSessionStoreRejectsInvalidSession(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), nil); err == nil {
		t.Fatal("nil session was saved")
	}
	s := NewResearchSession("sess-invalid", "task-invalid", testNow)
	s.State = "UNKNOWN"
	if err := store.Save(t.Context(), s); err == nil {
		t.Fatal("invalid session state was saved")
	}
}

func TestFileSessionStoreHonorsContext(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewResearchSession("sess-ctx", "task-ctx", testNow)
	if err := store.Save(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatalf("save: want context.Canceled, got %v", err)
	}
	if _, err := store.Load(ctx, s.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("load: want context.Canceled, got %v", err)
	}
	if _, err := store.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("list: want context.Canceled, got %v", err)
	}
}

func TestFileSessionStoreOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewResearchSession("sess/atomic", "task-atomic", testNow)
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	s.Objective = "second version"
	s.UpdatedAt = testNow.Add(time.Minute)
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != "second version" {
		t.Fatalf("objective = %q", got.Objective)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("leftover temp file %s", entry.Name())
		}
	}
}

type failingSessionStore struct{ err error }

func (f failingSessionStore) Save(context.Context, *ResearchSession) error { return f.err }
func (f failingSessionStore) Load(context.Context, string) (*ResearchSession, error) {
	return nil, f.err
}
func (f failingSessionStore) List(context.Context) ([]string, error) { return nil, f.err }

func TestLoopCheckpointsAndPersistsSession(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.9)}, nil
	}}
	executor := &fakeExecutor{fn: func(ActionProposal) (ExecutionResult, error) {
		return ExecutionResult{Evidence: []domain.EvidenceID{"ev-loop"}}, nil
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: executor, Authorization: loopAuthorization(),
		MaxSteps: 2, Store: store, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewResearchSession("sess/persist", "task-persist", testNow)
	s.ProgramID = "prog-loop"
	s.Budget = Budget{MaxSteps: 10, MaxToolCalls: 10, MaxTokens: 100000}
	if _, err := loop.Run(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 2 || len(got.RecentObservations) != 2 {
		t.Fatalf("attempts=%d observations=%d, want 2 each", len(got.Attempts), len(got.RecentObservations))
	}
	if got.Budget.StepsUsed != 2 || got.Budget.ToolCallsUsed != 2 {
		t.Fatalf("budget = %+v", got.Budget)
	}
}

func TestLoopAbortsWhenCheckpointFails(t *testing.T) {
	boom := errors.New("disk full")
	proposer := &fakeProposer{fn: func(ProposeInput) ([]ActionProposal, error) {
		return []ActionProposal{testProposal(domain.ActionViewRules, 0.9)}, nil
	}}
	loop, err := NewLoop(LoopConfig{
		Proposer: proposer, Executor: &fakeExecutor{}, Authorization: loopAuthorization(),
		MaxSteps: 1, Store: failingSessionStore{err: boom}, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(t.Context(), loopSession(t))
	if !errors.Is(err, boom) {
		t.Fatalf("want checkpoint error, got %v", err)
	}
}

func TestResumeReopensOnlyStalledOrParked(t *testing.T) {
	s := loopSession(t)
	s.State = StateStalled
	if err := s.Resume(testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s.State != StateRunning {
		t.Fatalf("state = %s", s.State)
	}
	if err := s.Resume(testNow.Add(2 * time.Minute)); !errors.Is(err, ErrInvalidResume) {
		t.Fatalf("running session resumed: %v", err)
	}
	s.State = StateCompleted
	if err := s.Resume(testNow); !errors.Is(err, ErrInvalidResume) {
		t.Fatalf("completed session resumed: %v", err)
	}
	var nilSession *ResearchSession
	if err := nilSession.Resume(testNow); err == nil {
		t.Fatal("nil session resumed")
	}
}
