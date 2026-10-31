package domain

import "time"

type Asset struct {
	ID        AssetID
	Platform  Platform
	ProgramID ProgramID
	Kind      AssetKind
	Value     string
	Tier      string
	Priority  int
	InScope   bool
	Source    EvidenceID
}

type Scope struct {
	Visibility         ScopeVisibility
	ContentsKnown      bool
	Include            []Asset
	Exclude            []Asset
	Conditional        []Asset
	AssetTiers         []string
	EnvironmentTypes   []EnvironmentType
	ChangeHistoryKnown bool
	ObservedAt         time.Time
	Evidence           []EvidenceID
}

type Money struct {
	Amount   int64
	Currency string
}

type TimeWindow struct {
	Start time.Time
	End   time.Time
	TZ    string
}

type DailyLimit struct {
	Interface string
	Amount    int64
	Unit      string
}

type Constraints struct {
	MaxRPS                       *float64
	MaxConcurrency               *int
	AllowedTimeWindows           []TimeWindow
	VPNRequired                  TriState
	DedicatedIPRequired          TriState
	ResearcherHeaderRequired     TriState
	TestAccountRequired          TriState
	TestAccountReportingRequired *bool
	PerInterfaceDailyLimits      []DailyLimit
	BudgetCap                    *Money
	SameTypePauseThreshold       *int
	ImpactReportingRequired      *bool
	AllowedMethods               []string
	ForbiddenMethods             []string
}

type RewardPolicy struct {
	Types           []RewardType
	Min             *Money
	Max             *Money
	AssetMultiplier bool
	AssetTiers      []string
	SeverityLevels  []string
	DuplicateRule   string
	FirstReportRule string
	QualityBonus    string
	NewcomerBonus   string
	CampaignBonus   string
	Commission      string
	MinimumPayout   *Money
	SettlementCycle string
	TaxBasis        TaxBasis
	StagedPayment   bool
}

type AutomationPolicy struct {
	AIAnalysis                CapabilityStatus
	AutonomousAgent           CapabilityStatus
	BrowserAutomation         CapabilityStatus
	AutomatedScanning         CapabilityStatus
	DirectoryEnumeration      CapabilityStatus
	SubdomainDiscovery        CapabilityStatus
	HighConcurrency           CapabilityStatus
	BruteForce                CapabilityStatus
	CredentialStuffing        CapabilityStatus
	FileUpload                CapabilityStatus
	StateMutation             CapabilityStatus
	RCEVerification           CapabilityStatus
	SensitiveDataRead         CapabilityStatus
	InternalNetwork           CapabilityStatus
	LateralMovement           CapabilityStatus
	DoS                       CapabilityStatus
	HumanVerificationRequired bool
	AIDisclosureFields        []string
	PenaltyThresholds         string
}

type SubmissionPolicy struct {
	DraftSupported        *bool
	RequiredFields        []string
	AttachmentsSupported  []string
	ConversationSupported *bool
	StatusTracking        bool
	StatusValues          []string
	EditSupported         *bool
	WithdrawSupported     *bool
	APISubmission         *bool
	Preconditions         []string
	AcceptedCategories    []string
	PausedCategories      []string
}

type ProgramState struct {
	Status     ProgramStatus
	Reason     string
	ObservedAt time.Time
	ValidUntil time.Time
	Confidence Confidence
	Evidence   []EvidenceID
}

type Program struct {
	ID          ProgramID
	Platform    Platform
	NativeID    string
	Name        string
	Type        ProgramType
	Status      ProgramState
	Eligibility EligibilityState
	Gates       []EligibilityGate
	Scope       Scope
	Constraints Constraints
	Reward      RewardPolicy
	Automation  AutomationPolicy
	Submission  SubmissionPolicy
	Evidence    []EvidenceID
	ObservedAt  time.Time
	NextCheckAt time.Time
}
