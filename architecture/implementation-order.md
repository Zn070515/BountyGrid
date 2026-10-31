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
| Phase 1：Browser Sidecar + Vulbox / Xianzhi / Butian Readers | 进行中：1.1 已完成；1.2 公开列表/账号代码已落地，账号实测待人工登录；1.3/1.4 未开始 |
| Phase 2：Facts + State | 提前实现并硬化（Reader 接入后回填真实 Fact） |
| Phase 3：Policy Engine | 提前实现并硬化 |
| Phase 4：Opportunity / Scheduler | Phase 1 Reader 接入之后 |
| Phase 5+：Harness / Model Router / Verification / Submission / Ledger | 规划中 |

> 当前实现顺序与 canonical 阶段存在交叉：Phase 2/3 的内核先于 Phase 1 Reader 落地，这是为了先把安全不变量固定下来；不改变 Phase 1 的定义，也不把 Reader 推迟成“Phase 2”。

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

**当前实现**：`internal/sidecar` 包装 `agent-browser` CLI，只暴露 `open/wait/eval/get/screenshot/state save/close`；没有 click/fill。会话检测与 state 保存已由探针验证；产品 Reader 复用 `vbprobe`。剩余：把 CLI 包装升级为长期驻留 RPC（当前每次命令一个进程）时再评估。

### 1.2 Vulbox Reader

- AuthStatus、SyncAccount、SyncPrograms、SyncProgram、SyncScope、SyncPolicy。
- Facts：账号等级/经验/信誉/已加入项目；项目状态/奖励/类型；Scope 可见性；FAQ/公告。
- 退出条件：在真实账号上生成 Account/Program/Scope/Policy 快照；所有 Fact 有 EvidenceRef；Vulbox 1513 项目标注 estimated。

**当前实现（2026-10-31）**：

- `AuthStatus`、`SyncPrograms`、`SyncAccount` 已实现；公开项目列表实测通过：第 1 页 10 项、`estimated_total=1513`、150 条 Fact、1 条 Evidence，所有 Fact 有 EvidenceRef。
- 账号实测通过：见习白帽 / exp 10 / 信誉 100 / 排名 9999+ / 实名 / `CanSubmit=true` / `CanPayout=false` / 已加入 2。
- 企业SRC详情已实现并实测联想 444：Scope `PUBLIC+GRANTED+known`、include 9 条、exclude `*.sunits.com`、`FileUpload=FORBIDDEN`、`AIAnalysis=CONDITIONAL`；Scope/Policy 各有独立 Evidence。
- 普通/高级项目详情页、已加入项目 ID 回填、平台 FAQ 策略合并仍未完成；非企业SRC仍返回 `ErrUnsupported`（不猜）。
- 退出条件仍差：普通项目 Scope/Policy、Xianzhi/Butian Reader。

### 1.3 Xianzhi Reader

- 阿里云 OAuth 会话；任务列表 91；状态/报名/任务时间；Scope NotSignUp。
- 退出条件：91 任务全部落库；Scope 全部 `GATED_BY_SIGNUP`；已结束任务不被误判 RUNNING。

**当前实现（2026-10-31）**：

- `internal/adapter/xianzhi` 已实现 `AuthStatus/SyncAccount/SyncPrograms/SyncProgram/SyncScope/SyncPolicy`，任务数据从 `.card-item` 的 React fiber `task` 对象读取。
- 状态/类型映射已实测：`status 4/5/6 = RUNNING/ENDED/PAUSED`，`type 1/2 = SRC/企业众测`；全量 5 页实测 **91 条 = 34 RUNNING / 54 ENDED / 3 PAUSED，61 SRC / 30 企业众测**，1058 Fact、5 Evidence，与审计一致。
- `SyncScope` 对未报名任务返回 `GATED_BY_SIGNUP + NOT_GRANTED + ContentsKnown=false`；已报名任务的资产解析未验证。
- 账号同步 `auth=OK`，但个人资料页只有“实名认证”标签、无文本状态；`identity_verified` 保持 nil（`CanSubmit=false`，fail-closed），待补账号状态提取器。
- 翻页/同 hash 重开需 `reload`；曾遇控制台数据接口 `os error 10060` 导致 SPA 0 卡片，reload 后恢复。退出条件中的“落库”仍需 Phase 2 用例层持久化。

### 1.4 Butian Reader

- 奇安信 SSO 会话；账号 Capability（合规考核/实战认证）；37 专属SRC；公益SRC 分页；活动/GROW。
- 退出条件：`CanSubmit=false` 正确派生；GROW 日期 8/25-11/30 与月度阶段 10/01-10/31 分开；专属SRC Scope GATED。

**当前实现（2026-10-31）**：

- `internal/adapter/butian` 已实现 `AuthStatus/SyncAccount/SyncPrograms/SyncProgram/SyncScope/SyncPolicy`。
- 37 专属SRC 已实测：从项目大厅已渲染 DOM 的 `.plan-exclusive-src .company-card` 读取，**37/37，259 Fact，1 Evidence**，全部 `RUNNING/PRIVATE_EXCLUSIVE/HUMAN_ACTION_REQUIRED`（报名 + 实战认证）。
- 快照持久化已接入 `sync -persist`：Evidence → Facts → Program/Account；实测 Vulbox 50 项目/750 Fact/1 Evidence 落库。
- 卡片无原生 company ID，ProgramID = 公司名 SHA-1 前缀；后续有原生 ID 时迁移。
- 账号中心/提交页的浏览器导航报 `os error 10060`（curl 302 可达），反爬/验证码会话阻止新导航；账号 Gate 代码已实现但未实测，`ComplianceExam` 未知时强制 `CanSubmit=false`。
- 公益SRC `/Reward/pub` 可用（200 页/30 条）但未接入；GROW 页面未联调（`GrowExtractor` 已有单测）。
- 退出条件仍差：账号 `CanSubmit=false` 实测、GROW 日期实测、公益SRC 分页接入。

## 2. Fact/State 层

### 2.1 Extractor 框架

- 版本化抽取器；失败不写猜测。
- `superseded_by`；重跑 Evidence。
- 退出条件：同一 Evidence 重跑幂等；错误抽取可回滚。

### 2.2 State Deriver

- ProgramStatus、RegistrationState、ScopeVisibility、AccountAbility、Competition。
- TTL/ValidUntil；输入 Fact 变化使下游失效。
- 退出条件：GROW 案例、Vulbox 暂停/重开、Xianzhi NotSignUp 三个回归测试通过。

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

## 4. Opportunity + Scheduler

### 4.1 CompetitionSnapshot

- 三平台榜单/项目数/奖励池；Top1/3/10 share；confidence。
- 退出条件：先知 70.8%、漏洞盒子 Top10 传说、补天 164,012 元三个样本可复算。

### 4.2 Opportunity Engine

- 期望收益/成本/竞争惩罚/置信度。
- 无数据时保守先验。
- 退出条件：不得用“最高赏金”排序；分数可解释（inputs/weights）。

### 4.3 Scheduler

- 硬过滤 → 打分 → 配额 → 人工队列 → 冷却 → kill switch。
- 退出条件：PAUSED/ENDED 不派发；人工队列不自动执行。

## 5. Harness + Model Router

### 5.1 Tool Registry

- 工具声明 RequiredAction；所有平台动作走 Action Gateway。
- 沙箱/网络白名单/预算。
- 退出条件：工具无法绕过 Policy；越权请求被 DENY。

### 5.2 Model Router

- 路由/预算/数据分级/审计。
- 模型输出不直接执行动作。
- 退出条件：SENSITIVE 数据默认本地；模型调用有成本/证据记录。

## 6. Verification + Report

### 6.1 Verification

- Scope/复现/影响/数据最小化/重复风险/AI验证。
- 模型不能单独判 VERIFIED。
- 退出条件：未复现 Finding 不能进入报告。

### 6.2 ReportDraft

- 三平台字段映射；必填检查；AI 披露。
- 退出条件：缺字段返回 REQUIRE_MORE_EVIDENCE；补天 GROW AI 字段完整。

## 7. Submission（Vulbox 首个）

### 7.1 Human Approval

- 审批界面/令牌/过期/范围。
- 退出条件：无审批不能提交；审批过期不能提交。

### 7.2 Vulbox Submit

- 联想/嘉立创真实表单字段；草稿箱；提交后状态。
- 退出条件：只读演练（不真正提交）通过；人工确认后可提交一次测试。

### 7.3 Xianzhi/Butian Submit

- 先知：报名后表单；补天：合规考核后表单。
- 退出条件：前置 Gate 未满足时提交入口不可达。

## 8. Revenue Ledger

- Expected/Submitted/Accepted/Paid/Fee/Tax/Refund。
- 对账/差异/证据。
- 退出条件：账本只追加；金额非负；按平台汇总可对账。

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
