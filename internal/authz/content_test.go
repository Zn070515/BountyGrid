package authz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func writeEvidenceFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileContentReaderReturnsRedactedContent(t *testing.T) {
	dir := t.TempDir()
	content := "平台政策页面正文\n" + testPermissionLine + "\n"
	path := writeEvidenceFile(t, dir, "ev-1.json", content)
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1", RawRef: path})
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

func TestFileContentReaderAcceptsRelativeRawRef(t *testing.T) {
	dir := t.TempDir()
	writeEvidenceFile(t, dir, "ev-1.json", testPolicyContent)
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadContent(context.Background(), domain.Evidence{
		ID: "ev-1", RawRef: "ev-1.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != testPolicyContent {
		t.Fatalf("content = %q, want %q", got, testPolicyContent)
	}
}

func TestFileContentReaderConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	path := writeEvidenceFile(t, outside, "ev-1.json", testPolicyContent)
	reader, err := NewFileContentReader(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1", RawRef: path})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("out-of-root read = %v, want escape error", err)
	}
}

func TestFileContentReaderMissingFileIsNotFound(t *testing.T) {
	dir := t.TempDir()
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadContent(context.Background(), domain.Evidence{
		ID: "ev-1", RawRef: filepath.Join(dir, "missing.json"),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing file = %v, want domain.ErrNotFound", err)
	}
}

func TestFileContentReaderRejectsEmptyRawRef(t *testing.T) {
	reader, err := NewFileContentReader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1"}); err == nil {
		t.Fatal("empty raw ref accepted")
	}
}

func TestFileContentReaderRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1", RawRef: sub}); err == nil {
		t.Fatal("directory accepted as evidence content")
	}
}

func TestFileContentReaderEnforcesSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := writeEvidenceFile(t, dir, "large.json", strings.Repeat("x", 129))
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader.MaxBytes = 128
	if _, err := reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1", RawRef: path}); err == nil {
		t.Fatal("oversize content accepted")
	}
}

func TestFileContentReaderRejectsNilReceiverAndEmptyRoot(t *testing.T) {
	if _, err := NewFileContentReader("  "); err == nil {
		t.Fatal("empty root accepted")
	}
	var reader *FileContentReader
	if _, err := reader.ReadContent(context.Background(), domain.Evidence{ID: "ev-1", RawRef: "x"}); err == nil {
		t.Fatal("nil reader accepted")
	}
}

func TestFileContentReaderHonoursCancelledContext(t *testing.T) {
	dir := t.TempDir()
	path := writeEvidenceFile(t, dir, "ev-1.json", testPolicyContent)
	reader, err := NewFileContentReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ReadContent(ctx, domain.Evidence{ID: "ev-1", RawRef: path}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v, want context.Canceled", err)
	}
}
