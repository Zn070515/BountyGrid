# Domain Model

- 原则：domain 包只放类型与不变量，不依赖数据库、HTTP、浏览器、模型。
- 平台原生字段保留在 `ObservedFact.Value`；规范化实体只保留 Policy/UI 需要的字段。
- 所有时间用 UTC；金额用最小货币单位（分）。

## 1. 标识与枚举

```go
package domain

type Platform string

const (
    PlatformXianzhi Platform = "xianzhi"
    PlatformVulbox  Platform = "vulbox"
    PlatformButian  Platform = "butian"
)

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

type TransportKind string

const (
    TransportOfficialAPI        TransportKind = "OFFICIAL_API"
    TransportBrowserPage        TransportKind = "BROWSER_PAGE"
    TransportBrowserObservedAPI TransportKind = "BROWSER_OBSERVED_API"
    TransportManualImport       TransportKind = "MANUAL_IMPORT"
)

type SourceType string

const (
    SourceAccount      SourceType = "ACCOUNT"
    SourceProgram      SourceType = "PROGRAM"
    SourcePolicy       SourceType = "POLICY"
    SourcePayout       SourceType = "PAYOUT"
    SourceAnnouncement SourceType = "ANNOUNCEMENT"
    SourceScreenshot   SourceType = "SCREENSHOT"
)

type Confidence float64 // 0..1
```

## 2. Evidence / Fact / State

```go
type Evidence struct {
    ID          EvidenceID
    Platform    Platform
    SourceType  SourceType
    Transport   TransportKind
    URL         string
    PageTitle   string
    CapturedAt  time.Time
    ContentHash string
    RawRef      string   // 文件/对象存储引用；不存敏感值
    Redactions  []string // 已脱敏字段说明
}

type FactSubject string

const (
    SubjectAccount FactSubject = "ACCOUNT"
    SubjectProgram FactSubject = "PROGRAM"
    SubjectAsset   FactSubject = "ASSET"
    SubjectPayout  FactSubject = "PAYOUT"
)

type ObservedFact struct {
    ID               FactID
    Subject          FactSubject
    Platform         Platform
    AccountID        *AccountID
    ProgramID        *ProgramID
    AssetID          *AssetID
    Predicate        string // e.g. "registration_end", "compensation_max"
    Value            any    // typed value; use JSON-safe types
    Unit             string // "CNY", "timestamp", "bool", "enum"
    ObservedAt       time.Time
    EvidenceRefs     []EvidenceID
    Confidence       Confidence
    ExtractorVersion string
}

type StateSubject string

const (
    StateProgramState    StateSubject = "PROGRAM_STATE"
    StateRegistration    StateSubject = "REGISTRATION_STATE"
    StateScopeVisibility StateSubject = "SCOPE_VISIBILITY"
    StateAccountAbility  StateSubject = "ACCOUNT_ABILITY"
    StateCompetition     StateSubject = "COMPETITION"
)

type DerivedState struct {
    ID           StateID
    Subject      StateSubject
    Platform     Platform
    AccountID    *AccountID
    ProgramID    *ProgramID
    State        string
    Inputs       []FactID
    ObservedAt   time.Time
    DerivedAt    time.Time
    ValidUntil   time.Time
    Confidence   Confidence
    RuleVersion  string
}
```

**不变量**

- 没有 `EvidenceRefs` 的 Fact 不允许进入 domain。
- `ValidUntil` 过期的 State 不得用于决策；Policy Engine 必须要求刷新。
- Fact 只陈述“观察到什么”，不写“能不能做”。

## 3. 账号与能力

```go
type GateKind string

const (
    GateIdentityVerified        GateKind = "IDENTITY_VERIFIED"
    GateComplianceExam          GateKind = "COMPLIANCE_EXAM"
    GatePracticalCertification  GateKind = "PRACTICAL_CERTIFICATION"
    GateProgramSignup           GateKind = "PROGRAM_SIGNUP"
    GateApplicationReview       GateKind = "APPLICATION_REVIEW"
    GateInvitation              GateKind = "INVITATION"
    GateNDA                     GateKind = "NDA"
    GateContract                GateKind = "CONTRACT"
    GateReputation              GateKind = "REPUTATION"
    GateHistoryValidVulns       GateKind = "HISTORY_VALID_VULNS"
    GateProfileCompletion       GateKind = "PROFILE_COMPLETION"
    GateBankBinding             GateKind = "BANK_BINDING"
    GateWechatBinding           GateKind = "WECHAT_BINDING"
    GateStudentVerification     GateKind = "STUDENT_VERIFICATION"
    GateTeamMembership          GateKind = "TEAM_MEMBERSHIP"
    GatePayoutSetup             GateKind = "PAYOUT_SETUP"
    GateTestAccountReporting    GateKind = "TEST_ACCOUNT_REPORTING"
    GateVPNAccess               GateKind = "VPN_ACCESS"
)

type GateSatisfaction string

const (
    GateSatisfied   GateSatisfaction = "SATISFIED"
    GateUnsatisfied GateSatisfaction = "UNSATISFIED"
    GateUnknown     GateSatisfaction = "UNKNOWN"
    GateNotRequired GateSatisfaction = "NOT_REQUIRED"
)

type EligibilityGate struct {
    Kind      GateKind
    ProgramID *ProgramID // nil = account-wide; non-nil = only that program
    Required  bool
    Satisfied GateSatisfaction
    Blocking  bool
    Evidence  []EvidenceID
    Notes     string
}

type EligibilityDecision string

const (
    Eligible          EligibilityDecision = "ELIGIBLE"
    Ineligible        EligibilityDecision = "INELIGIBLE"
    HumanActionNeeded EligibilityDecision = "HUMAN_ACTION_REQUIRED"
    EligibilityUnknown EligibilityDecision = "UNKNOWN"
)

type EligibilityState struct {
    Decision      EligibilityDecision
    Gates         []EligibilityGate
    EvaluatedAt   time.Time
    PolicyVersion string
    Notes         string
}

func (s EligibilityState) Blocking() []EligibilityGate
func (s EligibilityState) HasUnknown() bool
```

```go
type AccountCapability struct {
    CanViewPrograms bool
    CanViewScope    bool
    CanJoinPrograms bool
    CanSubmit       bool
    CanPayout       bool
    BlockingReasons []EligibilityGate
    EvaluatedAt     time.Time
}

type Account struct {
    ID                      AccountID
    Platform                Platform
    Registered              bool
    RegistrationTime        *time.Time
    IdentityVerified        *bool
    Level                   string
    LevelExp                *int
    LevelExpNext            *int
    ReputationScore         *float64
    ReputationMetric        string
    Rank                    *string
    NewcomerStatus          string
    ComplianceExamCompleted *bool
    PracticalCertification  *bool
    BankBound               *bool
    WechatBound             *bool
    PendingSubmissionLimit  *int
    JoinedProgramIDs        []ProgramID
    Capability              AccountCapability
    Gates                   []EligibilityGate
    ObservedAt              time.Time
}
```

**不变量**

- `Capability.CanSubmit == false` 时，任何 `REPORT_SUBMIT` 必须 DENY 或 REQUIRE_HUMAN；不能靠 Program 规则绕过。
- `ComplianceExamCompleted`、`PracticalCertification` 是账号级字段，不允许复制到 `Program.Gates`。
- 账号 ID 内部使用；昵称/手机号/邮箱不进入 evidence 明文。

## 4. 项目、Scope、约束、奖励

```go
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
    TypeNewcomerTask        ProgramType = "NEWCOMER_TASK"
    TypeCampaign           ProgramType = "CAMPAIGN"
)

type ProgramStatus string

const (
    StatusRunning  ProgramStatus = "RUNNING"
    StatusPaused   ProgramStatus = "PAUSED"
    StatusUpcoming ProgramStatus = "UPCOMING"
    StatusEnded    ProgramStatus = "ENDED"
    StatusUnknown  ProgramStatus = "UNKNOWN"
)

type ProgramState struct {
    Status           ProgramStatus
    Reason           string
    ObservedAt       time.Time
    ValidUntil       time.Time
    Confidence       Confidence
    Inputs           []FactID
    Evidence         []EvidenceID
}
```

```go
type ScopeVisibility string

const (
    ScopePublic         ScopeVisibility = "PUBLIC"
    ScopeGatedBySignup  ScopeVisibility = "GATED_BY_SIGNUP"
    ScopeGatedByNDA     ScopeVisibility = "GATED_BY_NDA"
    ScopeGatedByApproval ScopeVisibility = "GATED_BY_APPROVAL"
    ScopeUnknown        ScopeVisibility = "UNKNOWN"
)

type ScopeAccess string

const (
    ScopeAccessGranted    ScopeAccess = "GRANTED"
    ScopeAccessNotGranted ScopeAccess = "NOT_GRANTED"
    ScopeAccessUnknown    ScopeAccess = "UNKNOWN"
)

type EnvironmentType string

const (
    EnvWeb            EnvironmentType = "WEB"
    EnvApp            EnvironmentType = "APP"
    EnvPC             EnvironmentType = "PC"
    EnvH5             EnvironmentType = "H5"
    EnvAPI            EnvironmentType = "API"
    EnvIoT            EnvironmentType = "IOT"
    EnvCloud          EnvironmentType = "CLOUD"
    EnvMiniProgram    EnvironmentType = "MINI_PROGRAM"
    EnvGeneralSoftware EnvironmentType = "GENERAL_SOFTWARE"
    EnvIntel          EnvironmentType = "INTEL"
)

type AssetKind string

const (
    AssetDomain     AssetKind = "DOMAIN"
    AssetSubdomain  AssetKind = "SUBDOMAIN"
    AssetIP         AssetKind = "IP"
    AssetCIDR       AssetKind = "CIDR"
    AssetApp        AssetKind = "APP"
    AssetMiniProgram AssetKind = "MINI_PROGRAM"
    AssetAPI        AssetKind = "API"
    AssetCloud      AssetKind = "CLOUD"
    AssetIoT        AssetKind = "IOT"
    AssetRepo       AssetKind = "REPO"
    AssetLLM        AssetKind = "LLM"
    AssetAgent      AssetKind = "AGENT"
)

type Asset struct {
    ID         AssetID
    Platform   Platform
    ProgramID  ProgramID
    Kind       AssetKind
    Value      string
    Tier       string // 平台原生：超级/关键/核心/一般/边缘；核心/普通/边缘；A-E
    Priority   int
    InScope    bool
    Source     EvidenceID
}

type Scope struct {
    // Visibility is disclosure: who may see the scope.
    Visibility          ScopeVisibility
    // Access is this account's access: GRANTED / NOT_GRANTED / UNKNOWN.
    Access              ScopeAccess
    ContentsKnown       bool
    Include             []Asset
    Exclude             []Asset
    Conditional         []Asset
    AssetTiers          []string
    EnvironmentTypes    []EnvironmentType
    ChangeHistoryKnown  bool
    ObservedAt          time.Time
    Evidence            []EvidenceID
}
```

**不变量**

- `Access != GRANTED` 时 `ContentsKnown=false`，`Include/Exclude/Conditional` 必须为空。
- `Access=GRANTED` 且 `ContentsKnown=true` 但 `Include` 为空时，表示该 Scope 已知不包含可执行资产；`CheckAsset` 必须返回 `EXCLUDED`，不能把空列表解释为“全部资产”。
- 通配符 `*.example.com` 只匹配子域，不匹配 apex `example.com`；apex 需要显式列出。
- 报名后可以同时是 `Visibility=GATED_BY_SIGNUP`、`Access=GRANTED`、`ContentsKnown=true`；不能为了表达“已解锁”而把 Disclosure 改成 PUBLIC。
- `Asset.InScope` 只在 `Access=GRANTED` 且 contents known 时参与判断；GATED 下的资产不能进入 Harness。
- Scope 变更必须产生新 `ObservedFact` 与新的 `Scope` 快照，不能原地改。

```go
type TriState string

const (
    TriYes             TriState = "YES"
    TriNo              TriState = "NO"
    TriProgramSpecific TriState = "PROGRAM_SPECIFIC"
    TriUnknown         TriState = "UNKNOWN"
)

type DailyLimit struct {
    Interface string
    Amount    int64
    Unit      string // "requests/day"
}

type Constraints struct {
    MaxRPS                     *float64
    MaxConcurrency             *int
    AllowedTimeWindows         []TimeWindow
    VPNRequired                TriState
    DedicatedIPRequired        TriState
    ResearcherHeaderRequired   TriState
    TestAccountRequired        TriState
    TestAccountReportingRequired *bool
    PerInterfaceDailyLimits    []DailyLimit
    BudgetCap                  *Money
    SameTypePauseThreshold     *int
    ImpactReportingRequired    *bool
    AllowedMethods             []string
    ForbiddenMethods           []string
}

type TimeWindow struct {
    Start time.Time
    End   time.Time
    TZ    string
}
```

```go
type Money struct {
    Amount   int64  // 分
    Currency string // "CNY"
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

type RewardPolicy struct {
    Types             []RewardType
    Min               *Money
    Max               *Money
    AssetMultiplier   bool
    AssetTiers        []string
    SeverityLevels    []string
    DuplicateRule     string
    FirstReportRule   string
    QualityBonus      string
    NewcomerBonus     string
    CampaignBonus     string
    Commission        string
    MinimumPayout     *Money
    SettlementCycle   string
    TaxBasis          TaxBasis
    StagedPayment     bool
}
```

## 5. 自动化策略与提交策略

```go
type CapabilityStatus string

const (
    CapAllowed     CapabilityStatus = "ALLOWED"
    CapConditional CapabilityStatus = "CONDITIONAL"
    CapForbidden   CapabilityStatus = "FORBIDDEN"
    CapUnknown     CapabilityStatus = "UNKNOWN"
)

type AutomationPolicy struct {
    AIAnalysis               CapabilityStatus
    AutonomousAgent          CapabilityStatus
    ActiveHTTP               CapabilityStatus
    BrowserAutomation        CapabilityStatus
    AutomatedScanning        CapabilityStatus
    DirectoryEnumeration     CapabilityStatus
    SubdomainDiscovery       CapabilityStatus
    HighConcurrency          CapabilityStatus
    BruteForce               CapabilityStatus
    CredentialStuffing       CapabilityStatus
    FileUpload               CapabilityStatus
    StateMutation            CapabilityStatus
    RCEVerification          CapabilityStatus
    SensitiveDataRead        CapabilityStatus
    InternalNetwork          CapabilityStatus
    LateralMovement          CapabilityStatus
    DoS                      CapabilityStatus
    HumanVerificationRequired bool
    AIDisclosureFields       []string
    PenaltyThresholds        string
}

type SubmissionPolicy struct {
    DraftSupported       *bool
    RequiredFields       []string
    AttachmentsSupported []string
    ConversationSupported *bool
    StatusTracking       bool
    StatusValues         []string
    EditSupported        *bool
    WithdrawSupported    *bool
    APISubmission        *bool
    Preconditions        []string
    AcceptedCategories   []string
    PausedCategories     []string
}
```

## 6. Program 聚合

```go
type Program struct {
    ID           ProgramID
    Platform     Platform
    NativeID     string
    Name         string
    Type         ProgramType
    Status       ProgramState
    Eligibility  EligibilityState
    Gates        []EligibilityGate // 仅项目级
    Scope        Scope
    Constraints  Constraints
    Reward       RewardPolicy
    Automation   AutomationPolicy
    Submission   SubmissionPolicy
    Evidence     []EvidenceID
    ObservedAt   time.Time
    NextCheckAt  time.Time
}
```

**不变量**

- `Program.Gates` 只能包含项目级门槛；账号级门槛在 `Account.Gates/Capability`。
- `Program.Status.Status != RUNNING` 时，任何执行类 Action 都 DENY；查看类可 ALLOW。
- `Program.Scope.Access != GRANTED` 或 `ContentsKnown=false` 时，不能把 Scope 传给 Harness；Disclosure 可以仍是 GATED。
- `Program.Eligibility.Decision != ELIGIBLE` 时，不能创建 ResearchTask；但可查看规则/发起人工操作。

## 7. ActionRequest / ActionDecision

```go
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

type ActorKind string

const (
    ActorHuman     ActorKind = "HUMAN"
    ActorHarness   ActorKind = "HARNESS"
    ActorModel     ActorKind = "MODEL"
    ActorScheduler ActorKind = "SCHEDULER"
)

type ActorRef struct {
    Kind ActorKind
    ID   string
}

type ActionRequest struct {
    ID             string
    Actor          ActorRef
    AccountID      AccountID
    ProgramID      ProgramID
    AssetID        *AssetID
    Action         Action
    Parameters     json.RawMessage
    Purpose        string
    RequestedAt    time.Time
    Evidence       []EvidenceID
    IdempotencyKey string
}

type Decision string

const (
    DecisionAllow              Decision = "ALLOW"
    DecisionDeny               Decision = "DENY"
    DecisionRequireHuman       Decision = "REQUIRE_HUMAN"
    DecisionRequireMoreEvidence Decision = "REQUIRE_MORE_EVIDENCE"
)

type HumanApproval struct {
    ID             string
    ApprovedBy     string
    ApprovedAt     time.Time
    RequestID      string
    Action         Action
    ProgramID      ProgramID
    AssetID        *AssetID
    ResourceID     string // report/finding/task for resource-scoped actions
    ParametersHash string
    Scope          string
    ExpiresAt      time.Time
    SingleUse      bool
    Comment        string
}

type ActionDecision struct {
    ID             DecisionID
    RequestID      string
    Decision       Decision
    Reasons        []string
    GatesEvaluated []EligibilityGate
    PolicyVersion  string
    DecidedAt      time.Time
    HumanApproval  *HumanApproval
    ExpiresAt      *time.Time
    Constraints    *Constraints // 条件型 ALLOW 的执行约束
}
```

## 8. 竞争、机会、研究、收益

```go
type CompetitionSnapshot struct {
    ID                   string
    Platform             Platform
    Scope                string // "PLATFORM" | "PROGRAM"
    ProgramID            *ProgramID
    ObservedAt           time.Time
    ActiveResearchers    *int
    RewardedResearchers  *int
    Top1Share            *float64
    Top3Share            *float64
    Top10Share           *float64
    NewcomerPool         *bool
    NewcomerRankingExists *bool
    DuplicatePressure    *float64
    Saturation           *float64
    RewardPool           *Money
    Confidence           Confidence
    Evidence             []EvidenceID
}

type Range struct {
    Min    *Money
    Max    *Money
    Median *Money
}

type CostEstimate struct {
    HumanHours   float64
    ComputeCost  *Money
    ToolCost     *Money
    ReviewHours  float64
    RiskCost     *Money
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
    Inputs                []string // fact/state/snapshot IDs
    EvaluatedAt           time.Time
}

type ResearchTask struct {
    ID           TaskID
    OpportunityID OpportunityID
    ProgramID    ProgramID
    AssetID      *AssetID
    ScopeSnapshot Scope
    Constraints  Constraints
    Status       string
    Budget       *Money
    CreatedAt    time.Time
    ExpiresAt    *time.Time
}

type Finding struct {
    ID         FindingID
    TaskID     TaskID
    ProgramID  ProgramID
    AssetID    *AssetID
    Title      string
    VulnType   string
    Severity   string
    Evidence   []EvidenceID
    Status     string
    CreatedAt  time.Time
}

type VerificationVerdict string

const (
    VerdictVerified       VerificationVerdict = "VERIFIED"
    VerdictNeedsEvidence  VerificationVerdict = "NEEDS_MORE_EVIDENCE"
    VerdictOutOfScope     VerificationVerdict = "OUT_OF_SCOPE"
    VerdictDuplicateRisk  VerificationVerdict = "DUPLICATE_RISK"
    VerdictRejected       VerificationVerdict = "REJECTED"
)

type Verification struct {
    ID             string
    FindingID      FindingID
    Reproduced     bool
    InScope        bool
    ImpactVerified bool
    DataMinimized  bool
    DuplicateRisk  float64
    Verdict        VerificationVerdict
    VerifiedBy     string
    Evidence       []EvidenceID
    VerifiedAt     time.Time
}

type ReportDraft struct {
    ID           string
    FindingID    FindingID
    ProgramID    ProgramID
    Fields       map[string]string
    AIDisclosure map[string]string // 大模型名称/版本、工具名称、人工验证说明
    Attachments  []string
    Status       string
    CreatedAt    time.Time
}

type Submission struct {
    ID           SubmissionID
    ReportID     string
    ProgramID    ProgramID
    SubmittedAt  time.Time
    PlatformURL  string
    Receipt      string
    Status       string
    Evidence     []EvidenceID
}

type LedgerEntry struct {
    ID           string
    Platform     Platform
    ProgramID    ProgramID
    FindingID    *FindingID
    SubmissionID *SubmissionID
    Kind         string // EXPECTED | SUBMITTED | ACCEPTED | PAID | FEE | TAX | REFUND
    Amount       Money
    Status       string
    OccurredAt   time.Time
    Evidence     []EvidenceID
}
```

## 9. 聚合关系

```text
PlatformAccount 1─* ProgramMembership *─1 Program
Program 1─1 Scope
Program 1─1 RewardPolicy
Program 1─1 Constraints
Program 1─1 AutomationPolicy
Program 1─1 SubmissionPolicy
Program 1─* EligibilityGate（项目级）
PlatformAccount 1─* EligibilityGate（账号级）
AccountCapability 由账号级 Gate 派生
CompetitionSnapshot *─1 Program（或 Platform）
Opportunity *─1 Program
ResearchTask *─1 Opportunity
Finding *─1 ResearchTask
Verification 1─1 Finding
ReportDraft *─1 Finding
Submission 1─1 ReportDraft
LedgerEntry *─1 Submission
```

## 10. 不变量清单（用于测试）

1. 没有 EvidenceRef 的 Fact 不能落库。
2. `Scope.Access != GRANTED` 时 `ContentsKnown == false` 且 Include/Exclude 为空；`Visibility=GATED_* + Access=GRANTED` 是合法状态。
3. `Account.Capability.CanSubmit == false` 时 `REPORT_SUBMIT` 不得 ALLOW；`PRACTICAL_CERTIFICATION` 只按 Program 作用域生效，Program-scoped gate 不得使账号级 `CanSubmit` 失效；`BANK_BINDING`/`PAYOUT_SETUP` 只影响 `CanPayout`，不得使 `CanSubmit` 失效。
4. `Program.Status != RUNNING` 或状态置信度 `< 0.8` 时执行类 Action 不得 ALLOW；查看类不要求。
5. `Eligibility.Decision != ELIGIBLE` 时不能创建 ResearchTask；查看规则/项目不受 Eligibility 影响。
6. 任何 `REPORT_SUBMIT/PAYOUT/PROGRAM_SIGNUP/NDA_ACCEPT` 没有 HumanApproval 时不得 ALLOW；human-only 审批必须 single-use。
7. `ACTIVE_HTTP` 必须有 Asset，`Asset.ID` 必须等于 `ActionRequest.AssetID`，且 `Scope.CheckAsset` 必须为 INCLUDED；CONDITIONAL 需人工，EXCLUDED/UNKNOWN 不得 ALLOW。
8. `AutomationPolicy=FORBIDDEN` 时对应 Action 不得 ALLOW，人工审批也不能覆盖。
9. `DecisionContext` 的 `Account.ID/Program.ID/Platform` 必须与 `ActionRequest` 一致，否则 DENY。
10. `ActionDecision.ExpiresAt` 过期后，Harness 必须重新申请；`ALLOW` 决策必须携带 `ExpiresAt`。
11. `LedgerEntry` 金额不得为负（退款/费用单独 Kind）。
12. `Evidence` 不得包含密码/Cookie/身份证/银行卡/手机号明文。
13. `CompetitionSnapshot` 缺失时 Opportunity 的 Confidence 必须降低，不能默认高。
