package trace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Zn070515/BountyGrid/internal/redact"
)

var (
	// ErrWriterMissing means the sink has no underlying Writer.
	ErrWriterMissing = errors.New("trace: safe sink writer is nil")
	// ErrRawStoreMissing means the sink has no underlying RawStore.
	ErrRawStoreMissing = errors.New("trace: safe sink raw store is nil")
	// ErrDataClassRequired means RequireDataClass is set and the event has no
	// data class.
	ErrDataClassRequired = errors.New("trace: safe sink requires a data class")
	// ErrDanglingParent means ParentID has not been appended through this sink
	// yet. It catches an event whose cause was never written rather than
	// silently producing an orphan.
	ErrDanglingParent = errors.New("trace: safe sink rejects a dangling parent")
	// ErrSensitiveData means a payload or raw value matched internal/redact.
	// The error names only finding kinds, never the matched value.
	ErrSensitiveData = errors.New("trace: safe sink rejects sensitive data")
)

// SafeSink is the single enforced entry point for writing a trace. It applies
// schema validation, optional data-class enforcement, causal parent binding,
// and secret scanning before delegating to Writer/RawStore. Holding every write
// to one sink also makes parent binding deterministic under concurrency.
//
// A SafeSink is safe for concurrent use. Relative event order still depends on
// the caller: a parent must be appended before its children unless
// AllowDanglingParents is set.
type SafeSink struct {
	Writer *Writer
	Raw    *RawStore

	// RequireDataClass rejects events without PUBLIC/INTERNAL/SENSITIVE.
	RequireDataClass bool
	// AllowDanglingParents permits a non-empty ParentID that has not been seen
	// by this sink (for example when merging external traces).
	AllowDanglingParents bool

	mu   sync.Mutex
	seen map[string]struct{}
}

// NewSafeSink wraps a Writer and RawStore. Either may be nil if the sink is
// only used for the other operation; the corresponding method then fails
// closed.
func NewSafeSink(w *Writer, r *RawStore) *SafeSink {
	return &SafeSink{Writer: w, Raw: r, seen: make(map[string]struct{})}
}

// Append validates and redacts-scans e, checks parent binding, then appends it
// through the underlying Writer. The event ID is recorded only after a
// successful append, so a failed write does not make the event a valid parent.
func (s *SafeSink) Append(ctx context.Context, e Event) error {
	if s == nil || s.Writer == nil {
		return ErrWriterMissing
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if s.RequireDataClass && e.DataClass == "" {
		return fmt.Errorf("%w: event %q", ErrDataClassRequired, e.ID)
	}
	if findings := redact.Scan(string(e.Payload)); len(findings) > 0 {
		return fmt.Errorf("%w: event %q payload contains %s", ErrSensitiveData, e.ID, findingKinds(findings))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if e.ParentID != "" && !s.AllowDanglingParents {
		if _, ok := s.seen[e.ParentID]; !ok {
			return fmt.Errorf("%w: event %q parent %q", ErrDanglingParent, e.ID, e.ParentID)
		}
	}
	if err := s.Writer.Append(ctx, e); err != nil {
		return err
	}
	s.seen[e.ID] = struct{}{}
	return nil
}

// PutRaw scans data for secrets and stores it in the RawStore. Both the scan
// and storage happen before any reference is returned, so callers never get a
// ref to unredacted content.
func (s *SafeSink) PutRaw(ctx context.Context, data []byte) (string, error) {
	if s == nil || s.Raw == nil {
		return "", ErrRawStoreMissing
	}
	if findings := redact.Scan(string(data)); len(findings) > 0 {
		return "", fmt.Errorf("%w: raw payload contains %s", ErrSensitiveData, findingKinds(findings))
	}
	return s.Raw.Put(ctx, data)
}

// findingKinds returns a stable, comma-separated list of finding kinds. Only the
// kind is reported; previews are intentionally omitted so the error cannot leak
// any part of the matched value.
func findingKinds(findings []redact.Finding) string {
	set := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		set[f.Kind] = struct{}{}
	}
	kinds := make([]string, 0, len(set))
	for k := range set {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ",")
}
