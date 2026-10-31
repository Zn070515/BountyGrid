package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func TestBuildCompetitionSnapshot(t *testing.T) {
	top10 := 0.708
	in := competitionInput{
		Platform: "xianzhi", Scope: "PLATFORM", ObservedAt: testNow,
		Top10Share: &top10, Confidence: 0.7,
		EvidenceURL:   "https://xianzhi.aliyun.com/#/home/heroes-rank",
		EvidenceTitle: "先知英雄榜", EvidenceText: "前10赏金占可见总额70.8%",
	}
	dir := t.TempDir()
	snap, ev, err := buildCompetitionSnapshot(in, testNow, dir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Platform != domain.PlatformXianzhi || snap.Scope != "PLATFORM" || snap.Top10Share == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !strings.HasPrefix(string(snap.ID), "comp/xianzhi/PLATFORM/") {
		t.Fatalf("id = %s", snap.ID)
	}
	if len(snap.Evidence) != 1 || snap.Evidence[0] != ev.ID {
		t.Fatalf("evidence = %v", snap.Evidence)
	}
	raw, err := os.ReadFile(ev.RawRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "70.8%") {
		t.Fatalf("raw evidence = %s", raw)
	}
}

func TestBuildCompetitionSnapshotRejectsBadShare(t *testing.T) {
	bad := 1.5
	in := competitionInput{
		Platform: "vulbox", Scope: "PLATFORM", ObservedAt: testNow,
		Top10Share: &bad, Confidence: 0.5,
		EvidenceURL: "https://example.test", EvidenceText: "text",
	}
	if _, _, err := buildCompetitionSnapshot(in, testNow, t.TempDir()); err == nil {
		t.Fatal("out-of-range share should be rejected")
	}
}

func TestParseCompetitionOptions(t *testing.T) {
	opts, err := parseCompetitionOptions([]string{"-file", "comp.json", "-persist"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.File != "comp.json" || !opts.Persist {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseCompetitionOptions(nil); err == nil {
		t.Fatal("missing file should fail")
	}
}
