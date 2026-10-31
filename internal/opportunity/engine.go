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

type Input struct {
	Program     domain.Program
	Status      domain.ProgramStatus
	Account     domain.Account
	Competition *domain.CompetitionSnapshot
	Now         time.Time
}

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
	if c := in.Competition; c != nil {
		if c.Top10Share != nil {
			top10 = clamp01(*c.Top10Share)
		}
		if c.DuplicatePressure != nil {
			dup = clamp01(*c.DuplicatePressure)
		}
		if c.Saturation != nil {
			sat = clamp01(*c.Saturation)
		}
		conf = c.Confidence
	}
	penalty := e.Weights.Top10*top10 + e.Weights.Duplicate*dup + e.Weights.Saturation*sat
	penalty = math.Min(penalty, 0.95)

	pValid := e.Weights.PValid
	expectedReward := pValid * weighted * e.Weights.AssetTier * e.Weights.Campaign * (1 - dup)
	humanCost := e.Costs.HumanHours*e.Costs.HourlyRate + e.Costs.ReviewHours*e.Costs.ReviewRate
	cost := humanCost + e.Costs.Compute + e.Costs.Tool + e.Costs.Risk
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
	expected := expectedReward
	return domain.Opportunity{
		ID:        domain.OpportunityID("opp/" + string(in.Program.ID)),
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
