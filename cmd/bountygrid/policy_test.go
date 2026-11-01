package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testRulesJSON = `{
  "version": "cli-v1",
  "rules": [
    {
      "id": "allow-read",
      "actions": ["ACTIVE_HTTP"],
      "hosts": ["*.example.com"],
      "methods": ["GET"],
      "decision": "allow",
      "justification": "read-only",
      "match": [{"action": "ACTIVE_HTTP", "asset": "https://api.example.com/v1", "method": "GET"}],
      "not_match": [{"action": "ACTIVE_HTTP", "asset": "https://example.com/v1", "method": "GET"}]
    },
    {
      "id": "deny-admin",
      "actions": ["ACTIVE_HTTP"],
      "hosts": ["admin.example.com"],
      "decision": "forbidden",
      "justification": "out of scope",
      "match": [{"action": "ACTIVE_HTTP", "asset": "https://admin.example.com/", "method": "GET"}]
    }
  ]
}`

func writeRulesFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	name := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestParsePolicyOptions(t *testing.T) {
	opts, err := parsePolicyOptions([]string{
		"-rules", "r.json", "-action", "ACTIVE_HTTP",
		"-program", "vulbox/444", "-asset", "https://api.example.com/v1",
		"-method", "get", "-path", "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.RulesFile != "r.json" || opts.Action != "ACTIVE_HTTP" ||
		opts.Program != "vulbox/444" || opts.Method != "get" || opts.Path != "/v1" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parsePolicyOptions(nil); err == nil {
		t.Fatal("missing -rules/-action should fail")
	}
	if _, err := parsePolicyOptions([]string{"-rules", "r.json"}); err == nil {
		t.Fatal("missing -action should fail")
	}
}

func TestExecutePolicyCheckAllow(t *testing.T) {
	out, err := executePolicyCheck(policyOptions{
		RulesFile: writeRulesFile(t, testRulesJSON),
		Action:    "ACTIVE_HTTP",
		Asset:     "https://api.example.com/v1/items",
		Method:    "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "allow" || len(out.Matched) != 1 || out.Matched[0].ID != "allow-read" {
		t.Fatalf("out = %+v", out)
	}
	if out.Version != "cli-v1" || out.Fingerprint == "" {
		t.Fatalf("metadata = %+v", out)
	}
}

func TestExecutePolicyCheckStrictestAndNoMatch(t *testing.T) {
	out, err := executePolicyCheck(policyOptions{
		RulesFile: writeRulesFile(t, testRulesJSON),
		Action:    "ACTIVE_HTTP",
		Asset:     "https://admin.example.com/",
		Method:    "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "forbidden" || len(out.Matched) != 2 {
		t.Fatalf("forbidden out = %+v", out)
	}

	out, err = executePolicyCheck(policyOptions{
		RulesFile: writeRulesFile(t, testRulesJSON),
		Action:    "ACTIVE_HTTP",
		Asset:     "https://example.com/",
		Method:    "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "" || len(out.Matched) != 0 {
		t.Fatalf("no-match out = %+v", out)
	}
}

func TestExecutePolicyCheckExampleFile(t *testing.T) {
	out, err := executePolicyCheck(policyOptions{
		RulesFile: "../../rules/example.rules.json",
		Action:    "ACTIVE_HTTP",
		Program:   "vulbox/444",
		Asset:     "https://api.example.com/v1/items",
		Method:    "GET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "allow" || len(out.Matched) != 1 || out.Matched[0].ID != "vulbox-444-read-only" {
		t.Fatalf("example out = %+v", out)
	}
}

func TestLoadOptionalRules(t *testing.T) {
	if rs, err := loadOptionalRules(""); err != nil || rs != nil {
		t.Fatalf("empty rules = %v, %v", rs, err)
	}
	if rs, err := loadOptionalRules("../../rules/example.rules.json"); err != nil || rs == nil {
		t.Fatalf("example rules = %v, %v", rs, err)
	}
}

func TestExecutePolicyCheckRejectsInvalidRules(t *testing.T) {
	if _, err := executePolicyCheck(policyOptions{
		RulesFile: writeRulesFile(t, `{"rules":[]}`),
		Action:    "ACTIVE_HTTP",
	}); err == nil {
		t.Fatal("empty rule set should fail")
	}
	if _, err := executePolicyCheck(policyOptions{
		RulesFile: writeRulesFile(t, testRulesJSON),
		Action:    "NOT_AN_ACTION",
	}); err == nil {
		t.Fatal("invalid action should fail")
	}
}
