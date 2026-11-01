package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const allowRuleSet = `{
  "version": "test-v1",
  "rules": [
    {
      "id": "allow-api-read",
      "actions": ["ACTIVE_HTTP"],
      "hosts": ["*.example.com"],
      "methods": ["GET", "HEAD"],
      "decision": "allow",
      "justification": "public read-only scope",
      "match": [
        {"action": "ACTIVE_HTTP", "asset": "https://api.example.com/v1/items", "method": "GET"}
      ],
      "not_match": [
        {"action": "ACTIVE_HTTP", "asset": "https://example.com/v1/items", "method": "GET"},
        {"action": "ACTIVE_HTTP", "asset": "https://api.example.com/v1/items", "method": "POST"}
      ]
    },
    {
      "id": "deny-admin",
      "actions": ["ACTIVE_HTTP"],
      "hosts": ["admin.example.com"],
      "decision": "forbidden",
      "justification": "admin interface is out of scope",
      "match": [
        {"action": "ACTIVE_HTTP", "asset": "https://admin.example.com/", "method": "GET"}
      ]
    }
  ]
}`

func mustParse(t *testing.T, raw string) *RuleSet {
	t.Helper()
	rs, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestEvaluateMatchesHostMethodAndStrictestDecision(t *testing.T) {
	rs := mustParse(t, allowRuleSet)
	if rs.Version != "test-v1" {
		t.Fatalf("version = %q", rs.Version)
	}

	res, err := rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP,
		Asset:  "https://api.example.com/v1/items",
		Method: "get",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAllow || len(res.Matched) != 1 || res.Matched[0].ID != "allow-api-read" {
		t.Fatalf("allow result = %+v", res)
	}

	// Both the wildcard allow and the exact admin forbid match; the strictest wins.
	res, err = rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP,
		Asset:  "https://admin.example.com/",
		Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionForbidden || len(res.Matched) != 2 {
		t.Fatalf("strictest result = %+v", res)
	}
	if ids := res.IDs(); len(ids) != 2 || ids[0] != "allow-api-read" || ids[1] != "deny-admin" {
		t.Fatalf("ids = %v", ids)
	}
	if reasons := res.Reasons(); len(reasons) != 2 || !strings.HasPrefix(reasons[1], "RULE_FORBIDDEN:deny-admin") {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestWildcardDoesNotMatchApex(t *testing.T) {
	rs := mustParse(t, allowRuleSet)
	res, err := rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP,
		Asset:  "https://example.com/v1/items",
		Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionNone || len(res.Matched) != 0 {
		t.Fatalf("apex result = %+v", res)
	}
}

func TestDoubleWildcardMatchesApexAndSubdomains(t *testing.T) {
	rs := mustParse(t, `{
      "rules": [{
        "id": "allow-apex",
        "actions": ["ACTIVE_HTTP"],
        "hosts": ["**.example.com"],
        "decision": "allow",
        "justification": "apex and subdomains",
        "match": [{"action": "ACTIVE_HTTP", "asset": "https://example.com/"}]
      }]
    }`)
	for _, asset := range []string{"https://example.com/", "https://api.example.com/"} {
		res, err := rs.Evaluate(Request{Action: domain.ActionActiveHTTP, Asset: asset, Method: "GET"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != DecisionAllow {
			t.Fatalf("%s = %+v", asset, res)
		}
	}
}

func TestMethodAndPathFilters(t *testing.T) {
	rs := mustParse(t, `{
      "rules": [{
        "id": "allow-v1-get",
        "actions": ["ACTIVE_HTTP"],
        "hosts": ["api.example.com"],
        "methods": ["GET"],
        "path_prefixes": ["/v1"],
        "decision": "allow",
        "justification": "v1 read",
        "match": [{"action": "ACTIVE_HTTP", "asset": "https://api.example.com/v1/items", "method": "GET"}]
      }]
    }`)

	cases := []struct {
		name string
		req  Request
		want Decision
	}{
		{"get-v1", Request{Action: domain.ActionActiveHTTP, Asset: "https://api.example.com/v1/items", Method: "GET"}, DecisionAllow},
		{"post-v1", Request{Action: domain.ActionActiveHTTP, Asset: "https://api.example.com/v1/items", Method: "POST"}, DecisionNone},
		{"get-v2", Request{Action: domain.ActionActiveHTTP, Asset: "https://api.example.com/v2/items", Method: "GET"}, DecisionNone},
		{"get-no-path", Request{Action: domain.ActionActiveHTTP, Asset: "https://api.example.com", Method: "GET"}, DecisionNone},
		{"get-empty-method", Request{Action: domain.ActionActiveHTTP, Asset: "https://api.example.com/v1/items"}, DecisionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := rs.Evaluate(c.req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != c.want {
				t.Fatalf("decision = %q, want %q (%+v)", res.Decision, c.want, res)
			}
		})
	}
}

func TestURLIsCanonicalizedBeforeMatching(t *testing.T) {
	rs := mustParse(t, `{
      "rules": [{
        "id": "allow-v1",
        "actions": ["ACTIVE_HTTP"],
        "hosts": ["api.example.com"],
        "path_prefixes": ["/v1"],
        "decision": "allow",
        "justification": "v1 read",
        "match": [{"action": "ACTIVE_HTTP", "asset": "https://api.example.com/v1"}]
      }]
    }`)

	// Uppercase host, trailing dot, and percent-encoded path all normalize.
	res, err := rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP,
		Asset:  "https://API.Example.COM./%76%31/items",
		Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("canonicalized result = %+v", res)
	}

	// Path traversal is cleaned, so /v1/../admin does not match /v1.
	res, err = rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP,
		Asset:  "https://api.example.com/v1/../admin",
		Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionNone {
		t.Fatalf("traversal result = %+v", res)
	}
}

func TestProgramAndActionSelectors(t *testing.T) {
	rs := mustParse(t, `{
      "rules": [{
        "id": "allow-444",
        "actions": ["ACTIVE_HTTP"],
        "programs": ["vulbox/444"],
        "hosts": ["*.example.com"],
        "decision": "allow",
        "justification": "program-scoped",
        "match": [{"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://api.example.com/"}],
        "not_match": [{"action": "ACTIVE_HTTP", "program": "vulbox/999", "asset": "https://api.example.com/"}]
      }]
    }`)
	res, err := rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP, Program: "vulbox/444",
		Asset: "https://api.example.com/", Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("program match = %+v", res)
	}
	res, err = rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP, Program: "vulbox/999",
		Asset: "https://api.example.com/", Method: "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionNone {
		t.Fatalf("program mismatch = %+v", res)
	}
}

func TestParseRejectsInvalidRuleSets(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bad-json", `{`, "decode"},
		{"no-rules", `{"rules":[]}`, "no rules"},
		{"duplicate-id", `{"rules":[
          {"id":"a","hosts":["api.example.com"],"decision":"allow","justification":"x"},
          {"id":"a","hosts":["other.example.com"],"decision":"allow","justification":"x"}
        ]}`, "duplicate"},
		{"invalid-decision", `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"maybe"}]}`, "decision"},
		{"no-selectors", `{"rules":[{"id":"a","decision":"allow","justification":"x"}]}`, "selector"},
		{"global-wildcard", `{"rules":[{"id":"a","hosts":["*"],"decision":"allow","justification":"x"}]}`, "wildcard"},
		{"host-with-scheme", `{"rules":[{"id":"a","hosts":["https://api.example.com"],"decision":"allow","justification":"x"}]}`, "host"},
		{"forbidden-without-justification", `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"forbidden"}]}`, "justification"},
		{"match-example-mismatch", `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"allow","justification":"x",
          "match":[{"asset":"https://other.example.com/"}]}]}`, "match example"},
		{"not-match-example-matches", `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"allow","justification":"x",
          "not_match":[{"asset":"https://api.example.com/"}]}]}`, "not_match example"},
		{"invalid-example-asset", `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"allow","justification":"x",
          "match":[{"asset":"https://user:pass@/bad"}]}]}`, "asset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.raw))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
				t.Fatalf("err = %v, want substring %q", err, c.want)
			}
		})
	}
}

func TestDecisionAliasesAreNormalized(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"id":"a","hosts":["api.example.com"],"decision":"prompt","justification":"x"}]}`)
	if rs.Rules[0].Decision != DecisionRequireHuman {
		t.Fatalf("decision = %q", rs.Rules[0].Decision)
	}
}

func TestFingerprintIsStable(t *testing.T) {
	a := mustParse(t, allowRuleSet)
	b := mustParse(t, allowRuleSet)
	if a.Fingerprint() == "" || a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("fingerprints = %q / %q", a.Fingerprint(), b.Fingerprint())
	}
}

func TestEvaluateRejectsInvalidRequestAsset(t *testing.T) {
	rs := mustParse(t, allowRuleSet)
	if _, err := rs.Evaluate(Request{Action: domain.ActionActiveHTTP, Asset: "://bad"}); err == nil {
		t.Fatal("invalid asset should fail closed")
	}
}

func TestExampleRuleFileLoads(t *testing.T) {
	rs, err := LoadFile("../../../rules/example.rules.json")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version == "" || len(rs.Rules) == 0 {
		t.Fatalf("example = %+v", rs)
	}
	// The platform-evidence sample is a negative fixture: platform authority is
	// never loaded from JSON, even when the file looks complete.
	if _, err := LoadFile("../../../rules/platform-evidence.example.json"); err == nil ||
		!strings.Contains(err.Error(), "platform_evidence") || !strings.Contains(err.Error(), "internal/authz") {
		t.Fatalf("platform evidence file was not rejected clearly: %v", err)
	}
}

func platformRuleJSON(validUntil string) string {
	return `{"rules":[{
		"id":"platform-allow",
		"actions":["ACTIVE_HTTP"],
		"hosts":["api.example.com"],
		"methods":["GET"],
		"decision":"allow",
		"source":"platform_evidence",
		"evidence_refs":["ev-1"],
		"observed_at":"2026-11-01T00:00:00Z",
		"valid_until":"` + validUntil + `",
		"policy_revision":"sha256:rev",
		"justification":"platform evidence explicitly permits this endpoint",
		"match":[{"action":"ACTIVE_HTTP","asset":"https://api.example.com/v1","method":"GET"}]
	}]}`
}

func TestOperatorAllowCannotRelax(t *testing.T) {
	rs := mustParse(t, allowRuleSet)
	res, err := rs.Evaluate(Request{
		Action: domain.ActionActiveHTTP, Asset: "https://api.example.com/v1", Method: "GET", Now: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAllow || res.CanRelax {
		t.Fatalf("operator allow = %+v", res)
	}
}

func TestRuleFileRejectsPlatformEvidence(t *testing.T) {
	for _, raw := range []string{
		platformRuleJSON("2026-11-02T00:00:00Z"),
		`{"rules":[{"id":"p","actions":["ACTIVE_HTTP"],"decision":"allow","source":"platform_evidence","justification":"x"}]}`,
		`{"rules":[{"id":"p","actions":["ACTIVE_HTTP"],"decision":"allow","source":"platform_evidence","evidence_refs":["ev"],"observed_at":"2026-11-01T00:00:00Z","valid_until":"2026-11-02T00:00:00Z","policy_revision":"sha256:rev","justification":"x"}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil ||
			!strings.Contains(err.Error(), "platform_evidence") ||
			!strings.Contains(err.Error(), "internal/authz") {
			t.Fatalf("platform rule loaded or unclear error: %v\n%s", err, raw)
		}
	}
}

func TestOperatorRuleRejectsPlatformProvenance(t *testing.T) {
	raw := `{"rules":[{"id":"o","actions":["ACTIVE_HTTP"],"decision":"allow","evidence_refs":["ev"],"justification":"x"}]}`
	if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("err = %v", err)
	}
}

func TestPathWithinSegmentBoundaries(t *testing.T) {
	cases := []struct {
		prefix, path string
		want         bool
	}{
		{"/api", "/api", true},
		{"/api", "/api/users", true},
		{"/api", "/api/", true},
		{"/api", "/apix", false},
		{"/api", "/api2", false},
		{"/api", "/api-admin", false},
		{"/api", "/api/%2e%2e/admin", false},
		{"/api", "/api%2f..%2fadmin", false},
		{"/api", "/api\\..\\admin", false},
		{"/", "/anything", true},
	}
	for _, c := range cases {
		if got := PathWithin(c.prefix, c.path); got != c.want {
			t.Fatalf("PathWithin(%q,%q) = %v, want %v", c.prefix, c.path, got, c.want)
		}
	}
}

func TestCanonicalPathRejectsInvalidEscapes(t *testing.T) {
	if _, err := CanonicalPath("/api/%zz"); err == nil {
		t.Fatal("invalid escape should fail")
	}
	if _, err := CanonicalPath("/api/%252e%252e/admin"); err == nil {
		t.Fatal("double-encoded dot segment should fail")
	}
	got, err := CanonicalPath("/api/../admin")
	if err != nil || got != "/admin" {
		t.Fatalf("clean = %q, %v", got, err)
	}
}
