# 实现依赖顺序

- 这不是“MVP 第一版/第二版”切分；所有模块都要实现。
- 顺序只由依赖关系决定：下层不稳定，上层不写。
- 每个阶段有可验证的退出条件。

## 环境分工（先明确）

| 环境 | 职责 |
|------|------|
| 本机（当前 Windows） | 讨论、审计、只读探针；GitHub 同步后**也可开发** Go 模块/PostgreSQL/Adapter/Policy/测试 |
| 台式机 | BountyGrid 开发（可选/并行），通过 GitHub 同步 |
| 部署 | 单机、单用户、模块化单体；本机 PostgreSQL |

本机上的 `probes/` 是可丢弃的只读探针，不属于产品代码，也不进入 Go module。探针只验证平台事实/选择器/日期/会话，不报名、不签NDA、不提交、不提现。

## 当前进度

| 阶段 | 状态 |
|---|---|
| Phase 0：domain + evidence + PostgreSQL schema/repositories/rollback + CLI | 完成 |
| Phase 1：Browser Sidecar + Vulbox / Xianzhi / Butian Readers | 基本完成：三平台主路径已实测并落库；部分 scope/detail 与数据语义仍在收口 |
| Phase 2：Facts + State | 已实现并硬化；State TTL 受输入 Fact 年龄限制 |
| Phase 3：Policy Engine / Gateway | 已实现并硬化：authoritative Asset、ExecutionEnvironment、Authorize 重评估、审批在执行前消费 |
| Phase 4：Opportunity / Scheduler | 已实现：append-only 评分历史、DB cooldown、唯一 QUEUED 任务、CompetitionSnapshot TTL/decay |
| Phase 5：Harness / Model Router | Harness 边界/沙箱/预算/执行回执已实现；Model Router 路由/数据分级/预算/审计已实现（无真实 Provider），无 ACTIVE_HTTP 工具 |
| Phase 6：Verification / Report / Submission / Ledger | Verification、ReportDraft、Submission dry-run、CNY 现金 Ledger 已实现；真实平台 SubmitAdapter 与 Ledger 反馈闭环未进入 |

> **真实状态：Phase 0 complete; Phase 1–6 partially implemented; active execution remains disabled.**
>
> 当前实现顺序与 canonical 阶段存在交叉：Phase 2/3 的内核先于 Phase 1 Reader 完整落地，这是为了先把安全不变量固定下来；不改变 Phase 1 的定义，也不把 Reader 推迟成“Phase 2”。

## 0. 基础层（无业务依赖）

### 0.1 `internal/domain`

- 所有类型/枚举/不变量（见 `domain-model.md`）。
- 纯单元测试：Scope GATED、EligibilityState、ActionDecision、金额/范围。
- 退出条件：`go test ./internal/domain/...` 通过；无外部依赖。

### 0.2 `internal/evidence` + `internal/audit`

- Evidence 存储抽象、hash、脱敏、TTL。
- 审计事件只追加。
- PostgreSQL schema：`evidence`、`audit_events`。
- 退出条件：写入/读取/脱敏/去重测试通过；敏感值扫描通过。

### 0.3 `internal/store/postgres`

- 实体仓储：account/program/scope/fact/state/decision/opportunity/task/finding/report/ledger。
- 迁移脚本；事务边界；幂等键。
- 退出条件：迁移可在空库执行；仓储测试通过。

## 1. 只读采集层（Vulbox 优先）

### 1.1 Browser Sidecar + Transport

- Playwright/CDP sidecar；`Page`/`ObservedJSON` RPC。
- 会话保存/恢复；不在日志输出 Cookie。
- 退出条件：能打开登录页、检测会话、截图、保存 state。

**当前实现**：`internal/sidecar` 包装 `agent-browser` CLI，暴露 `open/wait/reload/eval/get/screenshot/state save/close`；没有 click/fill 命令。它不是安全边界：`Eval` 可执行任意 JS，Reader 只把它用于只读观察与分页/弹窗控制，Harness/Model 不得直接持有。会话检测与 state 保存已由探针验证；产品 Reader 复用 `vbprobe`。剩余：把 CLI 包装升级为长期驻留 RPC（当前每次命令一个进程）时再评估。

### 1.2 Vulbox Reader

- AuthStatus、SyncAccount、SyncPrograms、SyncProgram、SyncScope、SyncPolicy。
- Facts：账号等级/经验/信誉/已加入项目；项目状态/奖励/类型；Scope 可见性；FAQ/公告。
- 退出条件：在真实账号上生成 Account/Program/Scope/Policy 快照；所有 Fact 有 EvidenceRef；Vulbox 1513 项目标注 estimated。

**当前实现（2026-10-31）**：

- `AuthStatus`、`SyncPrograms`、`SyncAccount` 已实现；公开项目列表实测通过：第 1 页 10 项、`estimated_total=1513`、150 条 Fact、1 条 Evidence，所有 Fact 有 EvidenceRef。
- 账号实测通过：见习白帽 / exp 10 / 信誉 100 / 排名 9999+ / 实名 / `CanSubmit=true` / `CanPayout=false` / 已加入 2。个人中心有促销抽屉遮挡，`readPage` 只读关闭并等待“已实名”后抽取；修复后调度器为联想 444 产出 `QUEUED` research task。
- 企业SRC详情已实现并实测联想 444：Scope `PUBLIC+GRANTED+known`、include 9 条、exclude `*.sunits.com`、`FileUpload=FORBIDDEN`、`AIAnalysis=CONDITIONAL`；Scope/Policy 各有独立 Evidence。
- 普通/高级项目详情页、已加入项目 ID 回填、平台 FAQ 策略合并仍未完成；非企业SRC仍返回 `ErrUnsupported`（不猜）。
- 退出条件仍差：普通项目 Scope/Policy、Xianzhi/Butian Reader。

### 1.3 Xianzhi Reader

- 阿里云 OAuth 会话；任务列表 91；状态/报名/任务时间；Scope NotSignUp。
- 退出条件：91 任务全部落库；Scope 全部 `GATED_BY_SIGNUP`；已结束任务不被误判 RUNNING。

**当前实现（2026-10-31）**：

- `internal/adapter/xianzhi` 已实现 `AuthStatus/SyncAccount/SyncPrograms/SyncProgram/SyncScope/SyncPolicy`，任务数据从 `.card-item` 的 React fiber `task` 对象读取。
- 状态/类型映射已实测：`status 4/5/6 = RUNNING/ENDED/PAUSED`，`type 1/2 = SRC/企业众测`；全量 5 页实测 **91 条 = 34 RUNNING / 54 ENDED / 3 PAUSED，61 SRC / 30 企业众测**，1058 Fact、5 Evidence，与审计一致；已通过 `-persist` 落库。
- `SyncScope` 对未报名任务返回 `GATED_BY_SIGNUP + NOT_GRANTED + ContentsKnown=false`；已报名任务的资产解析未验证。
- 账号同步 `auth=OK`，但个人资料页只有“实名认证”标签、无文本状态；`identity_verified` 保持 nil（`CanSubmit=false`，fail-closed），待补账号状态提取器。
- 翻页/同 hash 重开需 `reload`；曾遇控制台数据接口 `os error 10060` 导致 SPA 0 卡片，reload 后恢复。退出条件中的“落库”仍需 Phase 2 用例层持久化。

### 1.4 Butian Reader

- 奇安信 SSO 会话；账号 Capability（合规考核/实战认证）；37 专属SRC；公益SRC 分页；活动/GROW。
- 当前（2026-11-01）除 1 企业SRC 详情与活动中心列表外均已实现。
- 退出条件：`CanSubmit=false` 正确派生；GROW 日期 8/25-11/30 与月度阶段 10/01-10/31 分开；专属SRC Scope GATED。

**当前实现（2026-10-31）**：

- `internal/adapter/butian` 已实现 `AuthStatus/SyncAccount/SyncPrograms/SyncProgram/SyncScope/SyncPolicy`。
- 37 专属SRC 已实测：从项目大厅已渲染 DOM 的 `.plan-exclusive-src .company-card` 读取，**37/37，259 Fact，1 Evidence**，全部 `RUNNING/PRIVATE_EXCLUSIVE/HUMAN_ACTION_REQUIRED`（报名 + 实战认证）。
- 快照持久化已接入 `sync -persist`：Evidence → Facts → Program/Account；实测 Vulbox 50 项目/750 Fact/1 Evidence、Xianzhi 91 项目/1058 Fact/5 Evidence、Vulbox 444 详情（PUBLIC/GRANTED，FileUpload=FORBIDDEN）落库；累计 141/1830/8，0 条 Fact 缺 EvidenceRef。
- 卡片无原生 company ID，ProgramID = 公司名 SHA-1 前缀；后续有原生 ID 时迁移。
- 账号同步已成功（auth=OK）：`CanSubmit=false`（合规考核未完成 / 专属SRC实战认证），已落库。之前的账号中心导航 `os error 10060` 是瞬时浏览器连接失败，重试后成功；登录态一直在。
- 37 专属SRC 已通过 `-persist` 落库（三平台合计 178 项目）；`derive` 产出 74 个 Butian states；`schedule` 34 human（ACCOUNT_CAPABILITY）、3 个无赏金数据。Xianzhi 91 任务只读成功，但 0 个已报名，Scope 必须 GATED；Vulbox 1 QUEUED research（444）。
- 公益SRC `/Reward/pub?p=N` 已接入（`-public-welfare`，可选能力 `adapter.PublicWelfareReader`）：必须带 `X-Requested-With: XMLHttpRequest`，`p` 才是分页参数；实测 200 页/30 条、末页 9 条 = 5,979 家，Scope 保持 `UNKNOWN`，状态 `RUNNING` confidence 0.8。
- GROW 已联调（`-grow`，可选能力 `adapter.GrowReader`）：规则弹窗 + 活动中心两页分别落 Evidence，实测报名 8/25–11/30、活动 9/1–11/30、第二期 10/01–10/31 共 6 条 Fact；月度阶段正则锚定 GROW 标题。
- 退出条件已满足：账号 `CanSubmit=false` 实测、GROW 日期实测、公益SRC 分页接入。剩余：1 企业SRC 详情、活动中心列表。

## 2. Fact/State 层

### 2.1 Extractor 框架

- 版本化抽取器；失败不写猜测。
- `superseded_by`；重跑 Evidence。
- 退出条件：同一 Evidence 重跑幂等；错误抽取可回滚。

### 2.2 State Deriver

- ProgramStatus、RegistrationState、ScopeVisibility、AccountAbility、Competition。
- TTL/ValidUntil；输入 Fact 变化使下游失效。
- 退出条件：GROW 案例、Vulbox 暂停/重开、Xianzhi NotSignUp 三个回归测试通过。

**当前实现（2026-10-31）**：

- `bountygrid derive -platform ...` 从 PostgreSQL 读取 Fact → `ProgramInputFromFacts` → `DeriveProgramState/ScopeVisibility/RegistrationOpen` → `PutState`。
- 实测 Xianzhi 273 + Vulbox 100 = **373** 条 `derived_states`；`inputs` 为空 0、confidence 越界 0、已过期 0。
- Fact supersede 已在 PostgreSQL 同一事务内失效下游 State（`valid_until=now()`，`inputs` GIN 索引）；集成测试覆盖。
- 显式 `RUNNING` 不再覆盖已过的 inclusive end（修复 live 发现的问题）。
- State 不能比输入 Fact 更新：`ValidUntil = min(now+TTL, oldestSelectedFact+30d)`，`ObservedAt` 记录最旧所选 Fact 的观察时间；超过 30 天的 Fact 不能生成新 State。
- 仍差：AccountAbility/Competition 状态派生、Adapter 自动触发 re-derive、GROW/Vulbox 暂停重开回归接入 CLI。

## 3. Policy Engine

### 3.1 Eligibility

- AccountCapability + Program Gates + Decision。
- 账号级 Gate 与项目级 Gate 分离。
- 退出条件：补天合规考核/实战认证、漏洞盒子高级项目 NDA、先知报名门槛四组测试通过。

### 3.2 Action Policy

- 动作矩阵、硬拒绝、项目覆盖、约束检查、人工审批。
- 退出条件：`policy-engine.md` 的 10 条测试矩阵全部通过。

### 3.3 Action Gateway

- ActionRequest → Decision → 执行/拒绝/人工队列。
- Decision 过期二次校验。
- 退出条件：过期 Decision 不能执行；DENY 不产生副作用。

**当前实现（2026-10-31）**：

- `internal/gateway`：`Evaluate` 从 PostgreSQL 加载 Account/Program，从 `Program.Scope.ResolveAsset` 解析 canonical Asset，调用 Policy Engine，持久化 `ActionDecision` + `action.decision` 审计；缺失上下文/未知资产持久化 DENY。PostgreSQL 使用 `RecordDecisionWithAudit` 把决策与审计写进同一事务。
- `Authorize`：执行前校验 ALLOW/`ExpiresAt`，然后重新加载 Account/Program、重新解析 Asset 并重跑 Policy；Scope/Automation/Eligibility 变化后旧 ALLOW 直接失败。single-use 审批只在重评估通过后原子消费。Gateway 不执行平台动作。
- 实测：VIEW_RULES ALLOW + authorize=true；REPORT_SUBMIT DENY ACCOUNT_CANNOT_SUBMIT；ACTIVE_HTTP 缺 Asset DENY ASSET_REQUIRED；未知 AssetID DENY ASSET_UNKNOWN；`action_decisions=3`、审计 3 条。
- 仍差：人工队列 UI、Decision 缓存、Model Router。

## 4. Opportunity + Scheduler

### 4.1 CompetitionSnapshot

- 三平台榜单/项目数/奖励池；Top1/3/10 share；confidence。
- 退出条件：先知 70.8%、漏洞盒子 Top10 传说、补天 164,012 元三个样本可复算。

**当前实现（2026-11-01）**：`competition` CLI 从带 Evidence 的 JSON 导入快照，校验 share/confidence 与 `top1 <= top3 <= top10`，写 `competition_snapshots`；导入时设置 `valid_until = observed_at + 30d`。三份审计快照已落库（先知 0.708/0.7、漏洞盒子 0.5/0.5、补天 0.5/0.5，后两者 top10 share 缺失按 0.5 保守估计并在 Evidence 注明）。`schedule` 只读取未过期快照；Opportunity Engine 在 7 天后线性衰减 confidence，30 天后完全忽略，并在 `inputs` 记录 `competition_age_hours`/`competition_decay`。剩余：自动采集榜单（当前是人工导入）、Program-scoped 快照。

### 4.2 Opportunity Engine

- 期望收益/成本/竞争惩罚/置信度。
- 无数据时保守先验。
- 退出条件：不得用“最高赏金”排序；分数可解释（inputs/weights）。

**当前实现（2026-11-01）**：`internal/opportunity.Engine` 已实现；`Inputs[]` 记录 p_valid、min/median/max 权重、top10/duplicate/saturation 权重、penalty、cost、confidence。缺/过期 CompetitionSnapshot 时 penalty=0.5/0.5/0.5、confidence=0.2。每次评分的 `OpportunityID` 含评估时间（append-only），不会覆盖历史。单测覆盖“高最高赏金但高集中度”输给小赏金程序，以及先知 70.8%/漏洞盒子/补天 164,012 元三个竞争样本。

### 4.3 Scheduler

- 硬过滤 → 打分 → 配额 → 人工队列 → 冷却 → kill switch。
- 退出条件：PAUSED/ENDED 不派发；人工队列不自动执行。

**当前实现（2026-11-01）**：`schedule` 从 PostgreSQL 读取 Program + 最新 `PROGRAM_STATE` + Account；RUNNING 才评分；CanSubmit/Eligibility/Scope 不满足只进 HumanActionQueue；Top-K/每平台/每项目配额/企业SRC保留位；Persist 写 `opportunities` 与 `research_tasks`（`QUEUED`/`HUMAN_ACTION_REQUIRED`），不执行。任务 ID 为 `task/<program>/<schedule-run>`；同一 Program 只保留一个 `QUEUED` 任务（旧任务标 `SUPERSEDED`）；cooldown 从 `research_tasks` 读取，不再依赖调用方传空 `Existing`。仍差：Harness 自动领取、人工队列 UI、Ledger 反馈。

## 5. Harness + Model Router

### 5.1 Tool Registry

- 工具声明 RequiredAction；所有平台动作走 Action Gateway。
- 沙箱/网络白名单/预算。
- 退出条件：工具无法绕过 Policy；越权请求被 DENY。

**当前实现（2026-11-01）**：`internal/harness` 的 Registry 校验工具名/动作/重复；Harness 强制 Evaluate→ALLOW→Budget→Authorize→Execute，DENY/REQUIRE_HUMAN 不调用工具；网络工具只拿到 `harness.Egress`（每次请求/重定向走 `Sandbox.CheckURL`），空 allowlist 直接拒绝；Destructive 默认禁用。`NetworkEgress`/`WritesFiles`/`Destructive` 工具必须有 `IdempotencyKey`，执行前预占 `execution_receipts`，重试回放而不再执行；工具已执行但审计失败时，回执保持 `EXECUTED`，重试只补审计。内置 `view_rules/read_scope/report_draft` 只读工具；`tool.executed` 审计。实测 ALLOW 执行、缺失账号 DENY 且工具未执行；`action_decisions=5`、`tool.executed=1`。仍差：真实执行工具（本仓库禁止真实测试）、ResearchTask 自动领取。

### 5.2 Model Router

- 路由/预算/数据分级/审计。
- 模型输出不直接执行动作。
- 退出条件：SENSITIVE 数据默认本地；模型调用有成本/证据记录。

**当前实现（2026-11-01）**：`internal/model` 实现 `Purpose/DataClass/ProviderInfo/Provider/Router`；SENSITIVE 只走本地，无本地 Provider 直接 `ErrNoProvider`；PLAN/ANALYZE 优先强远程、CODE/EMBED/REPORT 优先本地；调用前估算、调用后按实际 usage 复核预算；每次尝试写 `model.call` 审计（只有 hash/usage/cost，不含原文）。`bountygrid model` 仅输出路由计划，不调用模型。仍差：真实本地/云端 Provider、持久化 `model_calls`、ResearchTask 自动领取。

## 6. Verification + Report

### 6.1 Verification

- Scope/复现/影响/数据最小化/重复风险/AI验证。
- 模型不能单独判 VERIFIED。
- 退出条件：未复现 Finding 不能进入报告。

**当前实现（2026-11-01）**：`internal/verify.RuleVerifier` 按 Scope → 授权 → 数据最小化/公开已知 → 重复 → AI 人工验证 → 复现/影响的顺序判定；`VERIFIED` 要求四项 flag 全真、有 Evidence、`DuplicateChecked`，AI 辅助时持久化人工验证 trace。`bountygrid verify -file` 从 PostgreSQL 加载 Program/Decision/Finding、校验证据存在性，`-persist` 写 `verifications`。仍差：Finding 状态机自动推进、人工复核 UI。

### 6.2 ReportDraft

- 三平台字段映射；必填检查；AI 披露。
- 退出条件：缺字段返回 REQUIRE_MORE_EVIDENCE；补天 GROW AI 字段完整。

**当前实现（2026-11-01）**：`internal/report.Build` 只接受 `VERIFIED` Verification；通用必填字段 + Xianzhi `account_name` / Butian `vendor/category/asset_scope/weight/activity/task_id/industry/region/contact`；AI 披露要求 `human_verification`，Butian GROW 还要求模型名/版本/工具名；附件按平台校验扩展名；report 文本含 cookie/token/密码/证件/银行卡/完整手机号/邮箱会被拒绝。`bountygrid report -file [-persist]` 从 PostgreSQL 加载 Program/Finding/Verification，写 `report_drafts`；Submission 见 7.1/7.2。仍差：ReportDraft→READY_FOR_REVIEW 的人工状态推进、附件大小/上传。

## 7. Submission（Vulbox 首个）

### 7.1 Human Approval

- 审批界面/令牌/过期/范围。
- 退出条件：无审批不能提交；审批过期不能提交。

**当前实现（2026-11-01）**：`internal/submit` 校验 `HumanApproval` 的 action/request_id/program/asset/resource/parameters_hash/expiry/single_use；request_id 固定为 `submit/<report>/<approval>`，参数为 `{"report_id":...,"report_content_hash":...}`，审批后修改草稿会使 hash 不匹配。提交前重新解析当前 Scope 资产（必须 INCLUDED）；同一 report 只允许一次真实提交（`submissions.report_id` 部分唯一索引 + `GetSubmissionByReport` 前置检查 + `RecordSubmission` 事务）。`bountygrid submit -print-binding` 输出审批必须绑定的 request_id/parameters_hash。`ReportDraft.Status` 增加 `DRAFT/READY_FOR_REVIEW/APPROVED/SUBMITTED/...` 枚举；提交成功后置 `SUBMITTED`。

### 7.2 Vulbox Submit

- 联想/嘉立创真实表单字段；草稿箱；提交后状态。
- 退出条件：只读演练（不真正提交）通过；人工确认后可提交一次测试。

**当前实现（2026-11-01）**：`submit.Service` 在 Gateway `ALLOW` + `AuthorizeWith` 消费审批后才调用 `Submitter`。本仓库只有 `DryRunSubmitter`（本地 receipt，状态 `DRY_RUN`，无网络/平台动作）；dry-run 不改变 ReportDraft 状态，也不占用真实提交的唯一名额，因此后续仍可用新审批真实提交。`submissions` 持久化，`approval_id` 唯一索引防止同一审批重复提交，`report_id` 部分唯一索引（排除 `DRY_RUN`）防止同一报告真实提交两次。真实平台 SubmitAdapter 未实现。

### 7.3 Xianzhi/Butian Submit

- 先知：报名后表单；补天：合规考核后表单。
- 退出条件：前置 Gate 未满足时提交入口不可达。

## 8. Revenue Ledger

- Expected/Submitted/Accepted/Paid/Fee/Tax/Refund。
- 对账/差异/证据。
- 退出条件：账本只追加；金额非负；按平台汇总可对账。

**当前实现（2026-11-01）**：`internal/ledger` 实现 CNY 现金账本；`PAID`=到账净额，`FEE/TAX` 仅信息展示；`ExternalRef` 唯一索引防重复导入；`REVERSAL` 只冲 `PAID/REFUND`；`Append` 校验 Evidence 存在、Submission 非 `DRY_RUN`；`Summarize/Reconcile/Feedback` 已完成。`bountygrid ledger` 提供 append/summary/reconcile。非现金奖励留 `NonCashRecorder` 接口；`ReconciliationIssue` 持久化未实现。

## 9. 反馈闭环

- Ledger → P(valid)/E[reward]/E[cost]/duplicate_pressure。
- 退出条件：Opportunity 分数随历史数据变化；有版本记录。

## 10. 平台顺序与验收

| 阶段 | 平台 | 验收 |
|------|------|------|
| 1 | 漏洞盒子 | 账号/项目/状态/Scope/Policy/报告草稿全链路只读 |
| 2 | 先知 | 91 任务/状态/Scope GATED/公告规则 |
| 3 | 补天 | 账号 Capability/合规考核/实战认证/37 专属SRC/GROW |
| 4 | 三平台 | Policy Engine 动作矩阵全通过 |
| 5 | 漏洞盒子 | 人工审批后的首次真实提交演练 |
| 6 | 先知/补天 | 人工完成 Gate 后接入提交 |

## 11. 每阶段的质量门

- `go test ./...` 通过。
- `go vet ./...` 通过。
- 迁移可回滚。
- 审计事件完整。
- 敏感值扫描通过。
- 真实平台只读同步不触发任何状态变更。
- 任何平台动作都有 ActionDecision + Evidence + observed_at。

## 12. 不建议的捷径

1. 先写“自动扫描器”再接 Policy。
2. 先接提交，后补 Verification。
3. 用内部 API 绕过浏览器会话。
4. 把三平台字段硬编码成统一枚举。
5. 用“最高赏金”做排序。
6. 把 NDA/合规考核做成可自动点击。
