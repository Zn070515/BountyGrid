package domain

import "time"

type CompetitionSnapshot struct {
	ID         string
	Platform   Platform
	Scope      string
	ProgramID  *ProgramID
	ObservedAt time.Time
	// ValidUntil bounds how long competition data may influence scheduling.
	// Competition moves quickly; a snapshot that is too old must not be used.
	ValidUntil            time.Time
	ActiveResearchers     *int
	RewardedResearchers   *int
	Top1Share             *float64
	Top3Share             *float64
	Top10Share            *float64
	NewcomerPool          *bool
	NewcomerRankingExists *bool
	DuplicatePressure     *float64
	Saturation            *float64
	RewardPool            *Money
	Confidence            Confidence
	Evidence              []EvidenceID
}

type Range struct {
	Min    *Money
	Max    *Money
	Median *Money
}

type CostEstimate struct {
	HumanHours  float64
	ComputeCost *Money
	ToolCost    *Money
	ReviewHours float64
	RiskCost    *Money
}

type Opportunity struct {
	ID                    OpportunityID
	ProgramID             ProgramID
	AssetID               *AssetID
	EstimatedReward       Range
	AcceptanceProbability *float64
	ResearchCost          CostEstimate
	ExpectedReturn        *float64
	ExpectedReturnPerCost *float64
	CompetitionPenalty    float64
	Confidence            Confidence
	Inputs                []string
	EvaluatedAt           time.Time
}
