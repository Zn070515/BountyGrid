package main

import "testing"

func TestParseRevokeOptions(t *testing.T) {
	opts, err := parseRevokeOptions([]string{"-program", "vulbox/444", "-reason", "manual kill switch"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Program != "vulbox/444" || opts.Reason != "manual kill switch" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseRevokeOptions(nil); err == nil {
		t.Fatal("a missing -program must fail")
	}
	if _, err := parseRevokeOptions([]string{"-program", "  "}); err == nil {
		t.Fatal("a blank -program must fail")
	}
	if _, err := parseRevokeOptions([]string{"-bogus"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
}
