// Package rules is the declarative policy-rule layer. Rules are data, not code:
// each rule selects a request by action/program/host/method/path, carries a
// decision and justification, and may include match/not_match examples that are
// validated at load time. When several rules match, the strictest decision wins
// (forbidden > require_human > allow).
//
// The package deliberately does not replace Policy's domain invariants. It is a
// rule overlay applied after scope/asset/eligibility/runtime checks, so a rule
// can never authorize a request that failed an earlier gate.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

type Decision string

const (
	DecisionNone         Decision = ""
	DecisionAllow        Decision = "allow"
	DecisionRequireHuman Decision = "require_human"
	DecisionForbidden    Decision = "forbidden"
)

func (d Decision) Valid() bool {
	switch d {
	case DecisionAllow, DecisionRequireHuman, DecisionForbidden:
		return true
	default:
		return false
	}
}

func (d Decision) rank() int {
	switch d {
	case DecisionForbidden:
		return 3
	case DecisionRequireHuman:
		return 2
	case DecisionAllow:
		return 1
	default:
		return 0
	}
}

func normalizeDecision(raw string) (Decision, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "allow":
		return DecisionAllow, nil
	case "prompt", "prompt_user", "prompt-user", "require_human", "require-human":
		return DecisionRequireHuman, nil
	case "forbidden", "deny":
		return DecisionForbidden, nil
	default:
		return DecisionNone, fmt.Errorf("invalid decision %q", raw)
	}
}

// Example is a load-time test for a rule. The matcher treats it exactly like a
// Request; a `match` example must match its rule and a `not_match` example must
// not.
type Example struct {
	Action  domain.Action    `json:"action,omitempty"`
	Program domain.ProgramID `json:"program,omitempty"`
	Asset   string           `json:"asset,omitempty"`
	Method  string           `json:"method,omitempty"`
	Path    string           `json:"path,omitempty"`
}

// Source is the authority behind a rule. Operator guardrails may only tighten.
// Platform evidence is a separate, non-JSON authority: it is represented by
// internal/authz.PermissionFact and is verified against the Evidence store
// before it can relax anything. Parse/LoadFile reject source=platform_evidence
// outright, so a hand-written rule file can never claim platform authority.
type Source string

const (
	SourceOperator         Source = "operator"
	SourcePlatformEvidence Source = "platform_evidence"
)

func (s Source) Valid() bool {
	switch s {
	case SourceOperator, SourcePlatformEvidence:
		return true
	default:
		return false
	}
}

type Rule struct {
	ID string `json:"id"`
	// Empty selector lists mean "any"; every non-empty selector must match.
	Actions      []domain.Action    `json:"actions,omitempty"`
	Programs     []domain.ProgramID `json:"programs,omitempty"`
	Hosts        []string           `json:"hosts,omitempty"`
	Methods      []string           `json:"methods,omitempty"`
	PathPrefixes []string           `json:"path_prefixes,omitempty"`
	Decision     Decision           `json:"decision"`
	// Source defaults to operator. Parse/Validate reject platform_evidence in a
	// rule file; platform authority is represented by
	// internal/authz.PermissionFact and never by a parsed Rule.
	Source Source `json:"source,omitempty"`
	// Provenance fields are accepted only so a forged platform_evidence file can
	// be rejected with a clear error. Operator rules must leave them empty.
	EvidenceRefs   []domain.EvidenceID `json:"evidence_refs,omitempty"`
	ObservedAt     time.Time           `json:"observed_at,omitempty"`
	ValidUntil     time.Time           `json:"valid_until,omitempty"`
	PolicyRevision string              `json:"policy_revision,omitempty"`
	// Justification is required for forbid/require_human rules and is shown in
	// approval/rejection output.
	Justification string    `json:"justification,omitempty"`
	Match         []Example `json:"match,omitempty"`
	NotMatch      []Example `json:"not_match,omitempty"`
}

type RuleSet struct {
	Version string `json:"version,omitempty"`
	Rules   []Rule `json:"rules"`
}

type Request struct {
	Action  domain.Action    `json:"action,omitempty"`
	Program domain.ProgramID `json:"program,omitempty"`
	Asset   string           `json:"asset,omitempty"`
	Method  string           `json:"method,omitempty"`
	Path    string           `json:"path,omitempty"`
	// Now is used to expire platform-evidence rules. Zero means time.Now().
	Now time.Time `json:"-"`
}

type MatchedRule struct {
	ID            string   `json:"id"`
	Source        Source   `json:"source"`
	Decision      Decision `json:"decision"`
	Justification string   `json:"justification,omitempty"`
	MatchedOn     []string `json:"matched_on,omitempty"`
}

type Result struct {
	Decision Decision      `json:"decision,omitempty"`
	Matched  []MatchedRule `json:"matched_rules,omitempty"`
	// CanRelax is set only by authz.PlatformPolicy after a verified, fresh,
	// revision-matching platform rule matches. Operator RuleSet.Evaluate never
	// sets it, so an operator allow cannot raise a REQUIRE_HUMAN/DENY decision.
	CanRelax bool `json:"can_relax,omitempty"`
}

func (r Result) IDs() []string {
	out := make([]string, 0, len(r.Matched))
	for _, m := range r.Matched {
		out = append(out, m.ID)
	}
	return out
}

// Reasons renders stable policy-decision reasons for audit. The justification
// stays in MatchedRule/CLI output rather than being copied into every reason.
func (r Result) Reasons() []string {
	out := make([]string, 0, len(r.Matched))
	for _, m := range r.Matched {
		out = append(out, fmt.Sprintf("RULE_%s:%s", strings.ToUpper(string(m.Decision)), m.ID))
	}
	return out
}

func Parse(data []byte) (*RuleSet, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rs RuleSet
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("rules: decode: %w", err)
	}
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	return &rs, nil
}

func LoadFile(name string) (*RuleSet, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("rules: read %s: %w", name, err)
	}
	rs, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return rs, nil
}

func (s *RuleSet) Validate() error {
	if s == nil {
		return fmt.Errorf("rules: rule set is nil")
	}
	if len(s.Rules) == 0 {
		return fmt.Errorf("rules: no rules")
	}
	seen := make(map[string]struct{}, len(s.Rules))
	for i := range s.Rules {
		r := &s.Rules[i]
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			return fmt.Errorf("rules: rule %d: id is empty", i)
		}
		if _, ok := seen[r.ID]; ok {
			return fmt.Errorf("rules: duplicate rule id %q", r.ID)
		}
		seen[r.ID] = struct{}{}

		decision, err := normalizeDecision(string(r.Decision))
		if err != nil {
			return fmt.Errorf("rules: rule %s: %w", r.ID, err)
		}
		r.Decision = decision
		source := r.Source
		if source == "" {
			source = SourceOperator
		}
		if !source.Valid() {
			return fmt.Errorf("rules: rule %s: invalid source %q", r.ID, r.Source)
		}
		if source == SourcePlatformEvidence {
			return fmt.Errorf("rules: rule %s: source=platform_evidence cannot be loaded from a rule file; platform authority is compiled from Evidence via internal/authz", r.ID)
		}
		r.Source = source
		if len(r.EvidenceRefs) > 0 || !r.ObservedAt.IsZero() || !r.ValidUntil.IsZero() || r.PolicyRevision != "" {
			return fmt.Errorf("rules: rule %s: operator guardrails must not carry platform-evidence provenance", r.ID)
		}
		if r.Decision != DecisionAllow && strings.TrimSpace(r.Justification) == "" {
			return fmt.Errorf("rules: rule %s: justification is required for %s", r.ID, r.Decision)
		}
		r.Justification = strings.TrimSpace(r.Justification)

		if err := normalizeRuleSelectors(r); err != nil {
			return fmt.Errorf("rules: rule %s: %w", r.ID, err)
		}
		if len(r.Actions) == 0 && len(r.Programs) == 0 && len(r.Hosts) == 0 &&
			len(r.Methods) == 0 && len(r.PathPrefixes) == 0 {
			return fmt.Errorf("rules: rule %s: at least one selector is required", r.ID)
		}

		// Examples are validated against a copy without examples so validating a
		// rule cannot recurse into itself.
		base := *r
		base.Match = nil
		base.NotMatch = nil
		// Platform-evidence examples are checked at the rule's observation time,
		// not at load time, so a rule that is valid when written can still be
		// loaded after its valid_until passes (evaluation will expire it).
		exampleNow := time.Time{}
		if source == SourcePlatformEvidence {
			exampleNow = r.ObservedAt
		}
		for _, ex := range r.Match {
			req, err := normalizeRequest(Request{
				Action: ex.Action, Program: ex.Program, Asset: ex.Asset,
				Method: ex.Method, Path: ex.Path, Now: exampleNow,
			})
			if err != nil {
				return fmt.Errorf("rules: rule %s: match example asset: %w", r.ID, err)
			}
			if ok, _ := base.matches(req); !ok {
				return fmt.Errorf("rules: rule %s: match example does not match the rule", r.ID)
			}
		}
		for _, ex := range r.NotMatch {
			req, err := normalizeRequest(Request{
				Action: ex.Action, Program: ex.Program, Asset: ex.Asset,
				Method: ex.Method, Path: ex.Path, Now: exampleNow,
			})
			if err != nil {
				return fmt.Errorf("rules: rule %s: not_match example asset: %w", r.ID, err)
			}
			if ok, _ := base.matches(req); ok {
				return fmt.Errorf("rules: rule %s: not_match example matches the rule", r.ID)
			}
		}
	}
	return nil
}

// normalizeRuleSelectors canonicalizes every selector list in place. It is
// shared by operator-rule loading and the exported helpers authz uses for
// evidence-backed permission facts, so both use exactly the same matching
// semantics.
func normalizeRuleSelectors(r *Rule) error {
	for j, action := range r.Actions {
		if !action.Valid() {
			return fmt.Errorf("invalid action %q", action)
		}
		r.Actions[j] = action
	}
	for j, program := range r.Programs {
		program = domain.ProgramID(strings.TrimSpace(string(program)))
		if program == "" {
			return fmt.Errorf("empty program")
		}
		r.Programs[j] = program
	}
	for j, method := range r.Methods {
		m, err := normalizeMethod(method)
		if err != nil {
			return err
		}
		r.Methods[j] = m
	}
	for j, host := range r.Hosts {
		h, err := normalizeHostPattern(host)
		if err != nil {
			return err
		}
		r.Hosts[j] = h
	}
	for j, prefix := range r.PathPrefixes {
		p, err := normalizePathPrefix(prefix)
		if err != nil {
			return err
		}
		r.PathPrefixes[j] = p
	}
	return nil
}

// Evaluate returns the strictest decision among all matching rules. An empty
// Decision means no rule matched.
func (s *RuleSet) Evaluate(req Request) (Result, error) {
	if s == nil {
		return Result{}, nil
	}
	n, err := normalizeRequest(req)
	if err != nil {
		return Result{}, err
	}
	out := Result{}
	for _, r := range s.Rules {
		ok, matchedOn := r.matches(n)
		if !ok {
			continue
		}
		out.Matched = append(out.Matched, MatchedRule{
			ID: r.ID, Source: r.Source, Decision: r.Decision,
			Justification: r.Justification, MatchedOn: matchedOn,
		})
		if r.Decision.rank() > out.Decision.rank() {
			out.Decision = r.Decision
		}
	}
	return out, nil
}

func (s *RuleSet) Fingerprint() string {
	if s == nil {
		return ""
	}
	raw, err := json.Marshal(struct {
		Version string `json:"version,omitempty"`
		Rules   []Rule `json:"rules"`
	}{Version: s.Version, Rules: s.Rules})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type normalizedRequest struct {
	action  domain.Action
	program domain.ProgramID
	host    string
	path    string
	method  string
	now     time.Time
}

// NormalizedRequest is the canonical matching form of a Request. It is
// exported so evidence-backed authorizers (internal/authz) share exactly the
// same asset/method/path normalization as operator rules instead of
// re-implementing URL parsing.
type NormalizedRequest struct {
	Action  domain.Action
	Program domain.ProgramID
	Host    string
	Path    string
	Method  string
	Now     time.Time
}

// NormalizeRequest canonicalizes a Request for matching. Zero Now defaults to
// the current UTC time; an empty method/asset is allowed and simply does not
// match selectors that require one.
func NormalizeRequest(req Request) (NormalizedRequest, error) {
	n, err := normalizeRequest(req)
	if err != nil {
		return NormalizedRequest{}, err
	}
	return NormalizedRequest{
		Action: n.action, Program: n.program, Host: n.host,
		Path: n.path, Method: n.method, Now: n.now,
	}, nil
}

// NormalizeHostPattern validates and canonicalizes a host pattern using the
// same semantics as rule loading. It is exported for authz permission facts.
func NormalizeHostPattern(raw string) (string, error) { return normalizeHostPattern(raw) }

// NormalizeMethod uppercases and validates an HTTP method selector.
func NormalizeMethod(raw string) (string, error) { return normalizeMethod(raw) }

func normalizeRequest(req Request) (normalizedRequest, error) {
	out := normalizedRequest{
		action:  req.Action,
		program: domain.ProgramID(strings.TrimSpace(string(req.Program))),
		now:     req.Now,
	}
	if out.now.IsZero() {
		out.now = time.Now().UTC()
	}
	if out.action != "" && !out.action.Valid() {
		return out, fmt.Errorf("invalid action %q", out.action)
	}
	if req.Method != "" {
		m, err := normalizeMethod(req.Method)
		if err != nil {
			return out, err
		}
		out.method = m
	}
	if strings.TrimSpace(req.Asset) != "" {
		host, p, err := parseAsset(req.Asset)
		if err != nil {
			return out, err
		}
		out.host = host
		out.path = p
	}
	if strings.TrimSpace(req.Path) != "" {
		p, err := normalizePathPrefix(req.Path)
		if err != nil {
			return out, fmt.Errorf("path: %w", err)
		}
		out.path = p
	}
	return out, nil
}

func (r Rule) matches(req normalizedRequest) (bool, []string) {
	var matchedOn []string

	if r.Source == SourcePlatformEvidence {
		if r.ValidUntil.IsZero() || !r.ValidUntil.After(req.now) {
			return false, nil
		}
		if r.ObservedAt.After(req.now.Add(time.Minute)) {
			return false, nil
		}
	}

	if len(r.Actions) > 0 {
		found := false
		for _, a := range r.Actions {
			if a == req.action {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
		matchedOn = append(matchedOn, "action="+string(req.action))
	}
	if len(r.Programs) > 0 {
		found := false
		for _, p := range r.Programs {
			if p == req.program {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
		matchedOn = append(matchedOn, "program="+string(req.program))
	}
	if len(r.Hosts) > 0 {
		found := false
		for _, h := range r.Hosts {
			if hostMatches(h, req.host) {
				found = true
				matchedOn = append(matchedOn, "host="+h)
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	if len(r.Methods) > 0 {
		found := false
		for _, m := range r.Methods {
			if m == req.method {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
		matchedOn = append(matchedOn, "method="+req.method)
	}
	if len(r.PathPrefixes) > 0 {
		found := false
		for _, p := range r.PathPrefixes {
			if req.path != "" && PathWithin(p, req.path) {
				found = true
				matchedOn = append(matchedOn, "path_prefix="+p)
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, matchedOn
}

func normalizeMethod(raw string) (string, error) {
	m := strings.ToUpper(strings.TrimSpace(raw))
	if m == "" {
		return "", fmt.Errorf("method is empty")
	}
	return m, nil
}

func normalizeHostPattern(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", fmt.Errorf("host is empty")
	}
	if h == "*" {
		return "", fmt.Errorf("global wildcard host %q is not allowed", raw)
	}
	if strings.Contains(h, "://") || strings.ContainsAny(h, "/?#") {
		return "", fmt.Errorf("host %q must be a hostname or wildcard, not a URL", raw)
	}
	prefix := ""
	rest := h
	switch {
	case strings.HasPrefix(h, "**."):
		prefix, rest = "**.", h[3:]
	case strings.HasPrefix(h, "*."):
		prefix, rest = "*.", h[2:]
	}
	if rest == "" {
		return "", fmt.Errorf("host %q has an empty suffix", raw)
	}
	if strings.Contains(rest, "*") {
		return "", fmt.Errorf("host %q has an unsupported wildcard", raw)
	}
	if strings.Contains(rest, ":") {
		// IPv6 literals are exact hosts. Brackets are stripped; anything else
		// with a colon (a port) is invalid in a host pattern.
		if prefix != "" {
			return "", fmt.Errorf("host %q cannot be a wildcard IP literal", raw)
		}
		ip := strings.Trim(rest, "[]")
		if strings.Contains(ip, ":") && !isIPLiteral(ip) {
			return "", fmt.Errorf("host %q contains a port or invalid IPv6 literal", raw)
		}
		return prefix + ip, nil
	}
	return prefix + rest, nil
}

func isIPLiteral(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

func normalizePathPrefix(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("path prefix is empty")
	}
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("path prefix %q must not contain a query or fragment", raw)
	}
	return CanonicalPath(p)
}

// CanonicalPath normalizes a URL path for policy matching. It percent-decodes
// once, rejects encoded separators that remain after decoding (for example a
// double-encoded %252f), treats backslashes as separators, forces a leading
// slash, and removes dot segments with path.Clean.
func CanonicalPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "/", nil
	}
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("path %q must not contain a query or fragment", raw)
	}
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return "", fmt.Errorf("path %q: invalid escape: %w", raw, err)
	}
	p = decoded
	lower := strings.ToLower(p)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%2e") || strings.Contains(lower, "%5c") {
		return "", fmt.Errorf("path %q contains an encoded separator or dot segment", raw)
	}
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("path %q must not contain a query or fragment", raw)
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "." {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p, nil
}

// PathWithin reports whether path is prefix itself or a descendant of prefix.
// It is segment-aware: /api matches /api and /api/users, never /apix or
// /api-admin. Both arguments are canonicalized first.
func PathWithin(prefix, path string) bool {
	pre, err := CanonicalPath(prefix)
	if err != nil {
		return false
	}
	got, err := CanonicalPath(path)
	if err != nil {
		return false
	}
	if pre == "/" {
		return true
	}
	return got == pre || strings.HasPrefix(got, pre+"/")
}

func parseAsset(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	parse := raw
	if !strings.Contains(parse, "://") && !strings.HasPrefix(parse, "//") {
		parse = "//" + parse
	}
	u, err := url.Parse(parse)
	if err != nil {
		return "", "", fmt.Errorf("asset %q: %w", raw, err)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", "", fmt.Errorf("asset %q has no host", raw)
	}
	p, err := CanonicalPath(u.EscapedPath())
	if err != nil {
		return "", "", fmt.Errorf("asset %q: %w", raw, err)
	}
	return host, p, nil
}

// HostMatches reports whether host matches a host pattern. Supported forms are
// an exact host, "*.suffix" (subdomains only, never the apex), and
// "**.suffix" (the apex and subdomains). Matching is case-insensitive and
// ignores one trailing dot on either side.
func HostMatches(pattern, host string) bool { return hostMatches(pattern, host) }

func hostMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if pattern == "" || host == "" {
		return false
	}
	switch {
	case strings.HasPrefix(pattern, "**."):
		suffix := pattern[3:]
		if suffix == "" {
			return false
		}
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:] // includes the leading dot
		return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
	default:
		return host == pattern
	}
}
