package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy/rules"
	"github.com/Zn070515/BountyGrid/internal/redact"
)

// PermissionLinePrefix marks an explicit, machine-readable permission in a
// policy evidence document. A platform-specific Reader/compiler must emit these
// lines from the captured page; authz never infers a permission from prose.
const PermissionLinePrefix = "PERMISSION "

// EvidenceContentReader returns the redacted content that was hashed into
// Evidence.ContentHash. It is the trusted source the permission compiler reads;
// a nil reader means no semantic authority (fail closed).
type EvidenceContentReader interface {
	ReadContent(ctx context.Context, evidence domain.Evidence) (string, error)
}

// ExtractedPermission is one parsed PERMISSION line. It is the output of the
// trusted compiler and the only source from which a PermissionFact may cite
// selectors.
type ExtractedPermission struct {
	Quote        string
	ProgramID    domain.ProgramID
	Action       domain.Action
	Hosts        []string
	Methods      []string
	PathPrefixes []string
	Restrictions []string
}

// PermissionFact is the only input accepted by BuildPlatformPolicy. It is an
// explicit, evidence-bound statement that the platform permits one action on a
// bounded set of hosts/methods/paths for exactly one program.
//
// A program listing, account capture, payout record or screenshot is not a
// permission: VerifyPlatformEvidence accepts only Evidence with SourceType
// POLICY or ANNOUNCEMENT. Hosts and methods must be stated explicitly (an
// empty list is not "all"), and a non-empty Restrictions list means the
// platform attached a condition, so this plain-allow path rejects it instead
// of silently granting CanRelax.
type PermissionFact struct {
	// EvidenceID names the platform page/announcement that states the
	// permission. EvidenceContentHash binds this fact to that exact content:
	// if the stored Evidence changes, authority is removed.
	EvidenceID          domain.EvidenceID
	EvidenceContentHash string
	// EvidenceQuote is the exact PERMISSION line in the evidence content that
	// grants this fact. Verification requires the quote to match a parsed line
	// and every selector to equal the parsed values; a caller cannot invent a
	// permission that the content does not state.
	EvidenceQuote string
	Platform      domain.Platform
	ProgramID     domain.ProgramID
	Action        domain.Action
	// Hosts and Methods must be explicitly non-empty. PathPrefixes may be
	// empty, meaning "any path on the listed hosts".
	Hosts        []string
	Methods      []string
	PathPrefixes []string
	// Restrictions must be empty for a plain allow. Any entry turns the
	// permission into a conditional/human-review obligation.
	Restrictions   []string
	ObservedAt     time.Time
	ValidUntil     time.Time
	PolicyRevision string
	Justification  string
}

// ID is the stable audit identifier of this permission. It is derived from the
// Evidence ID plus a digest of the permission's own fields, so several
// permissions extracted from the same policy page remain distinguishable in
// audit output.
func (p PermissionFact) ID() string {
	raw, err := json.Marshal([]any{
		string(p.EvidenceID), p.Platform, string(p.ProgramID), string(p.Action),
		p.Hosts, p.Methods, p.PathPrefixes, p.Restrictions,
	})
	if err != nil {
		return "platform-permission/" + string(p.EvidenceID)
	}
	sum := sha256.Sum256(raw)
	return "platform-permission/" + string(p.EvidenceID) + "/" + hex.EncodeToString(sum[:8])
}

// VerifyPlatformEvidence proves that a PermissionFact actually states a
// permission for this program and that its evidence is still usable.
//
// It checks, fail-closed:
//   - an evidence store is configured and the Evidence exists;
//   - the permission and the Evidence both belong to the program's platform;
//   - Evidence.ContentHash matches the content hash the permission was
//     extracted from;
//   - Evidence.SourceType is POLICY or ANNOUNCEMENT (PROGRAM, ACCOUNT,
//     PAYOUT and SCREENSHOT are not authorization);
//   - the permission binds this program and names a valid action;
//   - Hosts and Methods are explicitly non-empty and valid;
//   - PathPrefixes are valid canonical paths;
//   - Restrictions is empty (a restricted permission is conditional, not a
//     plain allow);
//   - PolicyRevision matches Program.PolicyRevision();
//   - ValidUntil is in the future and ObservedAt is not materially future;
//   - Program.PolicyFreshAt(now) holds.
//
// A missing evidence record, a platform/hash/source-type mismatch, a stale
// revision and an expired permission are all rejections. Store/database
// failures are returned as errors so a transient outage is never mistaken for
// authority.
func VerifyPlatformEvidence(ctx context.Context, permission PermissionFact, program domain.Program, store EvidenceStore, contents EvidenceContentReader, now time.Time) error {
	_, err := verifyPermission(ctx, permission, program, newVerificationCache(store, contents), now)
	return err
}

type cachedEvidenceLoad struct {
	ev  domain.Evidence
	err error
}

type cachedContentLoad struct {
	text string
	err  error
}

// verificationCache keeps one Evidence/content load per evidence ID for a
// single Build or Evaluate call. Several permissions routinely cite the same
// policy page; without the cache the pre-execution Authorize path would read
// and hash that page once per permission. The cache is request-scoped, so a
// later Evaluate still observes deletes/replacements (the no-outliving rule).
type verificationCache struct {
	store    EvidenceStore
	contents EvidenceContentReader
	evidence map[domain.EvidenceID]cachedEvidenceLoad
	content  map[domain.EvidenceID]cachedContentLoad
}

func newVerificationCache(store EvidenceStore, contents EvidenceContentReader) *verificationCache {
	return &verificationCache{
		store:    store,
		contents: contents,
		evidence: make(map[domain.EvidenceID]cachedEvidenceLoad),
		content:  make(map[domain.EvidenceID]cachedContentLoad),
	}
}

func (c *verificationCache) getEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	if loaded, ok := c.evidence[id]; ok {
		return loaded.ev, loaded.err
	}
	ev, err := c.store.GetEvidence(ctx, id)
	c.evidence[id] = cachedEvidenceLoad{ev: ev, err: err}
	return ev, err
}

func (c *verificationCache) getContent(ctx context.Context, ev domain.Evidence) (string, error) {
	if loaded, ok := c.content[ev.ID]; ok {
		return loaded.text, loaded.err
	}
	text, err := c.contents.ReadContent(ctx, ev)
	c.content[ev.ID] = cachedContentLoad{text: text, err: err}
	return text, err
}

// verifyPermission validates a permission and returns its normalized copy.
// BuildPlatformPolicy stores the normalized copy so matching does not have to
// re-normalize host/method/path selectors.
func verifyPermission(ctx context.Context, permission PermissionFact, program domain.Program, cache *verificationCache, now time.Time) (PermissionFact, error) {
	var zero PermissionFact
	if cache == nil || cache.store == nil {
		return zero, reject(ReasonEvidenceStoreMissing, "no evidence store configured")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	normalized, err := normalizePermissionFact(permission)
	if err != nil {
		return zero, err
	}
	if normalized.ProgramID != program.ID {
		return zero, reject(ReasonPermissionProgramMismatch, "permission %s binds program %s, not program %s",
			normalized.ID(), normalized.ProgramID, program.ID)
	}
	if normalized.Platform != program.Platform {
		return zero, reject(ReasonEvidencePlatformMismatch, "permission %s platform %q does not match program platform %q",
			normalized.ID(), normalized.Platform, program.Platform)
	}
	if err := CheckRevisionBinding(program, "", normalized.PolicyRevision); err != nil {
		return zero, err
	}
	if !normalized.ValidUntil.After(now) {
		return zero, reject(ReasonPermissionExpired, "permission %s valid_until %s is not after %s",
			normalized.ID(), normalized.ValidUntil.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if normalized.ObservedAt.After(now.Add(ClockSkew)) {
		return zero, reject(ReasonPermissionFromFuture, "permission %s observed_at %s is after %s",
			normalized.ID(), normalized.ObservedAt.UTC().Format(time.RFC3339), now.Add(ClockSkew).UTC().Format(time.RFC3339))
	}
	if !program.PolicyFreshAt(now) {
		return zero, &FreshnessError{Reason: ReasonStaleAutomationPolicy}
	}

	ev, err := cache.getEvidence(ctx, normalized.EvidenceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return zero, reject(ReasonEvidenceMissing, "evidence %s not found", normalized.EvidenceID)
		}
		return zero, fmt.Errorf("authz: load evidence %s: %w", normalized.EvidenceID, err)
	}
	if ev.Platform != program.Platform {
		return zero, reject(ReasonEvidencePlatformMismatch, "evidence %s platform %q does not match program platform %q",
			normalized.EvidenceID, ev.Platform, program.Platform)
	}
	if normalized.EvidenceContentHash == "" || ev.ContentHash != normalized.EvidenceContentHash {
		return zero, reject(ReasonEvidenceContentHashMismatch, "evidence %s content hash %q does not match permission content hash %q",
			normalized.EvidenceID, ev.ContentHash, normalized.EvidenceContentHash)
	}
	if ev.SourceType != domain.SourcePolicy && ev.SourceType != domain.SourceAnnouncement {
		return zero, reject(ReasonEvidenceSourceTypeNotAuthorizing,
			"evidence %s source type %q is not POLICY or ANNOUNCEMENT", normalized.EvidenceID, ev.SourceType)
	}
	if cache.contents == nil {
		return zero, reject(ReasonEvidenceContentMissing,
			"no evidence content reader configured for %s", normalized.EvidenceID)
	}
	content, err := cache.getContent(ctx, ev)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return zero, reject(ReasonEvidenceContentMissing, "content for evidence %s not found", normalized.EvidenceID)
		}
		return zero, fmt.Errorf("authz: read evidence content %s: %w", normalized.EvidenceID, err)
	}
	if redact.Hash([]byte(content)) != ev.ContentHash {
		return zero, reject(ReasonEvidenceContentHashMismatch,
			"evidence %s content does not hash to %q", normalized.EvidenceID, ev.ContentHash)
	}
	extracted, err := ExtractPermissions(content)
	if err != nil {
		return zero, reject(ReasonPermissionLineInvalid,
			"evidence %s permission lines: %v", normalized.EvidenceID, err)
	}
	matched, ok := findQuotedPermission(extracted, normalized.EvidenceQuote)
	if !ok {
		return zero, reject(ReasonPermissionNotInEvidence,
			"permission %s quote is not a PERMISSION line in evidence %s", normalized.ID(), normalized.EvidenceID)
	}
	if matched.ProgramID != normalized.ProgramID || matched.Action != normalized.Action ||
		!sameStringSet(matched.Hosts, normalized.Hosts) ||
		!sameStringSet(matched.Methods, normalized.Methods) ||
		!sameStringSet(matched.PathPrefixes, normalized.PathPrefixes) {
		return zero, reject(ReasonPermissionNotInEvidence,
			"permission %s selectors do not match its quoted evidence line", normalized.ID())
	}
	if len(matched.Restrictions) > 0 {
		return zero, reject(ReasonPermissionRestrictionsPresent,
			"permission %s quotes a restricted line; a restricted permission requires human review", normalized.ID())
	}
	return normalized, nil
}

func findQuotedPermission(extracted []ExtractedPermission, quote string) (ExtractedPermission, bool) {
	for _, p := range extracted {
		if p.Quote == quote {
			return p, true
		}
	}
	return ExtractedPermission{}, false
}

// sameStringSet is exact multiset equality. It deliberately does not treat a
// repeated value as equivalent to two distinct values: this function is the
// proof that a PermissionFact's selectors equal the quoted PERMISSION line, and
// a duplicate must not verify against a line granting a different selector.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// normalizePermissionFact enforces the explicit permission shape and
// canonicalizes selectors. Every structural failure is a rejection: there is
// no default host, method, path or action.
func normalizePermissionFact(permission PermissionFact) (PermissionFact, error) {
	var zero PermissionFact
	permission.EvidenceID = domain.EvidenceID(strings.TrimSpace(string(permission.EvidenceID)))
	if permission.EvidenceID == "" {
		return zero, reject(ReasonEvidenceMissing, "permission has no evidence id")
	}
	permission.EvidenceQuote = strings.TrimSpace(permission.EvidenceQuote)
	if permission.EvidenceQuote == "" {
		return zero, reject(ReasonPermissionQuoteMissing, "permission %s has no evidence quote", permission.ID())
	}
	permission.ProgramID = domain.ProgramID(strings.TrimSpace(string(permission.ProgramID)))
	if permission.ProgramID == "" {
		return zero, reject(ReasonPermissionProgramMismatch, "permission %s has no program id", permission.ID())
	}
	if !permission.Platform.Valid() {
		return zero, reject(ReasonEvidencePlatformMismatch, "permission %s has invalid platform %q", permission.ID(), permission.Platform)
	}
	if !permission.Action.Valid() {
		return zero, reject(ReasonPermissionActionInvalid, "permission %s has invalid action %q", permission.ID(), permission.Action)
	}
	if len(permission.Restrictions) > 0 {
		return zero, reject(ReasonPermissionRestrictionsPresent,
			"permission %s carries %d restriction(s); a restricted permission requires human review and cannot relax automatically",
			permission.ID(), len(permission.Restrictions))
	}
	if len(permission.Hosts) == 0 {
		return zero, reject(ReasonPermissionHostsMissing, "permission %s does not name any hosts", permission.ID())
	}
	hosts := make([]string, 0, len(permission.Hosts))
	for _, raw := range permission.Hosts {
		host, err := rules.NormalizeHostPattern(raw)
		if err != nil {
			return zero, reject(ReasonPermissionHostInvalid, "permission %s: %v", permission.ID(), err)
		}
		hosts = append(hosts, host)
	}
	permission.Hosts = hosts
	if len(permission.Methods) == 0 {
		return zero, reject(ReasonPermissionMethodsMissing, "permission %s does not name any methods", permission.ID())
	}
	methods := make([]string, 0, len(permission.Methods))
	for _, raw := range permission.Methods {
		method, err := rules.NormalizeMethod(raw)
		if err != nil {
			return zero, reject(ReasonPermissionMethodsMissing, "permission %s: %v", permission.ID(), err)
		}
		methods = append(methods, method)
	}
	permission.Methods = methods
	prefixes := make([]string, 0, len(permission.PathPrefixes))
	for _, raw := range permission.PathPrefixes {
		// An empty entry would canonicalize to "/" and silently allow every
		// path. All-paths must be expressed by leaving the list empty.
		if strings.TrimSpace(raw) == "" {
			return zero, reject(ReasonPermissionPathInvalid, "permission %s has an empty path prefix", permission.ID())
		}
		prefix, err := rules.CanonicalPath(raw)
		if err != nil {
			return zero, reject(ReasonPermissionPathInvalid, "permission %s: %v", permission.ID(), err)
		}
		prefixes = append(prefixes, prefix)
	}
	permission.PathPrefixes = prefixes
	if permission.ObservedAt.IsZero() {
		return zero, reject(ReasonPermissionObservationInvalid, "permission %s has no observed_at", permission.ID())
	}
	if permission.ValidUntil.IsZero() || !permission.ValidUntil.After(permission.ObservedAt) {
		return zero, reject(ReasonPermissionObservationInvalid,
			"permission %s valid_until must be after observed_at", permission.ID())
	}
	permission.PolicyRevision = trimRevision(permission.PolicyRevision)
	if permission.PolicyRevision == "" {
		return zero, reject(ReasonPermissionRevisionMissing, "permission %s has no policy_revision", permission.ID())
	}
	permission.Justification = strings.TrimSpace(permission.Justification)
	if permission.Justification == "" {
		return zero, reject(ReasonPermissionJustificationMissing, "permission %s has no justification", permission.ID())
	}
	return permission, nil
}

// BuildPlatformPolicy constructs platform authority for one program from
// explicit PermissionFacts. There is deliberately no overload that accepts a
// rule file, an arbitrary platform-rule spec, or raw selector data: every
// candidate must be a PermissionFact and must pass VerifyPlatformEvidence. The resulting
// policy is immutable and holds only verified, normalized permissions.
func BuildPlatformPolicy(ctx context.Context, program domain.Program, permissions []PermissionFact, store EvidenceStore, contents EvidenceContentReader, now time.Time) (*PlatformPolicy, error) {
	verified := make([]PermissionFact, 0, len(permissions))
	cache := newVerificationCache(store, contents)
	for _, permission := range permissions {
		normalized, err := verifyPermission(ctx, permission, program, cache, now)
		if err != nil {
			return nil, err
		}
		verified = append(verified, normalized)
	}
	return newPlatformPolicy(program, verified, store, contents), nil
}

// PlatformPolicy is verified platform authority for exactly one program. It can
// only be produced by BuildPlatformPolicy (or another authz constructor), never
// from a rule file. Evaluate re-verifies every permission fact and its Evidence
// at decision time, so authority observed earlier cannot outlive the evidence,
// content hash, source type, program or policy snapshot it was derived from.
type PlatformPolicy struct {
	programID   domain.ProgramID
	platform    domain.Platform
	permissions []PermissionFact
	store       EvidenceStore
	contents    EvidenceContentReader
	fingerprint string
}

func newPlatformPolicy(program domain.Program, verified []PermissionFact, store EvidenceStore, contents EvidenceContentReader) *PlatformPolicy {
	p := &PlatformPolicy{
		programID:   program.ID,
		platform:    program.Platform,
		permissions: append([]PermissionFact(nil), verified...),
		store:       store,
		contents:    contents,
	}
	p.fingerprint = p.computeFingerprint()
	return p
}

// ProgramID is the program this policy was verified for.
func (p *PlatformPolicy) ProgramID() domain.ProgramID {
	if p == nil {
		return ""
	}
	return p.programID
}

// Len reports how many verified permission facts the policy holds.
func (p *PlatformPolicy) Len() int {
	if p == nil {
		return 0
	}
	return len(p.permissions)
}

// Fingerprint covers the program binding and every permission's provenance
// fields (evidence id/hash, observation window, policy revision, selectors,
// restrictions, justification). Authority changes therefore invalidate
// decisions bound to the previous fingerprint.
func (p *PlatformPolicy) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.fingerprint
}

func (p *PlatformPolicy) computeFingerprint() string {
	// The fingerprint identifies the authority, not the order in which a caller
	// happened to list its evidence. Serialize each permission and sort, so
	// compiling the same authority from `-platform-evidence evA,evB` and from
	// `-platform-evidence evB,evA` produces the same fingerprint (otherwise
	// `decide -authorize` rejects an otherwise unchanged decision).
	encoded := make([]string, 0, len(p.permissions))
	for _, permission := range p.permissions {
		raw, err := json.Marshal(permission)
		if err != nil {
			return ""
		}
		encoded = append(encoded, string(raw))
	}
	slices.Sort(encoded)
	raw, err := json.Marshal(struct {
		Program     domain.ProgramID
		Platform    domain.Platform
		Permissions []string
	}{Program: p.programID, Platform: p.platform, Permissions: encoded})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Evaluate matches verified permission facts against req. Only permissions
// that re-verify at decision time (evidence still exists, same platform, same
// content hash, authorizing source type, matching revision and freshness) can
// set CanRelax. A policy for a different program/platform, or a program whose
// policy snapshot is stale, yields no authority rather than an error; the
// caller's own domain gates still apply.
func (p *PlatformPolicy) Evaluate(ctx context.Context, program domain.Program, req rules.Request) (rules.Result, error) {
	if p == nil || p.store == nil {
		return rules.Result{}, nil
	}
	if program.ID != p.programID || program.Platform != p.platform {
		return rules.Result{}, nil
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
		req.Now = now
	}
	if !program.PolicyFreshAt(now) {
		return rules.Result{}, nil
	}
	normalized, err := rules.NormalizeRequest(req)
	if err != nil {
		return rules.Result{}, err
	}
	var out rules.Result
	cache := newVerificationCache(p.store, p.contents)
	for _, permission := range p.permissions {
		// Re-verify at decision time, not only at build time: evidence may have
		// been deleted or replaced since the policy was compiled. A failed
		// verification simply yields no authority for that permission (the base
		// decision/freshness gates still apply).
		verified, err := verifyPermission(ctx, permission, program, cache, now)
		if err != nil {
			continue
		}
		ok, matchedOn := verified.matches(normalized)
		if !ok {
			continue
		}
		out.Matched = append(out.Matched, rules.MatchedRule{
			ID: verified.ID(), Source: rules.SourcePlatformEvidence, Decision: rules.DecisionAllow,
			Justification: verified.Justification, MatchedOn: matchedOn,
		})
		out.Decision = rules.DecisionAllow
		out.CanRelax = true
	}
	return out, nil
}

// ExtractPermissions parses the explicit PERMISSION lines from a policy
// evidence document. A platform-specific Reader/compiler must emit these lines
// from the captured page; prose is never interpreted as permission. Any
// malformed line invalidates the document (fail closed).
//
// Grammar (one line per permission):
//
//	PERMISSION program=<id> action=<ACTION> hosts=<h1,h2> methods=<GET,POST> [paths=/a,/b] [restrictions=<r1,r2>]
func ExtractPermissions(content string) ([]ExtractedPermission, error) {
	var out []ExtractedPermission
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, PermissionLinePrefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("permission line has no fields")
		}
		values := make(map[string]string, len(fields))
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)
			if !ok || key == "" || value == "" {
				return nil, fmt.Errorf("malformed permission field %q", field)
			}
			switch key {
			case "program", "action", "hosts", "methods", "paths", "restrictions":
			default:
				return nil, fmt.Errorf("unknown permission field %q", key)
			}
			if _, duplicate := values[key]; duplicate {
				return nil, fmt.Errorf("duplicate permission field %q", key)
			}
			values[key] = value
		}
		program := domain.ProgramID(strings.TrimSpace(values["program"]))
		if program == "" {
			return nil, fmt.Errorf("missing program")
		}
		action := domain.Action(strings.ToUpper(strings.TrimSpace(values["action"])))
		if !action.Valid() {
			return nil, fmt.Errorf("invalid action %q", values["action"])
		}
		hosts, err := parsePermissionList(values["hosts"], rules.NormalizeHostPattern)
		if err != nil || len(hosts) == 0 {
			return nil, fmt.Errorf("hosts: %v", err)
		}
		methods, err := parsePermissionList(values["methods"], rules.NormalizeMethod)
		if err != nil || len(methods) == 0 {
			return nil, fmt.Errorf("methods: %v", err)
		}
		var paths []string
		if value, ok := values["paths"]; ok {
			paths, err = parsePermissionList(value, rules.CanonicalPath)
			if err != nil {
				return nil, fmt.Errorf("paths: %w", err)
			}
		}
		var restrictions []string
		if value, ok := values["restrictions"]; ok {
			restrictions, err = parsePermissionList(value, normalizeRestriction)
			if err != nil {
				return nil, fmt.Errorf("restrictions: %w", err)
			}
		}
		out = append(out, ExtractedPermission{
			Quote: line, ProgramID: program, Action: action,
			Hosts: hosts, Methods: methods, PathPrefixes: paths, Restrictions: restrictions,
		})
	}
	return out, nil
}

func parsePermissionList(raw string, normalize func(string) (string, error)) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty entry")
		}
		value, err := normalize(part)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func normalizeRestriction(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty restriction")
	}
	return raw, nil
}

// matches reports whether a verified permission permits the normalized
// request. Host and method are mandatory; path prefixes are optional and
// segment-aware.
func (p PermissionFact) matches(req rules.NormalizedRequest) (bool, []string) {
	if req.Action != p.Action || req.Program != p.ProgramID {
		return false, nil
	}
	if req.Host == "" || req.Method == "" {
		return false, nil
	}
	var matchedOn []string
	hostMatched := false
	for _, host := range p.Hosts {
		if rules.HostMatches(host, req.Host) {
			hostMatched = true
			matchedOn = append(matchedOn, "host="+host)
			break
		}
	}
	if !hostMatched {
		return false, nil
	}
	methodMatched := false
	for _, method := range p.Methods {
		if method == req.Method {
			methodMatched = true
			matchedOn = append(matchedOn, "method="+method)
			break
		}
	}
	if !methodMatched {
		return false, nil
	}
	if len(p.PathPrefixes) > 0 {
		pathMatched := false
		for _, prefix := range p.PathPrefixes {
			if req.Path != "" && rules.PathWithin(prefix, req.Path) {
				pathMatched = true
				matchedOn = append(matchedOn, "path_prefix="+prefix)
				break
			}
		}
		if !pathMatched {
			return false, nil
		}
	}
	return true, append([]string{
		"action=" + string(req.Action),
		"program=" + string(req.Program),
	}, matchedOn...)
}
