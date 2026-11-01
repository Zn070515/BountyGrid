package authz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// DefaultMaxEvidenceBytes bounds how much policy evidence the compiler will
// read for one Evidence record. Policy/announcement pages are small; a huge
// file is more likely an operator error or a tampering attempt than authority.
const DefaultMaxEvidenceBytes int64 = 8 << 20

// FileContentReader implements EvidenceContentReader over the local redacted
// evidence files referenced by Evidence.RawRef. It is the default reader for
// CLI/worker wiring; the content is still re-hashed and re-parsed by
// VerifyPlatformEvidence before any PermissionFact becomes authority.
//
// The reader is deliberately fail-closed:
//   - a nil reader, empty root or empty RawRef is an error;
//   - a relative RawRef resolves under Root, never relative to the process
//     working directory;
//   - an absolute RawRef or a path containing dot segments that leaves Root is
//     rejected;
//   - the target must be a regular file no larger than MaxBytes;
//   - a missing file maps to domain.ErrNotFound so the verifier can report
//     PLATFORM_EVIDENCE_CONTENT_MISSING instead of an infrastructure error.
type FileContentReader struct {
	root     string
	MaxBytes int64
}

// NewFileContentReader returns a reader confined to root. The root is resolved
// to an absolute path once so later working-directory changes cannot move the
// boundary.
func NewFileContentReader(root string) (*FileContentReader, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("authz: evidence content root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("authz: resolve evidence content root: %w", err)
	}
	return &FileContentReader{root: abs, MaxBytes: DefaultMaxEvidenceBytes}, nil
}

// ReadContent returns the redacted evidence content. It does not verify
// ContentHash: the caller owns that proof so there is exactly one hashing
// authority in the permission chain.
func (r *FileContentReader) ReadContent(ctx context.Context, ev domain.Evidence) (string, error) {
	if r == nil {
		return "", errors.New("authz: nil evidence content reader")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw := strings.TrimSpace(ev.RawRef)
	if raw == "" {
		return "", fmt.Errorf("authz: evidence %s has no raw ref", ev.ID)
	}
	ref := raw
	if !filepath.IsAbs(ref) {
		ref = filepath.Join(r.root, ref)
	}
	rel, err := filepath.Rel(r.root, ref)
	if err != nil {
		return "", fmt.Errorf("authz: evidence %s raw ref %q: %w", ev.ID, raw, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("authz: evidence %s raw ref %q escapes content root %q", ev.ID, raw, r.root)
	}

	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", fmt.Errorf("authz: open evidence content root: %w", err)
	}
	defer root.Close()
	// os.Root also blocks symlinks that resolve outside the root, which the
	// lexical Rel check above cannot see.
	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("authz: open evidence %s: %w", ev.ID, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("authz: stat evidence %s: %w", ev.ID, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("authz: evidence %s content is not a regular file", ev.ID)
	}
	limit := r.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxEvidenceBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", fmt.Errorf("authz: read evidence %s: %w", ev.ID, err)
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("authz: evidence %s content exceeds %d bytes", ev.ID, limit)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

var _ EvidenceContentReader = (*FileContentReader)(nil)
