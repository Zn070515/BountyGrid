// Package butian contains Butian page/text extractors.
package butian

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/facts"
	"github.com/Zn070515/BountyGrid/internal/timeutil"
)

// GrowExtractor parses the GROW AI-track rules modal and the activity-center
// monthly phase. It intentionally emits only what the text explicitly states.
type GrowExtractor struct{}

func (GrowExtractor) Name() string    { return "butian/grow" }
func (GrowExtractor) Version() string { return "1.0.0" }

var (
	growRegistrationRE = regexp.MustCompile(`报名时间：\s*([0-9./]+)\s*[~\-]\s*([0-9./]+)`)
	growActivityRE     = regexp.MustCompile(`活动时间：\s*([0-9./]+)\s*[~\-]\s*([0-9./]+)`)
	// The activity center lists several campaigns, each with its own date
	// range. Anchor the monthly phase to the GROW title so an unrelated
	// campaign that renders first cannot be mistaken for the GROW phase.
	growPhaseRE = regexp.MustCompile(`GROW[^0-9]{0,80}(20[0-9]{2}/[0-9]{1,2}/[0-9]{1,2})\s*-\s*(20[0-9]{2}/[0-9]{1,2}/[0-9]{1,2})`)
)

func (GrowExtractor) Extract(ctx context.Context, in facts.Input) ([]domain.ObservedFact, error) {
	if in.ProgramID == nil || *in.ProgramID == "" {
		return nil, fmt.Errorf("butian/grow: program_id is required")
	}
	if in.EvidenceID == "" {
		return nil, fmt.Errorf("butian/grow: evidence_id is required")
	}
	if in.CapturedAt.IsZero() {
		return nil, fmt.Errorf("butian/grow: captured_at is required")
	}

	var out []domain.ObservedFact
	addRange := func(re *regexp.Regexp, startPred, endPred string) error {
		m := re.FindStringSubmatch(in.Text)
		if m == nil {
			return nil
		}
		start, err := parseDate(m[1], false)
		if err != nil {
			return fmt.Errorf("butian/grow: parse %s start: %w", startPred, err)
		}
		end, err := parseDate(m[2], true)
		if err != nil {
			return fmt.Errorf("butian/grow: parse %s end: %w", endPred, err)
		}
		out = append(out, makeFact(in, startPred, start), makeFact(in, endPred, end))
		return nil
	}

	if err := addRange(growRegistrationRE, facts.PredRegistrationStart, facts.PredRegistrationEnd); err != nil {
		return nil, err
	}
	if err := addRange(growActivityRE, facts.PredActivityStart, facts.PredActivityEnd); err != nil {
		return nil, err
	}
	if m := growPhaseRE.FindStringSubmatch(in.Text); m != nil {
		start, err := parseDate(m[1], false)
		if err != nil {
			return nil, fmt.Errorf("butian/grow: parse phase start: %w", err)
		}
		end, err := parseDate(m[2], true)
		if err != nil {
			return nil, fmt.Errorf("butian/grow: parse phase end: %w", err)
		}
		out = append(out,
			makeFact(in, facts.PredPhaseStart, start),
			makeFact(in, facts.PredPhaseEnd, end),
		)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("butian/grow: no GROW dates found")
	}
	return out, nil
}

func makeFact(in facts.Input, pred string, v any) domain.ObservedFact {
	id := domain.FactID(fmt.Sprintf("%s/%s", in.EvidenceID, strings.ReplaceAll(pred, ".", "_")))
	return domain.ObservedFact{
		ID:               id,
		Subject:          domain.SubjectProgram,
		Platform:         domain.PlatformButian,
		ProgramID:        in.ProgramID,
		Predicate:        pred,
		Value:            v,
		Unit:             facts.UnitDate,
		ObservedAt:       in.CapturedAt,
		EvidenceRefs:     []domain.EvidenceID{in.EvidenceID},
		Confidence:       0.9,
		ExtractorVersion: "butian/grow@1.0.0",
	}
}

// parseDate parses a date-only value in the platform's business timezone.
// endOfDay makes the end boundary inclusive (23:59:59.999999999 local), which
// matches how Chinese platforms display "截止到 11 月 30 日".
func parseDate(s string, endOfDay bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	loc := timeutil.BusinessLocation()
	layouts := []string{"2006.1.2", "2006.01.02", "2006/1/2", "2006/01/02"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			if endOfDay {
				t = t.Add(24*time.Hour - time.Nanosecond)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", s)
}
