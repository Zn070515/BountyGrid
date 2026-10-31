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
		want time.Time
	}{
		{facts.PredRegistrationStart, time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)},
		{facts.PredRegistrationEnd, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)},
		{facts.PredActivityStart, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{facts.PredActivityEnd, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)},
		{facts.PredPhaseStart, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{facts.PredPhaseEnd, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, ok := fs.Time(c.pred)
		if !ok || !got.Equal(c.want) {
			t.Fatalf("%s = %v (ok=%v), want %v", c.pred, got, ok, c.want)
		}
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
