# Go Adapter 设计建议

- 观察时间：2026-10-31
- 目标：Go Control Plane 同时管理先知、漏洞盒子、补天；执行层（Harness/Agent/模型）后接。
- 硬原则：**LEVEL 4 封顶，报告提交必须人工批准；合规考核、NDA、报名、实名/收款一律人工。**

## 1. 三平台 Adapter 结论

### 先知 / ASRC

```json
{
  "platform": "xianzhi",
  "preferred_data_source": ["AUTHENTICATED_WEB", "BROWSER_AUTOMATION", "MANUAL_IMPORT", "PUBLIC_WEB"],
  "authentication_mode": "阿里云账号扫码/OAuth，浏览器会话 Cookie（可持久化 state）",
  "browser_required": true,
  "api_available": false,
  "api_note": "观察到前端内部 openapi/cst/2023-12-08/DescribeWebsiteTaskList.json 等，但无公开文档/稳定性承诺",
  "manual_fields_required": ["任务报名", "资产范围（报名后）", "收款信息", "报告提交"],
  "sync_frequency": "任务列表 1-6h；公告 1d；账号/财务 1d",
  "high_risk_operations": ["任务报名", "提交漏洞/情报", "修改支付信息"],
  "human_only_operations": ["报名", "提交", "提现", "修改收款/实名"],
  "automation_level": "LEVEL_2"
}
```

- 可自动：读取 91 个任务、状态、奖励范围、公告、排行榜、账号概览。
- 不可自动：报名前读资产范围（NotSignUp）、未报名提交、支付信息。
- 关键：`任务状态`（进行中/任务结束/任务暂停）+ `报名时间` 必须实时判断；已结束任务仍显示“我要报名”。

### 漏洞盒子

```json
{
  "platform": "vulbox",
  "preferred_data_source": ["AUTHENTICATED_WEB", "BROWSER_AUTOMATION", "MANUAL_IMPORT", "PUBLIC_WEB"],
  "authentication_mode": "微信扫码/手机验证码/账号密码，浏览器 Cookie",
  "browser_required": true,
  "api_available": false,
  "api_note": "观察到 vapi.vulbox.com/web/* 与 vbapi.vulbox.com/v1/user/identity_info，但无公开文档",
  "manual_fields_required": ["加入项目", "高级项目申请", "NDA签署", "银行卡绑定", "报告提交"],
  "sync_frequency": "项目状态 15-60min（会发生暂停/重开）；公告 6h；账号/财务 1d",
  "high_risk_operations": ["加入项目", "提交漏洞", "提现", "绑定银行卡", "签署NDA"],
  "human_only_operations": ["加入/申请项目", "签署NDA", "提交报告", "提现", "绑定收款"],
  "automation_level": "LEVEL_2"
}
```

- 可自动：约1513个项目（估计值）、51 个企业SRC、2 个已加入项目、项目状态、奖励范围、排行榜、FAQ。
- 不可自动：高级项目申请/审核、NDA、专属项目、提现、银行卡。
- 关键：项目状态会数小时内反复暂停/重开（联想SRC），缓存必须短；提交后不可编辑，只能评论请管理员补充。

### 补天

```json
{
  "platform": "butian",
  "preferred_data_source": ["AUTHENTICATED_WEB", "BROWSER_AUTOMATION", "MANUAL_IMPORT", "PUBLIC_WEB"],
  "authentication_mode": "奇安信统一账号（手机号+密码/短信），浏览器 Cookie",
  "browser_required": true,
  "api_available": false,
  "api_note": "观察到 /WhiteHat/Center/baseData、/Company/Info/assetRange 等 AJAX，但无公开文档",
  "manual_fields_required": ["合规考核", "实战认证", "专属SRC报名", "银行卡绑定", "漏洞提交"],
  "sync_frequency": "账号状态 1d；活动 1-6h；专属SRC项目 1d；公告 1d",
  "high_risk_operations": ["提交漏洞", "报名专属SRC", "领取新人任务", "绑定银行卡"],
  "human_only_operations": ["合规考核", "实战认证", "报名", "提交", "绑定收款"],
  "automation_level": "LEVEL_2"
}
```

- 可自动：账号概览、37 个专属SRC、公益SRC厂商列表、活动中心、GROW、排行榜、公告、项目规则。
- 不可自动：合规考核、实战认证、专属SRC报名、提交、收款。
- 关键：**合规考核未完成 = 所有提交硬阻断**；专属SRC还需实战认证（1个有效漏洞）；新用户待审核限5个。

## 2. 数据源优先级（统一）

| 优先级 | 数据源 | 适用 | 风险 |
|--------|--------|------|------|
| 1 | AUTHENTICATED_WEB（会话内读取页面/前端接口） | 账号状态、已加入项目、提交记录 | 会话过期、页面改版、内部API无承诺 |
| 2 | BROWSER_AUTOMATION（登录、状态轮询、只读抓取） | 登录、项目状态、公告 | 验证码/风控、NDA前必须停止 |
| 3 | MANUAL_IMPORT（人工操作后导入） | 报名、NDA、合规考核、实战认证、提交、提现 | 人工成本 |
| 4 | PUBLIC_WEB（无需登录） | 项目大厅、公告、FAQ、排行榜 | 看不到账号态/资产范围 |

**不建议**把三平台的前端内部 API 当作官方 API 直接集成；它们没有文档、稳定性和授权承诺。若要用，必须：只读、限速、可随时回退浏览器、遵守平台条款、不绕过认证。

## 3. Go 接口建议

```go
type PlatformAdapter interface {
    // 登录必须由人完成；Adapter 只负责检测会话/恢复 state
    AuthStatus(ctx context.Context) (AuthState, error)

    SyncAccount(ctx context.Context) (Account, error)
    SyncPrograms(ctx context.Context) ([]Program, error)
    SyncProgramStatus(ctx context.Context, ids []ProgramID) ([]ProgramStatus, error)

    // 可能返回 GATED_BY_SIGNUP / GATED_BY_NDA / UNKNOWN，而不是空 Scope
    SyncScope(ctx context.Context, id ProgramID) (Scope, error)
    SyncPolicy(ctx context.Context, id ProgramID) (Policy, error)

    // 只生成报告草稿；不提交
    PrepareReport(ctx context.Context, d Draft) (PreparedReport, error)

    // 必须携带人工批准凭证；Adapter 本身不得自动调用
    SubmitReport(ctx context.Context, r PreparedReport, approval HumanApproval) (Receipt, error)
}
```

### 事实与判断必须分层

不能把 Agent 抽取的 `status/end_date` 直接当事实存。必须：

```text
Evidence（截图/HTML/公告原文/接口响应）
    ↓
ObservedFact（registration_end = 2026-11-30）
    ↓
DerivedState（registration_open = true）
    ↓
PolicyDecision（ALLOW_VIEW / REQUIRE_HUMAN_TO_REGISTER）
```

每个 Fact/State/Decision 都要带 `observed_at`、`source/evidence_refs`、`confidence`、`rule_version`。抽取错了可以回溯重算，而不是污染数据库。

**GROW 案例**：平台活动中心显示“漏洞挖掘实战-第二期 2026-10-01~10-31”，但 GROW 整体/AI 赛道的规则是“报名 2026-08-25~11-30、活动 2026-09-01~11-30”。前者是月度阶段，后者才是项目周期；两者必须分别建模，不能把月度阶段当成整个项目结束时间。

### Eligibility 不是枚举，是 Decision + Gates

一个项目可以同时需要报名、审核、NDA、银行卡、历史信誉、学生认证。因此：

```go
type EligibilityDecision string

const (
    Eligible          EligibilityDecision = "ELIGIBLE"
    Ineligible        EligibilityDecision = "INELIGIBLE"
    HumanAction       EligibilityDecision = "HUMAN_ACTION_REQUIRED"
    Unknown           EligibilityDecision = "UNKNOWN"
)

type EligibilityState struct {
    Decision EligibilityDecision
    Gates    []EligibilityGate
    EvaluatedAt time.Time
    PolicyVersion string
}

type EligibilityGate struct {
    Kind      GateKind
    Required  bool
    Satisfied GateSatisfaction // SATISFIED | UNSATISFIED | UNKNOWN | NOT_REQUIRED
    Blocking  bool
    Evidence  []EvidenceRef
    Notes     string
}
```

补天可以自然表示：

```text
IDENTITY_VERIFIED        SATISFIED
COMPLIANCE_EXAM          UNSATISFIED  (blocking)
PRACTICAL_CERTIFICATION  UNSATISFIED  (blocking for 专属SRC)
PROGRAM_SIGNUP           UNSATISFIED  (blocking for 该专属SRC)
NDA                      UNKNOWN
```

而不是强行贴一个 `LOCKED_BY_REPUTATION` 标签。

### 账号级门槛必须单独建模

补天合规考核失败是**整个账号不能提交**，不是某个 Program 的属性。因此：

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
```

`program.gates` 只放项目级门槛（报名/审核/NDA/学生认证/团队）；`AccountCapability` 放账号级门槛（实名/合规考核/实战认证/银行卡/封禁）。

### Policy Engine 评估对象与顺序

判断对象不能只是 Program，至少是：

```text
Actor + PlatformAccount + Program + Asset + Action + Time
```

评估顺序：

1. **证据是否新鲜**：`observed_at` 是否在 TTL 内？否则 `REQUIRE_MORE_EVIDENCE`。
2. **账号能力**：`can_submit/can_view_scope/can_payout` 与 blocking gates；不合规 → `DENY` 或 `REQUIRE_HUMAN`。
3. **项目派生状态**：`RUNNING/PAUSED/ENDED/UNKNOWN`；非 RUNNING → `DENY`。
4. **项目门槛**：报名/审核/邀请/NDA/学生/团队；未满足 → `REQUIRE_HUMAN`（人工操作）。
5. **Scope 可见性**：`GATED_*` 且未解锁 → `DENY`（不能把 UNKNOWN 当空范围）。
6. **动作策略矩阵**：同一项目不同动作结果不同（见下表）。
7. **执行约束**：RPS/并发/日限额/预算/时间窗/VPN/测试账号报备/数据读取上限。
8. **报告完整性**：AI 人工验证、PoC/截图/数据包；缺失 → `REQUIRE_MORE_EVIDENCE`。
9. **人工批准**：`REPORT_SUBMIT`/`PAYOUT`/`PROGRAM_SIGNUP`/`NDA_ACCEPT` 必须 `HumanApproval`。

### 动作级策略矩阵（关键）

同一个 Running 项目，不同动作的决策不同：

```text
VIEW_RULES          → ALLOW
READ_SCOPE          → ALLOW (PUBLIC) / REQUIRE_HUMAN (GATED)
PASSIVE_ANALYSIS    → ALLOW (conditional)
ACTIVE_HTTP         → CONDITIONAL
AUTOMATED_SCAN      → DENY (default)
RCE_VERIFY          → CONDITIONAL
REPORT_DRAFT        → ALLOW
REPORT_SUBMIT       → REQUIRE_HUMAN
PAYOUT              → REQUIRE_HUMAN
```

执行层（Pi/Strata/本地模型）**没有权限决定自己能不能执行动作**；它只能提交 `ActionRequest`，由 Go 返回 `ActionDecision`。

输出四态：`ALLOW` / `DENY` / `REQUIRE_HUMAN` / `REQUIRE_MORE_EVIDENCE`。

## 4. 数据模型拆分

必须分成四层，不能合并：

```text
PlatformPolicy（平台默认）
  ├─ capabilities（默认 allowed/conditional/forbidden）
  ├─ global_constraints（无平台级QPS；禁止DoS/内网/大规模扫描）
  └─ ai_policy（3/5 阈值、人工验证）

AccountCapability（账号级）
  ├─ identity/compliance/certification/bank/wechat
  ├─ CanSubmit / CanViewScope / CanPayout
  └─ BlockingReasons[]（EligibilityGate）

ProgramPolicy（项目级）
  ├─ gates（报名/审核/NDA/学生/团队）
  ├─ scope（visibility/tiers/include/exclude）
  ├─ constraints（VPN/日限额/预算/先到先得/测试账号报备）
  ├─ automation_overrides（如淘天反爬允许浏览器自动化）
  └─ reward（资产系数/分期/税口径）

ActionPolicy（动作级）
  ├─ 按 Action 查表
  ├─ 结合 Asset/Time/约束
  └─ 输出四态 Decision
```

评估时 `ProgramPolicy` 覆盖 `PlatformPolicy`；`AccountCapability` 是前置硬门；`ActionPolicy` 决定同一项目下每个动作的边界。项目级“放宽”必须有明确原文，不能推测。

### Transport 抽象

内部前端接口不是官方 API，必须区分：

```go
type TransportKind string

const (
    OfficialAPI          TransportKind = "OFFICIAL_API"
    BrowserPage          TransportKind = "BROWSER_PAGE"
    BrowserObservedAPI   TransportKind = "BROWSER_OBSERVED_API"
    ManualImport         TransportKind = "MANUAL_IMPORT"
)
```

先知 `openapi/cst/*`、漏洞盒子 `vapi.vulbox.com/*`、补天 `/WhiteHat/Center/*` 均归为 `BROWSER_OBSERVED_API`。接口改版只影响采集器，不影响 Domain Model；不得绕过认证直接调用。

## 5. 同步与缓存

| 数据 | 建议频率 | 理由 |
|------|----------|------|
| 项目状态 | 15-60 分钟 | 漏洞盒子会数小时内暂停/重开；先知有任务暂停；补天有活动结束 |
| 项目列表 | 1-6 小时 | 新项目/活动发布；高级项目通常20:00发布 |
| 公告/规则 | 6-24 小时 | AI政策/奖励规则会更新 |
| 账号状态 | 1 天 | 等级/积分/待审核/合规考核 |
| 财务/收款 | 1 天 | 余额/打款状态 |
| 排行榜 | 1 天 | 竞争环境 |

## 6. 三平台接入顺序

| 维度 | 先知 | 漏洞盒子 | 补天 |
|------|------|----------|------|
| 数据可获取性 | 3/5（结构化任务，但scope报名后才可见） | 5/5（vapi + 已加入企业SRC） | 2/5（合规/实战认证硬门槛） |
| 项目数量 | 3/5（91，34进行中） | 5/5（约1513*，51企业SRC） | 5/5（6,655企业，37专属SRC） |
| 规则清晰度 | 4/5（公告详细） | 4/5（FAQ详细，NDA明确） | 5/5（公告极详细） |
| 新人机会 | 2/5（0任务已报名，无新秀榜） | 4/5（见习+2个已加入企业SRC+新秀榜） | 4/5（GROW校园+公益SRC，但合规考核） |
| Agent 可用程度 | 3/5（AI允许，但scope报名后才可见） | 4/5（API+已加入项目） | 2/5（合规考核+实战认证） |
| 收款便利 | 2/5（最低额/周期UNKNOWN） | 4/5（最低300，次月10-15，银行卡） | 4/5（每周三，代扣税，银行卡） |
| **合计** | **17/30** | **26/30** | **22/30** |

**结论：漏洞盒子最适合率先接入 Go。** 理由：已有2个可直接提交的企业SRC、约1513个项目（估计值）与清晰的奖励范围、FAQ把提现/审核/NDA讲清楚、内部API结构最规整。先知作为第二优先级（高赏金、长期SRC多，但资产范围报名后才可见）。补天第三（合规考核+实战认证使当前无法提交），但**校园GROW计划的AI辅助漏洞挖掘赛道**是对用户学生身份 + AI Agent 方向最有战略价值的入口，建议人工优先完成合规考核/学生认证后再接入。

## 7. 安全与合规设计

- NDA 项目：进入前停止抓取；不把项目名/资产/奖励写入共享日志；本地加密存储。
- 报告：AI 使用与人工验证结果必须记录；提交前人工逐条确认。
- 数据：不落盘用户数据/源码；测试数据按平台要求删除；敏感字段只记“已配置/未配置”。
- Kill switch：调度前必须重新读项目状态；不能依赖缓存。
- 审计：每个 `ALLOW/DENY` 记录 `platform/program/policy_version/evidence_url/observed_at`。
