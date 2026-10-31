package main

import "testing"

func TestParseDecideOptions(t *testing.T) {
	opts, err := parseDecideOptions([]string{"-request", "req.json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.RequestFile != "req.json" {
		t.Fatalf("opts = %+v", opts)
	}
	opts, err = parseDecideOptions([]string{"-authorize", "dec-1"})
	if err != nil || opts.Authorize != "dec-1" {
		t.Fatalf("opts = %+v err=%v", opts, err)
	}
	if _, err := parseDecideOptions(nil); err == nil {
		t.Fatal("empty selection should fail")
	}
	if _, err := parseDecideOptions([]string{"-request", "r", "-authorize", "d"}); err == nil {
		t.Fatal("request+authorize should fail")
	}
}
