package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/adapter"
	"github.com/Zn070515/BountyGrid/internal/domain"
)

type fakeReader struct {
	programs    adapter.ProgramBatch
	programsErr error
	account     adapter.AccountSnapshot
	accountErr  error
	program     adapter.ProgramSnapshot
	programErr  error
	policy      adapter.PolicySnapshot
	policyErr   error
}

func (f *fakeReader) Platform() domain.Platform { return domain.PlatformVulbox }
func (f *fakeReader) AuthStatus(context.Context) (adapter.AuthState, error) {
	return adapter.AuthOK, nil
}
func (f *fakeReader) SyncAccount(context.Context) (adapter.AccountSnapshot, error) {
	return f.account, f.accountErr
}
func (f *fakeReader) SyncPrograms(context.Context, adapter.SyncOptions) (adapter.ProgramBatch, error) {
	return f.programs, f.programsErr
}
func (f *fakeReader) SyncProgram(context.Context, domain.ProgramID) (adapter.ProgramSnapshot, error) {
	return f.program, f.programErr
}
func (f *fakeReader) SyncScope(context.Context, domain.ProgramID) (adapter.ScopeSnapshot, error) {
	return adapter.ScopeSnapshot{}, adapter.ErrUnsupported
}
func (f *fakeReader) SyncPolicy(context.Context, domain.ProgramID) (adapter.PolicySnapshot, error) {
	return f.policy, f.policyErr
}

type fakeWriter struct {
	evidence []domain.Evidence
	facts    []domain.ObservedFact
	accounts []domain.Account
	programs []domain.Program
	err      error
}

func (w *fakeWriter) PutEvidence(ctx context.Context, e domain.Evidence) error {
	if w.err != nil {
		return w.err
	}
	w.evidence = append(w.evidence, e)
	return nil
}
func (w *fakeWriter) PutFacts(ctx context.Context, fs []domain.ObservedFact) error {
	if w.err != nil {
		return w.err
	}
	w.facts = append(w.facts, fs...)
	return nil
}
func (w *fakeWriter) UpsertAccount(ctx context.Context, a domain.Account) error {
	if w.err != nil {
		return w.err
	}
	w.accounts = append(w.accounts, a)
	return nil
}
func (w *fakeWriter) UpsertProgram(ctx context.Context, p domain.Program) error {
	if w.err != nil {
		return w.err
	}
	w.programs = append(w.programs, p)
	return nil
}

func TestExecuteSyncPersistsPrograms(t *testing.T) {
	f := &fakeReader{programs: adapter.ProgramBatch{
		Programs: []domain.Program{{ID: "vulbox/444"}},
		Facts:    []domain.ObservedFact{{ID: "f1"}},
		Evidence: []domain.Evidence{{ID: "ev1"}},
	}}
	w := &fakeWriter{}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), f, syncOptions{Programs: true}, &buf, w); err != nil {
		t.Fatal(err)
	}
	if len(w.evidence) != 1 || len(w.facts) != 1 || len(w.programs) != 1 {
		t.Fatalf("persisted = %d/%d/%d", len(w.evidence), len(w.facts), len(w.programs))
	}
	var out syncOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Persisted {
		t.Fatal("output should report persisted=true")
	}
}

func TestExecuteSyncPersistsAccount(t *testing.T) {
	f := &fakeReader{account: adapter.AccountSnapshot{
		Account:  domain.Account{ID: "vulbox/account"},
		Facts:    []domain.ObservedFact{{ID: "f1"}},
		Evidence: []domain.Evidence{{ID: "ev1"}},
	}}
	w := &fakeWriter{}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), f, syncOptions{Account: true}, &buf, w); err != nil {
		t.Fatal(err)
	}
	if len(w.accounts) != 1 || len(w.facts) != 1 || len(w.evidence) != 1 {
		t.Fatalf("persisted = %+v", w)
	}
}

func TestExecuteSyncPersistErrorSurfaces(t *testing.T) {
	f := &fakeReader{programs: adapter.ProgramBatch{Evidence: []domain.Evidence{{ID: "ev1"}}}}
	w := &fakeWriter{err: errors.New("db down")}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), f, syncOptions{Programs: true}, &buf, w); err == nil {
		t.Fatal("persist error should surface")
	}
}

func TestExecuteSyncPrograms(t *testing.T) {
	prog := domain.Program{ID: "vulbox/444", Name: "联想", Type: domain.TypeEnterpriseSRC}
	prog.Status.Status = domain.StatusRunning
	prog.Eligibility.Decision = domain.Eligible
	f := &fakeReader{programs: adapter.ProgramBatch{
		Programs:       []domain.Program{prog},
		Facts:          []domain.ObservedFact{{ID: "f1"}, {ID: "f2"}},
		Evidence:       []domain.Evidence{{ID: "ev1"}},
		ObservedCount:  1,
		EstimatedTotal: 1513,
		Estimated:      true,
	}}
	var buf bytes.Buffer
	err := executeSync(context.Background(), f, syncOptions{Programs: true}, &buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out syncOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Programs == nil || out.Programs.Count != 1 || out.Programs.EstimatedTotal != 1513 || !out.Programs.Estimated {
		t.Fatalf("programs output = %+v", out.Programs)
	}
	if out.Programs.Facts != 2 || len(out.Evidence) != 1 || out.Evidence[0] != "ev1" {
		t.Fatalf("facts/evidence = %+v %v", out.Programs, out.Evidence)
	}
	if strings.Contains(buf.String(), "task_title") {
		t.Fatal("output must not dump raw platform payloads")
	}
}

func TestExecuteSyncAccount(t *testing.T) {
	identity, bank := true, false
	acct := domain.Account{ID: "vulbox/account", Platform: domain.PlatformVulbox, Level: "见习白帽", IdentityVerified: &identity, BankBound: &bank}
	acct.Capability.CanSubmit = true
	f := &fakeReader{account: adapter.AccountSnapshot{Account: acct, Facts: []domain.ObservedFact{{ID: "f1"}}}}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), f, syncOptions{Account: true}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	var out syncOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Account == nil || out.Account.ID != "vulbox/account" || !out.Account.CanSubmit || out.Account.CanPayout {
		t.Fatalf("account output = %+v", out.Account)
	}
}

func TestExecuteSyncProgramDetail(t *testing.T) {
	prog := domain.Program{ID: "vulbox/444", Name: "联想", Type: domain.TypeEnterpriseSRC}
	prog.Status.Status = domain.StatusRunning
	prog.Eligibility.Decision = domain.Eligible
	snap := adapter.ProgramSnapshot{
		Program: prog,
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted, ContentsKnown: true,
			Include: []domain.Asset{{Value: "*.lenovo.com"}},
			Exclude: []domain.Asset{{Value: "*.sunits.com"}},
		},
		Facts:    []domain.ObservedFact{{Predicate: "vulbox.scope.exclude_notes", Value: []string{"安想、智慧医疗"}}},
		Evidence: []domain.Evidence{{ID: "evp"}},
	}
	pol := adapter.PolicySnapshot{
		Automation: domain.AutomationPolicy{
			FileUpload: domain.CapForbidden, AIAnalysis: domain.CapConditional,
			HumanVerificationRequired: true,
		},
		Facts:    []domain.ObservedFact{{ID: "fp"}},
		Evidence: []domain.Evidence{{ID: "evpol"}},
	}
	f := &fakeReader{program: snap, policy: pol}
	var buf bytes.Buffer
	if err := executeSync(context.Background(), f, syncOptions{ProgramID: "444"}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	var out syncOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Detail == nil || out.Detail.ID != "vulbox/444" || !out.Detail.ContentsKnown {
		t.Fatalf("detail = %+v", out.Detail)
	}
	if len(out.Detail.Include) != 1 || out.Detail.Include[0] != "*.lenovo.com" {
		t.Fatalf("include = %v", out.Detail.Include)
	}
	if len(out.Detail.Exclude) != 1 || out.Detail.Exclude[0] != "*.sunits.com" {
		t.Fatalf("exclude = %v", out.Detail.Exclude)
	}
	if out.Detail.FileUpload != "FORBIDDEN" || out.Detail.AIAnalysis != "CONDITIONAL" {
		t.Fatalf("policy = %+v", out.Detail)
	}
	if len(out.Evidence) != 2 {
		t.Fatalf("evidence = %v", out.Evidence)
	}
}

func TestParseSyncOptionsAllowsProgramOnly(t *testing.T) {
	opts, err := parseSyncOptions([]string{"-programs=false", "-program", "444"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ProgramID != "444" {
		t.Fatalf("program = %q", opts.ProgramID)
	}
}

func TestExecuteSyncSurfacesAuthRequired(t *testing.T) {
	f := &fakeReader{accountErr: adapter.ErrAuthRequired}
	var buf bytes.Buffer
	err := executeSync(context.Background(), f, syncOptions{Account: true}, &buf, nil)
	if !errors.Is(err, adapter.ErrAuthRequired) {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
}

func TestParseSyncOptionsRejectsEmptySelection(t *testing.T) {
	if _, err := parseSyncOptions([]string{"-programs=false", "-account=false"}); err == nil {
		t.Fatal("empty selection should be rejected")
	}
}

func TestParseSyncOptionsDefaults(t *testing.T) {
	opts, err := parseSyncOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Programs || opts.Account || opts.Pages != 1 || opts.PageSize != 50 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.MinInterval != 2*time.Second {
		t.Fatalf("min interval = %v", opts.MinInterval)
	}
	if opts.Close {
		t.Fatal("sync must keep the login session alive by default")
	}
	if opts.Platform != "vulbox" || opts.Session != "vbprobe" || opts.RawDir != ".evidence/vulbox" {
		t.Fatalf("defaults = %+v", opts)
	}
	if opts.Persist {
		t.Fatal("persist must be opt-in")
	}
}

func TestParseSyncOptionsXianzhi(t *testing.T) {
	opts, err := parseSyncOptions([]string{"-platform", "xianzhi", "-pages", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Session != "xzprobe" || opts.RawDir != ".evidence/xianzhi" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseSyncOptionsButian(t *testing.T) {
	opts, err := parseSyncOptions([]string{"-platform", "butian"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Session != "btprobe" || opts.RawDir != ".evidence/butian" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseSyncOptionsRejectsUnknownPlatform(t *testing.T) {
	if _, err := parseSyncOptions([]string{"-platform", "unknown"}); err == nil {
		t.Fatal("unknown platform should be rejected")
	}
}
