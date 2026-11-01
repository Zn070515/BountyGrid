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
	// Visibility is disclosure (who may see the scope), not access (whether
	// this account has been granted it).
	Visibility         ScopeVisibility
	Access             ScopeAccess
	ContentsKnown      bool
	Include            []Asset
	Exclude            []Asset
	Conditional        []Asset
	AssetTiers         []string
	EnvironmentTypes   []EnvironmentType
	ChangeHistoryKnown bool
	// ObservedAt/ValidUntil make the scope snapshot's freshness explicit. A
	// recently observed program status does not make an old scope current; an
	// execution decision requires both. Zero ValidUntil falls back to
	// ObservedAt+ScopeMaxAge.
	ObservedAt time.Time
	ValidUntil time.Time
	Evidence   []EvidenceID
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
	MaxRPS              *float64
	MaxConcurrency      *int
	AllowedTimeWindows  []TimeWindow
	VPNRequired         TriState
	DedicatedIPRequired TriState
	// AllowedEgressIPs, when non-empty, is the exact set of public source IPs
	// the program permits. DedicatedIPRequired only says an egress IP must be
	// observed; this list says which one is acceptable.
	AllowedEgressIPs             []string
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
	ActiveHTTP                CapabilityStatus
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

// ModelEgressPolicy is a program-level ceiling for model data egress. It is a
// policy fact recorded on the Program, not a model-provider capability: the
// Model Router must never send a request beyond this ceiling even when the
// provider supports the data class.
//
// The zero value and UNKNOWN both mean "no remote egress". A missing or
// unobserved platform policy must fail closed rather than fall back to the
// router's permissive default.
type ModelEgressPolicy string

const (
	ModelEgressLocalOnly          ModelEgressPolicy = "LOCAL_ONLY"
	ModelEgressRemoteRedactedOnly ModelEgressPolicy = "REMOTE_REDACTED_ONLY"
	ModelEgressRemoteAllowed      ModelEgressPolicy = "REMOTE_ALLOWED"
	ModelEgressUnknown            ModelEgressPolicy = "UNKNOWN"
)

// Valid accepts the explicit policies and the zero value. A Program synced
// before this field existed has no value; treating it as UNKNOWN keeps old
// records loadable while failing closed.
func (p ModelEgressPolicy) Valid() bool {
	switch p {
	case "", ModelEgressLocalOnly, ModelEgressRemoteRedactedOnly, ModelEgressRemoteAllowed, ModelEgressUnknown:
		return true
	default:
		return false
	}
}

// Effective maps the zero value and UNKNOWN to LOCAL_ONLY. Callers must use
// this value, not the raw field, when deciding whether remote egress is
// permitted.
func (p ModelEgressPolicy) Effective() ModelEgressPolicy {
	if p == "" || p == ModelEgressUnknown {
		return ModelEgressLocalOnly
	}
	return p
}

type ProgramState struct {
	Status     ProgramStatus
	Reason     string
	ObservedAt time.Time
	ValidUntil time.Time
	Confidence Confidence
	Inputs     []FactID
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
	// ModelEgressPolicy bounds where model requests for this program may go.
	// UNKNOWN (and the zero value) means LOCAL_ONLY.
	ModelEgressPolicy ModelEgressPolicy
	Submission        SubmissionPolicy
	Evidence          []EvidenceID
	ObservedAt        time.Time
	// PolicyObservedAt/PolicyValidUntil timestamp the Automation+Constraints
	// snapshot specifically. A list sync that refreshes Program.ObservedAt must
	// not make an older automation policy look fresh.
	PolicyObservedAt time.Time
	PolicyValidUntil time.Time
	NextCheckAt      time.Time
}
