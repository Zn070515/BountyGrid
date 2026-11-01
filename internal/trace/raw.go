package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RawStore stores raw (already redacted) payloads under root/raw/<sha256>.
// Content addressing makes repeated writes idempotent: identical bytes always
// map to the same Ref and the same file.
type RawStore struct {
	dir string
}

// OpenRawStore ensures root/raw exists and returns a store rooted there.
func OpenRawStore(root string) (*RawStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("trace: raw store root is empty")
	}
	dir := filepath.Join(root, "raw")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trace: create raw store %s: %w", dir, err)
	}
	return &RawStore{dir: dir}, nil
}

// ContentRef returns the content-addressed reference for data without writing
// it. The format is "sha256:<lowercase hex>".
func ContentRef(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Put writes data atomically and returns its Ref. The write goes to a 0600 temp
// file in the same directory, is synced, then renamed into place. Identical
// content is deduplicated: an existing file is left untouched.
func (s *RawStore) Put(ctx context.Context, data []byte) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	ref := ContentRef(data)
	name := strings.TrimPrefix(ref, "sha256:")
	dst := filepath.Join(s.dir, name)

	if _, err := os.Stat(dst); err == nil {
		return ref, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("trace: stat raw %s: %w", dst, err)
	}

	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("trace: create temp raw file: %w", err)
	}
	tmpName := tmp.Name()
	// After a successful rename tmpName no longer exists; Remove is a harmless
	// no-op. On any error path it cleans the partial file.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("trace: write temp raw file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("trace: sync temp raw file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("trace: close temp raw file: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		// A concurrent Put may have won the race. If the final content is there,
		// the operation succeeded by deduplication.
		if _, statErr := os.Stat(dst); statErr == nil {
			return ref, nil
		}
		return "", fmt.Errorf("trace: commit raw file %s: %w", dst, err)
	}
	return ref, nil
}

// Get reads a raw payload by Ref and verifies that its bytes still match the
// content address. A missing or corrupted payload is an error (fail closed).
func (s *RawStore) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	sum, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, hex.EncodeToString(sum[:]))
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("trace: raw payload %s: %w", ref, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("trace: read raw %s: %w", path, err)
	}
	got := sha256.Sum256(data)
	if got != sum {
		return nil, fmt.Errorf("trace: raw payload %s: content hash mismatch", ref)
	}
	return data, nil
}

// parseRef accepts only "sha256:<64 lowercase hex chars>". Refusing other forms
// also prevents path traversal through the reference.
func parseRef(ref string) ([32]byte, error) {
	var out [32]byte
	hexPart, ok := strings.CutPrefix(ref, "sha256:")
	if !ok {
		return out, fmt.Errorf("trace: invalid raw ref %q", ref)
	}
	raw, err := hex.DecodeString(hexPart)
	if err != nil || len(raw) != sha256.Size {
		return out, fmt.Errorf("trace: invalid raw ref %q", ref)
	}
	copy(out[:], raw)
	return out, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
