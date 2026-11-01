// Package opportunity scores programs by expected return per cost. It never
// sorts by advertised maximum bounty; competition and confidence are part of
// the score.
package opportunity

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var ErrNoReward = errors.New("opportunity: no reward range")

// missingShareConf caps scoring confidence when the competition snapshot omits
// a share and the engine falls back to a conservative 0.5 estimate.
const missingShareConf domain.Confidence = 0.5

type Weights struct {
	Min, Median, Max float64
	Top10            float64
	Duplicate        float64
	Saturation       float64
	AssetTier        float64
	Campaign         float64
	PValid           float64
}

func DefaultWeights() Weights {
	return Weights{
		Min: 0.25, Median: 0.5, Max: 0.25,
		Top10: 0.4, Duplicate: 0.35, Saturation: 0.25,
		AssetTier: 1, Campaign: 1,
		PValid: 0.3,
	}
}

type Costs struct {
	HumanHours  float64
	HourlyRate  float64
	ReviewHours float64
	ReviewRate  float64
	Compute     float64
	Tool        float64
	Risk        float64
}

func DefaultCosts() Costs {
	return Costs{HumanHours: 4, HourlyRate: 100, ReviewHours: 0.5, ReviewRate: 100}
}

type Engine struct {
	Weights    Weights
	Costs      Costs
	NoDataConf domain.Confidence
}

func NewEngine() Engine {
	return Engine{Weights: DefaultWeights(), Costs: DefaultCosts(), NoDataConf: 0.2}
}

// History is the ledger-measured outcome for a program. It lets the score use
// actual acceptance/payout instead of only advertised bounty ranges.
type History struct {
	Submitted   int
	Accepted    int
	Paid        int
	AvgAccepted domain.Money
	AvgPaid     domain.Money
	ActualCost  domain.Money
}

func (h *History) AcceptanceRate() (float64, bool) {
	if h == nil || h.Submitted <= 0 {
		return 0, false
	}
	rate := float64(h.Accepted) / float64(h.Submitted)
	return clamp01(rate), true
}

type Input struct {
	Program     domain.Program
	Status      domain.ProgramStatus
	Account     domain.Account
	Competition *domain.CompetitionSnapshot
	History     *History
	Now         time.Time
}

const (
	// historyPriorWeight shrinks a small sample toward the base P(valid) instead
	// of letting one accepted/rejected report dominate the score.
	historyPriorWeight = 5.0
	// rewardPriorWeight does the same for observed cash versus advertised range.
	rewardPriorWeight = 3.0
)

func (e Engine) Evaluate(in Input) (domain.Opportunity, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if in.Program.Reward.Min == nil || in.Program.Reward.Max == nil {
		return domain.Opportunity{}, ErrNoReward
	}
	min := float64(in.Program.Reward.Min.Amount)
	max := float64(in.Program.Reward.Max.Amount)
	if min < 0 || max <= 0 || max < min {
		return domain.Opportunity{}, ErrNoReward
	}
	median := (min + max) / 2
	weighted := e.Weights.Min*min + e.Weights.Median*median + e.Weights.Max*max

	top10, dup, sat := 0.5, 0.5, 0.5
	conf := e.NoDataConf
	decay := 1.0
	comp := competitionAt(in.Competition, now)
	if comp != nil {
		if comp.Top10Share != nil {
			top10 = clamp01(*comp.Top10Share)
		}
		if comp.DuplicatePressure != nil {
			dup = clamp01(*comp.DuplicatePressure)
		}
		if comp.Saturation != nil {
			sat = clamp01(*comp.Saturation)
		}
		conf = comp.Confidence
		if comp.Top10Share == nil {
			// A guessed 0.5 share must not inherit the snapshot's full
			// confidence; missing top10 share caps confidence at 0.5.
			conf = minConf(conf, missingShareConf)
		}
		decay = competitionDecay(comp, now)
		conf = domain.Confidence(float64(conf) * decay)
	}
	penalty := e.Weights.Top10*top10 + e.Weights.Duplicate*dup + e.Weights.Saturation*sat
	penalty = math.Min(penalty, 0.95)

	pValid := e.Weights.PValid
	historyNote := "history=none"
	if rate, ok := in.History.AcceptanceRate(); ok {
		// Shrink toward the base prior: one report must not set P(valid) to 0 or 1.
		pValid = (rate*float64(in.History.Submitted) + e.Weights.PValid*historyPriorWeight) /
			(float64(in.History.Submitted) + historyPriorWeight)
		historyNote = fmt.Sprintf("history_submitted=%d history_accepted=%d history_p_valid=%.2f",
			in.History.Submitted, in.History.Accepted, pValid)
	}
	observedReward := int64(0)
	if in.History != nil {
		if in.History.AvgPaid.Amount > 0 {
			observedReward = in.History.AvgPaid.Amount
		} else if in.History.AvgAccepted.Amount > 0 {
			observedReward = in.History.AvgAccepted.Amount
		}
	}
	if observedReward > 0 && in.History.Submitted > 0 {
		// Blend advertised reward with observed cash. Advertised ranges are
		// marketing numbers; a program that consistently pays far below its max
		// should lose score.
		n := float64(in.History.Submitted)
		weighted = (weighted*rewardPriorWeight + float64(observedReward)*n) / (rewardPriorWeight + n)
		historyNote += fmt.Sprintf(" history_observed_reward=%.0f", float64(observedReward))
	}
	expectedReward := pValid * weighted * e.Weights.AssetTier * e.Weights.Campaign * (1 - dup)
	humanCost := e.Costs.HumanHours*e.Costs.HourlyRate + e.Costs.ReviewHours*e.Costs.ReviewRate
	cost := humanCost + e.Costs.Compute + e.Costs.Tool + e.Costs.Risk
	if in.History != nil && in.History.ActualCost.Amount > 0 {
		cost = float64(in.History.ActualCost.Amount)
		historyNote += fmt.Sprintf(" history_actual_cost=%.0f", cost)
	}
	if cost <= 0 {
		cost = 1
	}
	stateConf := in.Program.Status.Confidence
	if stateConf <= 0 {
		stateConf = 0.5
	}
	confidence := minConf(stateConf, conf)
	score := expectedReward * (1 - penalty) / cost * float64(confidence)

	inputs := []string{
		fmt.Sprintf("p_valid=%.2f", pValid),
		historyNote,
		fmt.Sprintf("reward_min=%.0f", min),
		fmt.Sprintf("reward_median=%.0f", median),
		fmt.Sprintf("reward_max=%.0f", max),
		fmt.Sprintf("weights=min%.2f/median%.2f/max%.2f", e.Weights.Min, e.Weights.Median, e.Weights.Max),
		fmt.Sprintf("competition=top10 %.2f/duplicate %.2f/saturation %.2f", top10, dup, sat),
		fmt.Sprintf("competition_weights=%.2f/%.2f/%.2f", e.Weights.Top10, e.Weights.Duplicate, e.Weights.Saturation),
		fmt.Sprintf("competition_penalty=%.3f", penalty),
		fmt.Sprintf("cost=%.0f", cost),
		fmt.Sprintf("confidence=%.2f", confidence),
	}
	if comp != nil {
		inputs = append(inputs,
			fmt.Sprintf("competition_age_hours=%.1f", now.Sub(comp.ObservedAt).Hours()),
			fmt.Sprintf("competition_decay=%.2f", decay))
	} else if in.Competition != nil {
		inputs = append(inputs, "competition=expired_or_stale")
	}
	expected := expectedReward
	// Each evaluation is an append-only record: the history of why a program
	// ranked the way it did (competition, P(valid), confidence) is the point.
	return domain.Opportunity{
		ID:        domain.OpportunityID("opp/" + string(in.Program.ID) + "/" + now.UTC().Format("20060102T150405.000000000")),
		ProgramID: in.Program.ID,
		EstimatedReward: domain.Range{
			Min: in.Program.Reward.Min, Max: in.Program.Reward.Max,
			Median: &domain.Money{Amount: int64(median), Currency: "CNY"},
		},
		AcceptanceProbability: &pValid,
		ResearchCost: domain.CostEstimate{
			HumanHours: e.Costs.HumanHours, ReviewHours: e.Costs.ReviewHours,
			ComputeCost: moneyPtr(e.Costs.Compute), ToolCost: moneyPtr(e.Costs.Tool), RiskCost: moneyPtr(e.Costs.Risk),
		},
		ExpectedReturn:        &expected,
		ExpectedReturnPerCost: &score,
		CompetitionPenalty:    penalty,
		Confidence:            confidence,
		Inputs:                inputs,
		EvaluatedAt:           now,
	}, nil
}

const (
	competitionFreshWindow = 7 * 24 * time.Hour
	competitionMaxAge      = 30 * 24 * time.Hour
)

// competitionAt drops snapshots that have expired or are older than the hard
// maximum age. Competition data changes fast; a six-month-old ranking must not
// influence today's schedule.
func competitionAt(c *domain.CompetitionSnapshot, now time.Time) *domain.CompetitionSnapshot {
	if c == nil {
		return nil
	}
	if !c.ValidUntil.IsZero() && !c.ValidUntil.After(now) {
		return nil
	}
	if !c.ObservedAt.IsZero() && now.Sub(c.ObservedAt) >= competitionMaxAge {
		return nil
	}
	return c
}

// competitionDecay lowers confidence linearly between the fresh window (7
// days) and the hard maximum age (30 days).
func competitionDecay(c *domain.CompetitionSnapshot, now time.Time) float64 {
	if c.ObservedAt.IsZero() {
		return 1
	}
	age := now.Sub(c.ObservedAt)
	if age <= competitionFreshWindow {
		return 1
	}
	frac := float64(age-competitionFreshWindow) / float64(competitionMaxAge-competitionFreshWindow)
	if frac >= 1 {
		return 0
	}
	return 1 - frac
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func minConf(a, b domain.Confidence) domain.Confidence {
	if a < b {
		return a
	}
	return b
}

func moneyPtr(v float64) *domain.Money {
	if v <= 0 {
		return nil
	}
	return &domain.Money{Amount: int64(v), Currency: "CNY"}
}
