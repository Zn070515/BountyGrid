package domain

// Platform identifies a bug bounty platform.
type Platform string

const (
	PlatformXianzhi Platform = "xianzhi"
	PlatformVulbox  Platform = "vulbox"
	PlatformButian  Platform = "butian"
)

func (p Platform) Valid() bool {
	switch p {
	case PlatformXianzhi, PlatformVulbox, PlatformButian:
		return true
	default:
		return false
	}
}

type (
	AccountID     string
	ProgramID     string
	AssetID       string
	EvidenceID    string
	FactID        string
	StateID       string
	OpportunityID string
	TaskID        string
	FindingID     string
	SubmissionID  string
	DecisionID    string
)

// TransportKind describes where a fact came from.
type TransportKind string

const (
	TransportOfficialAPI        TransportKind = "OFFICIAL_API"
	TransportBrowserPage        TransportKind = "BROWSER_PAGE"
	TransportBrowserObservedAPI TransportKind = "BROWSER_OBSERVED_API"
	TransportManualImport       TransportKind = "MANUAL_IMPORT"
)

func (t TransportKind) Valid() bool {
	switch t {
	case TransportOfficialAPI, TransportBrowserPage, TransportBrowserObservedAPI, TransportManualImport:
		return true
	default:
		return false
	}
}

type SourceType string

const (
	SourceAccount      SourceType = "ACCOUNT"
	SourceProgram      SourceType = "PROGRAM"
	SourcePolicy       SourceType = "POLICY"
	SourcePayout       SourceType = "PAYOUT"
	SourceAnnouncement SourceType = "ANNOUNCEMENT"
	SourceScreenshot   SourceType = "SCREENSHOT"
)

func (s SourceType) Valid() bool {
	switch s {
	case SourceAccount, SourceProgram, SourcePolicy, SourcePayout, SourceAnnouncement, SourceScreenshot:
		return true
	default:
		return false
	}
}

// Confidence is 0..1.
type Confidence float64

func (c Confidence) Valid() bool { return c >= 0 && c <= 1 }

// GateKind is an eligibility gate. Account-level and program-level gates share this type.
type GateKind string

const (
	GateIdentityVerified       GateKind = "IDENTITY_VERIFIED"
	GateComplianceExam         GateKind = "COMPLIANCE_EXAM"
	GatePracticalCertification GateKind = "PRACTICAL_CERTIFICATION"
	GateProgramSignup          GateKind = "PROGRAM_SIGNUP"
	GateApplicationReview      GateKind = "APPLICATION_REVIEW"
	GateInvitation             GateKind = "INVITATION"
	GateNDA                    GateKind = "NDA"
	GateContract               GateKind = "CONTRACT"
	GateReputation             GateKind = "REPUTATION"
	GateHistoryValidVulns      GateKind = "HISTORY_VALID_VULNS"
	GateProfileCompletion      GateKind = "PROFILE_COMPLETION"
	GateBankBinding            GateKind = "BANK_BINDING"
	GateWechatBinding          GateKind = "WECHAT_BINDING"
	GateStudentVerification    GateKind = "STUDENT_VERIFICATION"
	GateTeamMembership         GateKind = "TEAM_MEMBERSHIP"
	GatePayoutSetup            GateKind = "PAYOUT_SETUP"
	GateTestAccountReporting   GateKind = "TEST_ACCOUNT_REPORTING"
	GateVPNAccess              GateKind = "VPN_ACCESS"
)

func (g GateKind) Valid() bool {
	switch g {
	case GateIdentityVerified, GateComplianceExam, GatePracticalCertification,
		GateProgramSignup, GateApplicationReview, GateInvitation, GateNDA,
		GateContract, GateReputation, GateHistoryValidVulns, GateProfileCompletion,
		GateBankBinding, GateWechatBinding, GateStudentVerification,
		GateTeamMembership, GatePayoutSetup, GateTestAccountReporting, GateVPNAccess:
		return true
	default:
		return false
	}
}

// BlocksSubmit reports whether an unsatisfied account-wide gate makes
// submission impossible. Bank/payout gates must not be included here.
func (g GateKind) BlocksSubmit() bool {
	switch g {
	case GateIdentityVerified, GateComplianceExam:
		return true
	default:
		return false
	}
}

// BlocksPayout reports whether an unsatisfied account-wide gate makes payout
// impossible.
func (g GateKind) BlocksPayout() bool {
	switch g {
	case GateBankBinding, GatePayoutSetup:
		return true
	default:
		return false
	}
}

// HumanResolvable reports whether a gate can only be satisfied by a human action.
func (g GateKind) HumanResolvable() bool {
	switch g {
	case GateProgramSignup, GateApplicationReview, GateComplianceExam,
		GatePracticalCertification, GateBankBinding, GateWechatBinding,
		GateStudentVerification, GateTeamMembership, GatePayoutSetup, GateNDA:
		return true
	default:
		return false
	}
}

type GateSatisfaction string

const (
	GateSatisfied   GateSatisfaction = "SATISFIED"
	GateUnsatisfied GateSatisfaction = "UNSATISFIED"
	GateUnknown     GateSatisfaction = "UNKNOWN"
	GateNotRequired GateSatisfaction = "NOT_REQUIRED"
)

func (s GateSatisfaction) Valid() bool {
	switch s {
	case GateSatisfied, GateUnsatisfied, GateUnknown, GateNotRequired:
		return true
	default:
		return false
	}
}

type EligibilityDecision string

const (
	Eligible           EligibilityDecision = "ELIGIBLE"
	Ineligible         EligibilityDecision = "INELIGIBLE"
	HumanActionNeeded  EligibilityDecision = "HUMAN_ACTION_REQUIRED"
	EligibilityUnknown EligibilityDecision = "UNKNOWN"
)

func (d EligibilityDecision) Valid() bool {
	switch d {
	case Eligible, Ineligible, HumanActionNeeded, EligibilityUnknown:
		return true
	default:
		return false
	}
}

type ProgramType string

const (
	TypeBrandSRC           ProgramType = "BRAND_SRC"
	TypeEnterpriseSRC      ProgramType = "ENTERPRISE_SRC"
	TypePublicWelfareSRC   ProgramType = "PUBLIC_WELFARE_SRC"
	TypeUnregisteredVendor ProgramType = "UNREGISTERED_VENDOR"
	TypePremiumProject     ProgramType = "PREMIUM_PROJECT"
	TypeCrowdTest          ProgramType = "CROWD_TEST"
	TypeFlashTest          ProgramType = "FLASH_TEST"
	TypeGeneralVulnProgram ProgramType = "GENERAL_VULN_PROGRAM"
	TypeIntelProgram       ProgramType = "INTEL_PROGRAM"
	TypeCampusProgram      ProgramType = "CAMPUS_PROGRAM"
	TypeOfflineJob         ProgramType = "OFFLINE_JOB"
	TypePrivateExclusive   ProgramType = "PRIVATE_EXCLUSIVE"
	TypeNewcomerTask       ProgramType = "NEWCOMER_TASK"
	TypeCampaign           ProgramType = "CAMPAIGN"
)

func (t ProgramType) Valid() bool {
	switch t {
	case TypeBrandSRC, TypeEnterpriseSRC, TypePublicWelfareSRC, TypeUnregisteredVendor,
		TypePremiumProject, TypeCrowdTest, TypeFlashTest, TypeGeneralVulnProgram,
		TypeIntelProgram, TypeCampusProgram, TypeOfflineJob, TypePrivateExclusive,
		TypeNewcomerTask, TypeCampaign:
		return true
	default:
		return false
	}
}

type ProgramStatus string

const (
	StatusRunning  ProgramStatus = "RUNNING"
	StatusPaused   ProgramStatus = "PAUSED"
	StatusUpcoming ProgramStatus = "UPCOMING"
	StatusEnded    ProgramStatus = "ENDED"
	StatusUnknown  ProgramStatus = "UNKNOWN"
)

func (s ProgramStatus) Valid() bool {
	switch s {
	case StatusRunning, StatusPaused, StatusUpcoming, StatusEnded, StatusUnknown:
		return true
	default:
		return false
	}
}

// ScopeVisibility describes disclosure, not whether this account has access.
// After signup/NDA, Disclosure can remain GATED_* while Access is GRANTED.
type ScopeVisibility string

const (
	ScopePublic          ScopeVisibility = "PUBLIC"
	ScopeGatedBySignup   ScopeVisibility = "GATED_BY_SIGNUP"
	ScopeGatedByNDA      ScopeVisibility = "GATED_BY_NDA"
	ScopeGatedByApproval ScopeVisibility = "GATED_BY_APPROVAL"
	ScopeUnknown         ScopeVisibility = "UNKNOWN"
)

func (v ScopeVisibility) Valid() bool {
	switch v {
	case ScopePublic, ScopeGatedBySignup, ScopeGatedByNDA, ScopeGatedByApproval, ScopeUnknown:
		return true
	default:
		return false
	}
}

// ScopeAccess is the current account's access to the scope contents.
type ScopeAccess string

const (
	ScopeAccessGranted    ScopeAccess = "GRANTED"
	ScopeAccessNotGranted ScopeAccess = "NOT_GRANTED"
	ScopeAccessUnknown    ScopeAccess = "UNKNOWN"
)

func (a ScopeAccess) Valid() bool {
	switch a {
	case ScopeAccessGranted, ScopeAccessNotGranted, ScopeAccessUnknown:
		return true
	default:
		return false
	}
}

// ScopeMatch is the result of checking one asset against a scope.
type ScopeMatch string

const (
	ScopeMatchIncluded    ScopeMatch = "INCLUDED"
	ScopeMatchConditional ScopeMatch = "CONDITIONAL"
	ScopeMatchExcluded    ScopeMatch = "EXCLUDED"
	ScopeMatchUnknown     ScopeMatch = "UNKNOWN"
)

type EnvironmentType string

const (
	EnvWeb             EnvironmentType = "WEB"
	EnvApp             EnvironmentType = "APP"
	EnvPC              EnvironmentType = "PC"
	EnvH5              EnvironmentType = "H5"
	EnvAPI             EnvironmentType = "API"
	EnvIoT             EnvironmentType = "IOT"
	EnvCloud           EnvironmentType = "CLOUD"
	EnvMiniProgram     EnvironmentType = "MINI_PROGRAM"
	EnvGeneralSoftware EnvironmentType = "GENERAL_SOFTWARE"
	EnvIntel           EnvironmentType = "INTEL"
)

type AssetKind string

const (
	AssetDomain      AssetKind = "DOMAIN"
	AssetSubdomain   AssetKind = "SUBDOMAIN"
	AssetIP          AssetKind = "IP"
	AssetCIDR        AssetKind = "CIDR"
	AssetApp         AssetKind = "APP"
	AssetMiniProgram AssetKind = "MINI_PROGRAM"
	AssetAPI         AssetKind = "API"
	AssetCloud       AssetKind = "CLOUD"
	AssetIoT         AssetKind = "IOT"
	AssetRepo        AssetKind = "REPO"
	AssetLLM         AssetKind = "LLM"
	AssetAgent       AssetKind = "AGENT"
)

type TriState string

const (
	TriYes             TriState = "YES"
	TriNo              TriState = "NO"
	TriProgramSpecific TriState = "PROGRAM_SPECIFIC"
	TriUnknown         TriState = "UNKNOWN"
)

func (t TriState) Valid() bool {
	switch t {
	case TriYes, TriNo, TriProgramSpecific, TriUnknown:
		return true
	default:
		return false
	}
}

type RewardType string

const (
	RewardCash     RewardType = "CASH"
	RewardPoints   RewardType = "POINTS"
	RewardKB       RewardType = "KB"
	RewardRYB      RewardType = "RYB"
	RewardCoins    RewardType = "COINS"
	RewardHonor    RewardType = "HONOR"
	RewardPhysical RewardType = "PHYSICAL"
	RewardCoupon   RewardType = "COUPON"
	RewardToken    RewardType = "TOKEN"
)

type TaxBasis string

const (
	TaxPreTax  TaxBasis = "PRE_TAX"
	TaxPostTax TaxBasis = "POST_TAX"
	TaxUnknown TaxBasis = "UNKNOWN"
)

func (t TaxBasis) Valid() bool {
	switch t {
	case TaxPreTax, TaxPostTax, TaxUnknown:
		return true
	default:
		return false
	}
}

type CapabilityStatus string

const (
	CapAllowed     CapabilityStatus = "ALLOWED"
	CapConditional CapabilityStatus = "CONDITIONAL"
	CapForbidden   CapabilityStatus = "FORBIDDEN"
	CapUnknown     CapabilityStatus = "UNKNOWN"
)

func (c CapabilityStatus) Valid() bool {
	switch c {
	case CapAllowed, CapConditional, CapForbidden, CapUnknown:
		return true
	default:
		return false
	}
}

type Action string

const (
	ActionViewProgram        Action = "VIEW_PROGRAM"
	ActionViewRules          Action = "VIEW_RULES"
	ActionReadScope          Action = "READ_SCOPE"
	ActionPassiveAnalysis    Action = "PASSIVE_ANALYSIS"
	ActionActiveHTTP         Action = "ACTIVE_HTTP"
	ActionDirectoryEnum      Action = "DIRECTORY_ENUMERATION"
	ActionSubdomainDiscovery Action = "SUBDOMAIN_DISCOVERY"
	ActionAutomatedScan      Action = "AUTOMATED_SCAN"
	ActionHighConcurrency    Action = "HIGH_CONCURRENCY"
	ActionBruteForce         Action = "BRUTE_FORCE"
	ActionCredentialStuffing Action = "CREDENTIAL_STUFFING"
	ActionFileUpload         Action = "FILE_UPLOAD"
	ActionStateMutation      Action = "STATE_MUTATION"
	ActionRCEVerify          Action = "RCE_VERIFY"
	ActionSensitiveDataRead  Action = "SENSITIVE_DATA_READ"
	ActionInternalNetwork    Action = "INTERNAL_NETWORK"
	ActionLateralMovement    Action = "LATERAL_MOVEMENT"
	ActionDoS                Action = "DOS"
	ActionReportDraft        Action = "REPORT_DRAFT"
	ActionReportSubmit       Action = "REPORT_SUBMIT"
	ActionProgramSignup      Action = "PROGRAM_SIGNUP"
	ActionNDAAccept          Action = "NDA_ACCEPT"
	ActionComplianceExam     Action = "COMPLIANCE_EXAM"
	ActionPracticalCert      Action = "PRACTICAL_CERTIFICATION"
	ActionPayout             Action = "PAYOUT"
	ActionBankBinding        Action = "BANK_BINDING"
)

func (a Action) Valid() bool {
	switch a {
	case ActionViewProgram, ActionViewRules, ActionReadScope, ActionPassiveAnalysis,
		ActionActiveHTTP, ActionDirectoryEnum, ActionSubdomainDiscovery, ActionAutomatedScan,
		ActionHighConcurrency, ActionBruteForce, ActionCredentialStuffing, ActionFileUpload,
		ActionStateMutation, ActionRCEVerify, ActionSensitiveDataRead, ActionInternalNetwork,
		ActionLateralMovement, ActionDoS, ActionReportDraft, ActionReportSubmit,
		ActionProgramSignup, ActionNDAAccept, ActionComplianceExam, ActionPracticalCert,
		ActionPayout, ActionBankBinding:
		return true
	default:
		return false
	}
}

// HumanOnly reports actions that can never be approved for autonomous execution.
func (a Action) HumanOnly() bool {
	switch a {
	case ActionReportSubmit, ActionProgramSignup, ActionNDAAccept,
		ActionComplianceExam, ActionPracticalCert, ActionPayout, ActionBankBinding:
		return true
	default:
		return false
	}
}

// NeedsScope reports actions that require an unlocked scope.
// NeedsScope reports actions whose execution requires granted scope contents.
// READ_SCOPE is deliberately excluded: reading a gated scope is how a human
// unlocks it, so it is handled separately.
func (a Action) NeedsScope() bool {
	switch a {
	case ActionPassiveAnalysis, ActionActiveHTTP, ActionDirectoryEnum,
		ActionSubdomainDiscovery, ActionAutomatedScan, ActionHighConcurrency,
		ActionBruteForce, ActionCredentialStuffing, ActionFileUpload,
		ActionStateMutation, ActionRCEVerify, ActionSensitiveDataRead,
		ActionInternalNetwork, ActionLateralMovement, ActionDoS:
		return true
	default:
		return false
	}
}

// NeedsAsset reports actions that must name a specific in-scope asset.
func (a Action) NeedsAsset() bool {
	switch a {
	case ActionActiveHTTP, ActionDirectoryEnum, ActionSubdomainDiscovery,
		ActionAutomatedScan, ActionHighConcurrency, ActionBruteForce,
		ActionCredentialStuffing, ActionFileUpload, ActionStateMutation,
		ActionRCEVerify, ActionSensitiveDataRead, ActionInternalNetwork,
		ActionLateralMovement, ActionDoS:
		return true
	default:
		return false
	}
}

// EligibilityRequired reports actions that require program participation.
// Viewing rules/program and reading a gated scope are not participation.
func (a Action) EligibilityRequired() bool {
	switch a {
	case ActionPassiveAnalysis, ActionActiveHTTP, ActionDirectoryEnum,
		ActionSubdomainDiscovery, ActionAutomatedScan, ActionHighConcurrency,
		ActionBruteForce, ActionCredentialStuffing, ActionFileUpload,
		ActionStateMutation, ActionRCEVerify, ActionSensitiveDataRead,
		ActionInternalNetwork, ActionLateralMovement, ActionDoS,
		ActionReportSubmit:
		return true
	default:
		return false
	}
}

type ActorKind string

const (
	ActorHuman     ActorKind = "HUMAN"
	ActorHarness   ActorKind = "HARNESS"
	ActorModel     ActorKind = "MODEL"
	ActorScheduler ActorKind = "SCHEDULER"
)

func (k ActorKind) Valid() bool {
	switch k {
	case ActorHuman, ActorHarness, ActorModel, ActorScheduler:
		return true
	default:
		return false
	}
}

type Decision string

const (
	DecisionAllow               Decision = "ALLOW"
	DecisionDeny                Decision = "DENY"
	DecisionRequireHuman        Decision = "REQUIRE_HUMAN"
	DecisionRequireMoreEvidence Decision = "REQUIRE_MORE_EVIDENCE"
)

func (d Decision) Valid() bool {
	switch d {
	case DecisionAllow, DecisionDeny, DecisionRequireHuman, DecisionRequireMoreEvidence:
		return true
	default:
		return false
	}
}
