package main

import "testing"

func TestParseHarnessOptions(t *testing.T) {
	opts, err := parseHarnessOptions([]string{"-tool", "view_rules", "-request", "req.json", "-budget", "50", "-allowed-hosts", "api.example.com,*.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Tool != "view_rules" || opts.RequestFile != "req.json" || opts.BudgetCNY != 50 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.AllowedHosts != "api.example.com,*.example.org" {
		t.Fatalf("hosts = %q", opts.AllowedHosts)
	}
	if _, err := parseHarnessOptions([]string{"-tool", "view_rules"}); err == nil {
		t.Fatal("missing request should fail")
	}
}
