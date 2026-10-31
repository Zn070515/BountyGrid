package butian

import (
	"context"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
)

func TestGrowExtractorDates(t *testing.T) {
	pid := domain.ProgramID("butian:grow")
	in := facts.Input{
		Platform:   domain.PlatformButian,
		ProgramID:  &pid,
		Text:       "AI 辅助漏洞挖掘规则\n报名时间：2026.8.25~2026.11.30\n活动时间：2026.9.1~2026.11.30\n2026GROW计划漏洞挖掘实战-第二期 2026/10/01 - 2026/10/31",
		CapturedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		EvidenceID: "ev-grow",
	}
	got, err := (GrowExtractor{}).Extract(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	fs := facts.NewFactSet(got)

	cases := []struct {
		pred string
		want string
	}{
		{facts.PredRegistrationStart, "2026-08-24T16:00:00Z"},
		{facts.PredRegistrationEnd, "2026-11-30T15:59:59.999999999Z"},
		{facts.PredActivityStart, "2026-08-31T16:00:00Z"},
		{facts.PredActivityEnd, "2026-11-30T15:59:59.999999999Z"},
		{facts.PredPhaseStart, "2026-09-30T16:00:00Z"},
		{facts.PredPhaseEnd, "2026-10-31T15:59:59.999999999Z"},
	}
	for _, c := range cases {
		got, ok := fs.Time(c.pred)
		if !ok {
			t.Fatalf("%s missing", c.pred)
		}
		if gotStr := got.UTC().Format(time.RFC3339Nano); gotStr != c.want {
			t.Fatalf("%s = %s, want %s", c.pred, gotStr, c.want)
		}
	}
}

// The activity center lists other campaigns before GROW. Only the range that
// follows the GROW title is the monthly phase.
func TestGrowExtractorAnchorsPhaseToGrow(t *testing.T) {
	pid := domain.ProgramID("butian:grow")
	in := facts.Input{
		Platform:  domain.PlatformButian,
		ProgramID: &pid,
		Text: "2026中秋活动 2026/08/20 - 2026/09/03\n" +
			"2026GROW计划漏洞挖掘实战-第二期 已结束 2026/10/01 - 2026/10/31",
		CapturedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		EvidenceID: "ev-grow",
	}
	got, err := (GrowExtractor{}).Extract(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	fs := facts.NewFactSet(got)
	start, ok := fs.Time(facts.PredPhaseStart)
	if !ok {
		t.Fatal("phase start missing")
	}
	if got := start.UTC().Format(time.RFC3339Nano); got != "2026-09-30T16:00:00Z" {
		t.Fatalf("phase start = %s, want the GROW month", got)
	}
	if _, ok := fs.Time(facts.PredRegistrationStart); ok {
		t.Fatal("activity center text must not invent registration dates")
	}
}

func TestGrowExtractorRejectsMissingDates(t *testing.T) {
	pid := domain.ProgramID("butian:grow")
	in := facts.Input{
		Platform:   domain.PlatformButian,
		ProgramID:  &pid,
		Text:       "no dates here",
		CapturedAt: time.Now().UTC(),
		EvidenceID: "ev-grow",
	}
	if _, err := (GrowExtractor{}).Extract(context.Background(), in); err == nil {
		t.Fatal("expected error for missing dates")
	}
}
