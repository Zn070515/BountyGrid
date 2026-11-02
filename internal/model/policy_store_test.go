package model

import (
	"context"
	"errors"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type fakeProgramStore struct {
	program domain.Program
	err     error
	calls   int
}

func (f *fakeProgramStore) GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error) {
	f.calls++
	return f.program, f.err
}

func TestStorePolicyResolverWithoutProgramIsLocalOnly(t *testing.T) {
	store := &fakeProgramStore{}
	got, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressLocalOnly {
		t.Fatalf("egress = %q, want LOCAL_ONLY", got.Egress)
	}
	if got.Redacted || got.Escalation != EscalationNone {
		t.Fatalf("policy = %+v", got)
	}
	if store.calls != 0 {
		t.Fatalf("store was consulted without a program: %d calls", store.calls)
	}
}

func TestStorePolicyResolverRequiresStoreForProgram(t *testing.T) {
	_, err := StorePolicyResolver{}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err == nil {
		t.Fatal("a program without a store must fail closed")
	}
}

func TestStorePolicyResolverUsesProgramEgress(t *testing.T) {
	store := &fakeProgramStore{program: domain.Program{
		ID: "p/1", ModelEgressPolicy: domain.ModelEgressRemoteRedactedOnly,
	}}
	got, err := StorePolicyResolver{
		Store:       store,
		Attestation: ModelPolicy{Redacted: true, Escalation: EscalationStalled},
	}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressRemoteRedactedOnly {
		t.Fatalf("egress = %q", got.Egress)
	}
	if !got.Redacted || got.Escalation != EscalationStalled {
		t.Fatalf("attestation was not applied: %+v", got)
	}
}

func TestStorePolicyResolverStoreErrorFailsClosed(t *testing.T) {
	store := &fakeProgramStore{err: errors.New("db down")}
	_, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), ModelRequest{ProgramID: "p/1"})
	if err == nil {
		t.Fatal("store error must be returned")
	}
}

func TestStorePolicyResolverIgnoresRequestHints(t *testing.T) {
	store := &fakeProgramStore{program: domain.Program{
		ID: "p/1", ModelEgressPolicy: domain.ModelEgressLocalOnly,
	}}
	req := ModelRequest{
		ProgramID:   "p/1",
		ModelEgress: domain.ModelEgressRemoteAllowed,
		Redacted:    true,
		Escalation:  EscalationHighValueTask,
	}
	got, err := StorePolicyResolver{Store: store}.ResolveModelPolicy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Egress != domain.ModelEgressLocalOnly {
		t.Fatalf("request hint changed egress to %q", got.Egress)
	}
	if got.Redacted || got.Escalation != EscalationNone {
		t.Fatalf("request hints leaked into policy: %+v", got)
	}
}
