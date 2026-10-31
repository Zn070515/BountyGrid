package evidence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func sampleEvidence(id domain.EvidenceID) domain.Evidence {
	return domain.Evidence{
		ID:          id,
		Platform:    domain.PlatformButian,
		SourceType:  domain.SourceProgram,
		Transport:   domain.TransportBrowserPage,
		URL:         "https://www.butian.net/Grow",
		PageTitle:   "GROW",
		CapturedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		ContentHash: "sha256:test",
		RawRef:      "local://test",
	}
}

func TestMemoryStoreImmutable(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	e := sampleEvidence("e1")
	if err := s.Put(ctx, e); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URL != e.URL {
		t.Fatalf("got %q, want %q", got.URL, e.URL)
	}
	if err := s.Put(ctx, e); err == nil {
		t.Fatal("duplicate evidence should be rejected")
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreClonesMutableFields(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	e := sampleEvidence("e1")
	e.Redactions = []string{"phone"}
	if err := s.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	got.Redactions[0] = "changed"
	again, err := s.Get(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Redactions[0] != "phone" {
		t.Fatalf("store was mutated through Get: %v", again.Redactions)
	}
}

func TestPrepareRedactsAndHashes(t *testing.T) {
	e := sampleEvidence("e1")
	e.ContentHash = ""
	got, redacted, findings, err := Prepare(e, "phone 13800138000")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	if redacted == "phone 13800138000" || got.ContentHash == "" {
		t.Fatalf("not redacted/hashed: %q %q", redacted, got.ContentHash)
	}
	if len(got.Redactions) == 0 || got.Redactions[0] != "phone" {
		t.Fatalf("redactions = %v", got.Redactions)
	}
}

func TestMemoryStoreListByPlatform(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if err := s.Put(ctx, sampleEvidence("e1")); err != nil {
		t.Fatal(err)
	}
	e2 := sampleEvidence("e2")
	e2.Platform = domain.PlatformVulbox
	if err := s.Put(ctx, e2); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListByPlatform(ctx, domain.PlatformButian)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "e1" {
		t.Fatalf("unexpected list: %+v", got)
	}
}
