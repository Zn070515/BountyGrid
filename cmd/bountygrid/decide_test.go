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

func TestParseDecideOptionsPlatformEvidence(t *testing.T) {
	opts, err := parseDecideOptions([]string{
		"-request", "req.json", "-platform-evidence", "ev-1, ev-2", "-content-root", ".evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.PlatformEvidence) != 2 || opts.PlatformEvidence[0] != "ev-1" ||
		opts.PlatformEvidence[1] != "ev-2" || opts.ContentRoot != ".evidence" {
		t.Fatalf("opts = %+v", opts)
	}
	// -authorize must accept the same authority so a decision bound to a
	// platform-policy fingerprint can be re-evaluated under that fingerprint.
	opts, err = parseDecideOptions([]string{"-authorize", "dec-1", "-platform-evidence", "ev-1"})
	if err != nil || len(opts.PlatformEvidence) != 1 {
		t.Fatalf("authorize opts = %+v err=%v", opts, err)
	}
	if _, err := parseDecideOptions([]string{"-request", "r", "-content-root", " "}); err == nil {
		t.Fatal("blank content root should fail")
	}
}
