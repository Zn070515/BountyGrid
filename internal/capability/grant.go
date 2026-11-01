package capability

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
)

// ErrGrantDenied marks a request outside the approved execution grant. Egress
// and the proxy both return it (wrapped) so callers can fail closed without
// matching on strings.
var ErrGrantDenied = errors.New("capability: request is outside the execution grant")

// DefaultGrantLease bounds how long one ExecutionGrant may authorize requests.
// A grant is derived from a decision, re-reads no authoritative state, and is
// therefore short-lived by construction; a long-running execution must obtain
// a new decision. The lease is always capped by the decision expiry.
const DefaultGrantLease = 60 * time.Second

// ExecutionGrant is the per-execution network authority derived from the
// approved decision. A tool cannot widen it: the approved method, target,
// rate/concurrency ceiling and daily limit are enforced on every request, not
// once per tool call.
type ExecutionGrant struct {
	DecisionID domain.DecisionID
	// AccountID is the account the decision was made for. Together with
	// ProgramID it identifies the shared rate/concurrency/daily limiter, so a
	// second execution for the same account and program cannot reset the ceiling.
	AccountID domain.AccountID
	ProgramID domain.ProgramID
	AssetID   *domain.AssetID
	// AssetTarget is the canonical asset value resolved by the Action Gateway.
	AssetTarget string
	// ExpiresAt is the decision's expiry. A zero value means the grant carries no
	// expiry; GrantPolicy rejects such a grant rather than treating it as
	// unlimited.
	ExpiresAt time.Time
	// LeaseUntil is the grant's own short lease. A validated grant must be used
	// before this instant; a long-running execution has to re-decide. A zero
	// value is rejected by ProgramValidator (fail closed) and by GrantPolicy
	// when a Validator is configured.
	LeaseUntil time.Time
	// AllowedMethods is empty only for a grant with no bound method, in which
	// case only GET/HEAD are permitted.
	AllowedMethods   map[string]struct{}
	ForbiddenMethods map[string]struct{}
	// AllowedPathPrefixes restricts a URL asset to the approved path tree. Empty
	// means the asset did not specify a path.
	AllowedPathPrefixes []string
	MaxRPS              *float64
	MaxConcurrency      *int
	// DailyLimit is the maximum requests in one business day; 0 means the grant
	// has no explicit daily ceiling (it still counts requests for audit).
	DailyLimit int64
	// ScopeRevision and PolicyRevision bind the grant to the exact scope and
	// automation/constraints snapshots the decision was evaluated against. A
	// later edit changes the revision and ProgramValidator rejects the grant.
	ScopeRevision  string
	PolicyRevision string
	// AuthorizationEpoch is the revocation-registry generation observed when the
	// grant was built. A later Bump makes every grant with the old epoch invalid.
	AuthorizationEpoch uint64
}

// AllowsTarget checks a request URL against the grant's canonical asset and
// path prefixes. Bare-host assets match the host only; URL assets also pin
// scheme, port and path. Path comparison is segment-aware and canonicalized so
// "/api" can never match "/apix", "/api-admin" or encoded traversal.
func (g *ExecutionGrant) AllowsTarget(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: request has no url", ErrGrantDenied)
	}
	target := strings.TrimSpace(g.AssetTarget)
	host := strings.ToLower(u.Hostname())
	// Asset values may be bare host patterns (api.example.com, *.example.com)
	// or full URLs including a path. A bare pattern is matched against the host;
	// a URL target additionally pins scheme and path prefix.
	if target != "" && !strings.Contains(target, "/") && !strings.Contains(target, ":") {
		if !rules.HostMatches(target, host) {
			return fmt.Errorf("%w: host %s is not the approved asset %s", ErrGrantDenied, host, target)
		}
	} else if target != "" {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Hostname() == "" {
			// A non-URL asset value (a description, CIDR, etc.) cannot be treated
			// as a network target. Do not widen the grant silently.
			return fmt.Errorf("%w: asset target %q is not a network target", ErrGrantDenied, target)
		}
		if parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, u.Scheme) {
			return fmt.Errorf("%w: scheme %s is not approved", ErrGrantDenied, u.Scheme)
		}
		if !rules.HostMatches(parsed.Hostname(), host) {
			return fmt.Errorf("%w: host %s is not the approved asset %s", ErrGrantDenied, host, parsed.Hostname())
		}
		if parsed.Port() != "" && parsed.Port() != u.Port() {
			return fmt.Errorf("%w: port %s is not approved", ErrGrantDenied, u.Port())
		}
		if parsed.Path != "" && parsed.Path != "/" {
			path := u.EscapedPath()
			if path == "" {
				path = "/"
			}
			// Segment-aware matching: /api must not match /apix or /api-admin.
			// CanonicalPath also decodes escapes and resolves dot segments, so
			// /api/%2e%2e/admin cannot escape an /api prefix.
			if !rules.PathWithin(parsed.EscapedPath(), path) {
				return fmt.Errorf("%w: path %s is outside %s", ErrGrantDenied, path, parsed.Path)
			}
		}
	}
	// AllowedPathPrefixes apply to bare-host assets too; a host-only asset must
	// not silently drop the grant's path restriction.
	for _, prefix := range g.AllowedPathPrefixes {
		if prefix == "" {
			continue
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if !rules.PathWithin(prefix, path) {
			return fmt.Errorf("%w: path %s is outside %s", ErrGrantDenied, path, prefix)
		}
	}
	return nil
}
