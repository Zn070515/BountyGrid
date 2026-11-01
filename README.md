<div align="center">

<h1>BountyGrid</h1>

<p><strong>漏洞赏金统一控制层</strong> · <strong>Bug bounty control plane for Xianzhi, Vulbox and Butian</strong></p>

<p>
  <a href="https://github.com/Zn070515/BountyGrid/actions/workflows/ci.yml"><img src="https://github.com/Zn070515/BountyGrid/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI status"></a>
  <img src="https://img.shields.io/badge/Go-1.27.3-00ADD8?logo=go&logoColor=white" alt="Go 1.27.3">
  <img src="https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white" alt="PostgreSQL 16">
  <img src="https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white" alt="Docker">
  <img src="https://img.shields.io/badge/Phase-0--6%20partial%20%C2%B7%20execution%20disabled-blue" alt="Phase 0–6 partial; active execution disabled">
</p>

<p>
  <a href="#三条线--three-lines">三条线</a> ·
  <a href="#核心模型--core-model">核心模型</a> ·
  <a href="#当前平台状态--platform-state">平台状态</a> ·
  <a href="#快速开始--quick-start">快速开始</a> ·
  <a href="#文档入口--docs">文档入口</a> ·
  <a href="#工程原则--engineering-rules">工程原则</a> ·
  <a href="#开发与合并流程">开发流程</a>
</p>

</div>

BountyGrid 是面向国内漏洞赏金平台（阿里云先知 / ASRC、漏洞盒子、补天）的 **统一控制层**。它不代替人挖洞，也不做自动扫描器；它负责把平台事实、账号资格、项目 Scope、测试约束、竞争数据与收益账本收束成一套可审计、可调度、可回放的 Go 系统。

当前阶段：**平台配置审计完成 + 完整架构定稿 + Phase 0 完成 + 核心内核（audit/facts/state/policy）硬化完成**。尚未对任何厂商资产发起真实测试，未报名、未签 NDA、未提交漏洞、未提现。

> **项目状态：** `platform-audit/` 是三平台账号与规则的事实基线；`architecture/` 是完整 Go 设计；`internal/` 是实现。验证入口以 `architecture/implementation-order.md` 的退出条件为准；本文不复述测试数量、commit 或 CI 结果，避免与代码漂移。

## 三条线 · Three lines

| 目录 | 角色 | 内容 | 状态 |
|---|---|---|---|
| [`platform-audit/`](platform-audit/) | 事实线 | 三平台账号状态、项目体系、Scope、规则、AI 政策、收款、自动化边界 | 审计完成；[`CORRECTIONS.md`](platform-audit/CORRECTIONS.md) 记录 GROW 日期等纠正 |
| [`architecture/`](architecture/) | 设计线 | Domain Model、Evidence/Fact/State、Adapter、Policy Engine、Opportunity、Harness、Submission、Ledger | 完整定稿；按依赖实现 |
| [`internal/`](internal/) | 实现线 | Go 类型/不变量、Evidence Store、PostgreSQL 迁移/仓储、Audit/Facts/State/Policy、Readers、Opportunity/Scheduler、Harness | Phase 0 完成；Phase 1–6 部分完成；**主动执行仍禁用** |

**不混用三条线。** 平台事实变化先改 `platform-audit/` 与 Evidence；领域语义变化先改 `architecture/` 与不变量；实现只消费前两者的结论，不把观察值直接写成业务规则。

## 核心模型 · Core model

```text
平台页面/公告/截图
        ↓
Evidence（原文、hash、URL、captured_at、脱敏）
        ↓
ObservedFact（平台原生字段）
        ↓
DerivedState（状态/可见性/竞争/账号能力）
        ↓
PolicyDecision（ALLOW / DENY / REQUIRE_HUMAN / REQUIRE_MORE_EVIDENCE）
```

| 概念 | 约束 |
|---|---|
| **EligibilityState** | `Decision + Gates[]`，不是单枚举；一个项目可同时需要报名、审核、NDA、银行卡、历史信誉、学生认证 |
| **AccountCapability** | 账号级门槛独立于 Program；补天合规考核未完成 → `CanSubmit=false` |
| **Scope.Visibility** | `GATED_BY_SIGNUP` / `GATED_BY_NDA` / `GATED_BY_APPROVAL` 时 contents=UNKNOWN，不能当空数组 |
| **ActionPolicy** | 评估 `Actor + Account + Program + Asset + Action + Time`；同一项目不同动作决策不同 |
| **CompetitionSnapshot** | Top1/3/10 share、duplicate_pressure、saturation 参与 Opportunity/Scheduler，不用于展示 |
| **ObservedTransport** | 先知 `openapi/cst/*`、漏洞盒子 `vapi.*`、补天 AJAX 统一为 `BROWSER_OBSERVED_API`，不是 Official API |
| **Human-only** | 报名、NDA、合规考核、实战认证、提交报告、提现、绑定收款永远人工 |

完整的 Go 类型与不变量见 [`architecture/domain-model.md`](architecture/domain-model.md)，评估顺序与动作矩阵见 [`architecture/policy-engine.md`](architecture/policy-engine.md)。

## 当前平台状态 · Platform state

| 平台 | 账号状态 | 当前可用性 | 接入顺序 |
|---|---|---|---|
| **漏洞盒子** | 实名，见习白帽，信誉 100 | 已加入联想、嘉立创两个进行中企业 SRC | 第一 |
| **先知 / ASRC** | 实名，0 战绩 | 91 个任务、34 个进行中，但当前报名 0；Scope 报名后才解锁 | 第二 |
| **补天** | 实名，实习白帽 LV1 | 本季度合规考核未完成，提交被硬阻断；专属 SRC 还需 1 个有效漏洞完成实战认证 | 第三 |

> 平台规则会变化；所有结论带 `observed_at`。审计快照见 [`platform-audit/README.md`](platform-audit/README.md)。

## 快速开始 · Quick start

本机使用 Docker PostgreSQL 16，容器 `bountygrid-pg`（`127.0.0.1:55434`，库/用户 `bountygrid`）。DSN 在 `.env.local`，已 gitignore，不提交。

```bash
docker start bountygrid-pg

cp env.example .env.local
# 填入本地开发密码；已有 .env.local 可跳过

go run ./cmd/bountygrid migrate
go run ./cmd/bountygrid ping

go test ./...
go vet ./...
go build ./...
```

Go 工具链为 **1.27.3**；本机通过 `GOTOOLCHAIN=go1.27.3` 切换，基础安装未覆盖系统目录。回退：`go env -w GOTOOLCHAIN=local`。

### 只读探针 · Read-only probes

探针不属于产品代码，不进入 Go module；只验证会话、选择器、日期与账号状态，不报名、不签 NDA、不提交、不提现。

```bash
bash probes/session/check-sessions.sh
bash probes/xianzhi/task-summary.sh
bash probes/butian/account-summary.sh
bash probes/butian/grow-dates.sh
```

探针按平台携带独立 `--state` 恢复登录态；漏洞盒子 Cookie 过期时运行 `bash probes/session/relogin.sh vulbox`。详见 [`probes/README.md`](probes/README.md)。

## 实现进度 · Implementation

> **真实状态：Phase 0 complete; Phase 1–6 partially implemented; active execution remains disabled.** 三平台 Reader 主路径已通并落库；Policy/Gateway/Opportunity/Scheduler/Harness 已有实现并在控制层收口；Verification 规则引擎已实现；ReportDraft/Submission/Ledger 仍未进入。

| 阶段 | 内容 | 状态 |
|---|---|---|
| Phase 0 | `internal/domain`、`internal/evidence`、`internal/store/postgres`（含仓储/rollback）、`cmd/bountygrid` | 完成 |
| 核心内核硬化 | `internal/audit`、`internal/facts`（含补天 GROW 抽取器）、`internal/state`、`internal/policy` | 完成（对应 canonical Phase 2/3 的提前实现） |
| Phase 1 | Browser Sidecar + Vulbox / Xianzhi / Butian Readers | 基本完成：三平台主路径已实测并落库；部分 scope/detail 与数据语义仍在收口 |
| Phase 2/3 | Facts/State/Policy | 已实现并硬化；Fact 年龄限制 State TTL，运行环境约束进入 Policy |
| Phase 4 | Opportunity / Scheduler | 已实现：append-only 评分历史、DB cooldown、CompetitionSnapshot TTL/decay |
| Phase 5 | Harness / Model Router | Harness 注册表/Gateway/沙箱/预算/执行回执已实现；Model Router 的路由/数据分级/预算/审计已实现（仅本地 dry-run，**无真实 Provider**）；**无 ACTIVE_HTTP 工具** |
| Phase 6 | Verification / Report / Submission / Ledger | Verification 规则引擎与 `verifications` 持久化已实现；ReportDraft/Submission/Ledger 仍以领域类型为主 |

### Phase 1 progress · Browser Sidecar + Vulbox Reader

已落地（只读，不点击提交/报名/NDA）：

- `internal/sidecar`：`agent-browser` 传输层，暴露 `open/wait/reload/eval/get/screenshot/state save/close`；按命令限速，不提供 click/fill。它不是安全沙箱：`Eval` 可执行任意 JS，Reader 只把它用于只读观察与分页/弹窗控制；Harness/Model 不得直接持有 Client。
- `internal/adapter/vulbox`：`AuthStatus`、`SyncAccount`（需登录）、`SyncPrograms`（公开项目列表，走页面自身的只读 GET）、`SyncProgram/SyncScope/SyncPolicy`（企业SRC子域，实测联想 444）；非企业SRC详情页仍 fail-closed 返回 `ErrUnsupported`。
- `cmd/bountygrid sync`：`-programs`（默认）、`-public-welfare`/`-grow`（仅 butian）、`-account`、`-program <id>`、`-pages`、`-page-size`、`-session`（默认 `vbprobe`）、`-raw-dir`；输出 JSON 摘要，不打印页面原文。
- 原始证据脱敏后写入 `.evidence/`（gitignored），数据库/仓库不保存页面原文。

实测（2026-10-31）：

```text
公开列表：10 个项目/页，estimated_total=1513，150 条 Fact，1 条 Evidence
账号    ：见习白帽 / exp 10 / 信誉 100 / 排名 9999+ / 实名 / CanSubmit / 已加入 2
联想 444：RUNNING / ELIGIBLE；Scope PUBLIC+GRANTED+known；include 9 条、exclude *.sunits.com；
          FileUpload=FORBIDDEN，AIAnalysis=CONDITIONAL，HumanVerificationRequired=true
```

> 用户中心页面会弹出促销抽屉遮挡个人资料。`readPage` 现在会只读关闭抽屉并等待“已实名”出现后再抽取；修复后 `identity_verified=true`、已加入 2、`CanSubmit=true`。调度器据此为联想 444 产生 `QUEUED` research task。

### Xianzhi Reader（任务列表已实测）

- `internal/adapter/xianzhi`：`AuthStatus`、`SyncAccount`、`SyncPrograms`（5 页/91 任务）、`SyncProgram`、`SyncScope`（未报名 → `GATED_BY_SIGNUP + NOT_GRANTED`）、`SyncPolicy`（保守 `UNKNOWN`）。
- 任务数据来自页面 React fiber 的 `task` 对象；状态映射实测：`4=RUNNING`、`5=ENDED`、`6=PAUSED`；类型 `1=SRC`、`2=企业众测`。实测 91 条：**34 RUNNING / 54 ENDED / 3 PAUSED**，**61 BRAND_SRC / 30 CROWD_TEST**，1058 条 Fact、5 条 Evidence，与审计完全一致。
- 翻页/重开同一 hash URL 可能是 no-op；`openList` 会 `reload` 强制重新取数。曾遇到控制台数据接口 `os error 10060` 时 SPA 只剩壳（0 卡片），reload 后恢复。
- 账号同步已跑通（`auth=OK`），但个人资料页只显示“实名认证”标签、没有文本状态，当前 `identity_verified` 保持 nil（`CanSubmit=false`，fail-closed）；需要补账号状态提取器。
- **登录不等于报名**：当前 0 个已报名任务；Scope 必须 `GATED_BY_SIGNUP`。已报名任务的可见 Scope 解析需要在本人实际报名后验证，未报名任务一律返回 GATED。

```bash
go run ./cmd/bountygrid sync -platform xianzhi -programs -pages=5
go run ./cmd/bountygrid sync -platform xianzhi -programs=false -account
```

账号与单项目同步需先人工登录：

```bash
bash probes/session/relogin.sh vulbox
go run ./cmd/bountygrid sync -account -programs -pages=1
go run ./cmd/bountygrid sync -programs=false -account=false -program 444
```

`relogin.sh` 现在故意不加载旧 state（损坏的 state 会让浏览器导航挂起），登录成功后重新保存。单机上产品 Reader 复用探针会话 `vbprobe`；其他环境用 `-session` 覆盖。`sync` 默认**不关闭**会话，避免把内存中的登录态关掉；需要清理时显式加 `-close=true`。

### 快照持久化 · `sync -persist`

- 顺序：Evidence → Facts → Program/Account；Evidence 先落库，Fact 必须带 EvidenceRef，然后才 upsert Program/Account。
- 原始页面仍只写本地 `.evidence/`；数据库只存 Evidence 元数据/hash/RawRef、Fact、Program/Account JSON。
- 幂等：Evidence/Fact ID 确定性；同 ID 同内容重跑是 no-op，Program/Account 用 upsert。新一次同步产生新 Evidence/Fact（append-only observation）。
- 实测（2026-10-31，本地 `bountygrid-pg`）：
  - Vulbox 第 1 页 50 项目 → `programs=50`、`observed_facts=750`、`evidence=1`
  - Xianzhi 5 页 91 任务 → `programs=91`、`observed_facts=1058`、`evidence=5`（54 ENDED / 34 RUNNING / 3 PAUSED）
  - Vulbox 444 详情 → Scope `PUBLIC/GRANTED`、`FileUpload=FORBIDDEN`、`AIAnalysis=CONDITIONAL`
  - 累计 `programs=141`、`observed_facts=1830`、`evidence=8`；`evidence_refs` 为空 0 条，confidence 越界 0 条。

```bash
go run ./cmd/bountygrid sync -platform vulbox -programs -pages=1 -persist
```

### 派生状态 · `derive`

- `bountygrid derive -platform vulbox|xianzhi` 从已落库 Fact 重新计算状态，写入 `derived_states`：
  - `PROGRAM_STATE` ← `DeriveProgramState`（显式 PAUSED/ENDED 优先；已过的 inclusive end 不会被陈旧的 RUNNING 覆盖）
  - `SCOPE_VISIBILITY` ← `DeriveScopeVisibility`
  - `REGISTRATION_STATE` ← `DeriveRegistrationOpen`（有 canonical 报名窗口时才写）
- 状态 ID 稳定（`state/<program>/<subject>`），`inputs` 记录 Fact ID；`derived_states.state` 为 JSONB 字符串。
- Fact supersede：PostgreSQL `SupersedeFact` 在同一事务里把 `inputs` 含该 Fact 的 State 的 `valid_until` 置为 `now()`；`inputs` 有 GIN 索引。状态不会悄悄继续使用被推翻的证据。
- 实测（2026-10-31）：Xianzhi 91 项目 → 273 状态（91/91/91），Vulbox 50 → 100 状态，合计 **373**；`inputs` 为空 0 条、confidence 越界 0 条、已过期 0 条。
  - `xianzhi/324 = RUNNING / GATED_BY_SIGNUP / registration CLOSED`
  - `vulbox/444 = RUNNING / PUBLIC`（详情同步的最新 Scope Fact 覆盖列表的 UNKNOWN）

```bash
go run ./cmd/bountygrid derive -platform xianzhi
go run ./cmd/bountygrid derive -platform vulbox
```

### Action Gateway · `decide`

- `bountygrid decide -request <json>`：从 PostgreSQL 加载 Account/Program → Policy Engine → 持久化 `ActionDecision` + `action.decision` 审计事件。缺失上下文直接产生并持久化 `DENY CONTEXT_NOT_FOUND`。
- `bountygrid decide -authorize <decision-id>`：执行前重新加载 Account/Program、重新解析 Asset 并重跑 Policy —— 必须 ALLOW、未过期、human-only 有审批；Scope/资格/自动化策略变化后旧 ALLOW 失败。带运行时约束的动作需要 `-context <json>` 提供新的 `usage_today`/`budget_spent`/`execution_environment`（后者必须带 `observed_at`，超过 5 分钟即视为未知），缺失时 fail-closed。Gateway 本身不执行任何平台动作。
- `REPORT_SUBMIT` 接 PostgreSQL 一次性审批账本；`Policy.Decide` 只校验，`Gateway.Authorize` 在重评估通过后才原子消费，重复使用在 Authorize 阶段失败。
- Decision ID 使用带分隔符的时间戳（不是 `UnixNano`）：19 位连续数字会被 secret scanner 误判为银行卡号，导致审计 payload 被拒绝。
- 实测（2026-10-31）：
  - `VIEW_RULES` → `ALLOW`，`-authorize` 返回 `authorized:true`
  - `REPORT_SUBMIT` → `DENY ACCOUNT_CANNOT_SUBMIT`
  - `ACTIVE_HTTP` 缺 Asset → `DENY ASSET_REQUIRED`
  - DB：`action_decisions=3`、`audit_events(action.decision)=3`，无任何平台动作。

```bash
go run ./cmd/bountygrid decide -request ./request.json
go run ./cmd/bountygrid decide -authorize <decision-id>
```

### Opportunity / Scheduler · `schedule`

- `internal/opportunity`：
  - `E[reward] = P(valid) × (0.25·min + 0.5·median + 0.25·max) × tier × campaign × (1 − duplicate)`
  - `penalty = 0.4·top10_share + 0.35·duplicate + 0.25·saturation`；缺 CompetitionSnapshot 时用保守 0.5/0.5/0.5，并把 Confidence 降到 0.2。
  - `score = E[reward] × (1 − penalty) / max(cost,1) × confidence`；`Inputs[]` 记录 p_valid/权重/竞争/成本/置信度。**不按最高赏金排序。**
- `Scheduler`：硬过滤 `RUNNING` + cooldown；`CanSubmit=false` / `Eligibility != ELIGIBLE` / `Scope.Access != GRANTED` 只进 HumanActionQueue，不进入执行队列；Top-K、每平台/每项目配额、企业SRC保留位；产出 `ResearchTask` 状态 `QUEUED` 或 `HUMAN_ACTION_REQUIRED`，**没有 Harness 执行**。
- 实测（2026-10-31）：Vulbox 9 个 RUNNING 可评分程序 → **1 research（联想 444，`QUEUED`）/ 8 human（`PROGRAM_GATE`，未加入）**；40 个 PAUSED/ENDED 被排除、1 个无赏金数据。Butian 34 human（`ACCOUNT_CAPABILITY`，合规考核/实战认证）、Xianzhi 3 human。DB `research_tasks` = 1 `QUEUED` + 45 `HUMAN_ACTION_REQUIRED`，无平台动作。账号能力修复前 Vulbox 是 0 research / 9 human，见下。
- **登录态 ≠ 资格**：三个浏览器会话都登录着，但 Xianzhi 需要实际报名才解锁 Scope，Butian 需要合规考核/实战认证，Vulbox 个人中心被促销抽屉遮挡（已修复）。账号门槛与页面授权永远不由“已登录”推断。

```bash
go run ./cmd/bountygrid schedule -platform vulbox -top 5 -persist
```

### CompetitionSnapshot · `competition`

- `bountygrid competition -file snapshot.json -persist`：校验 platform/scope/share/confidence；生成带 Evidence 的快照（`ManualImport`，原文脱敏到 `.evidence/competition/`），写入 `competition_snapshots`。
- 已落库三份审计快照（2026-10-31）：

```text
xianzhi  top10=0.708  active=28 rewarded=27 pool=14,680  confidence=0.7
vulbox   top10=0.500* active=50 rewarded=30 pool=30,000  confidence=0.5
butian   top10=0.500* active=30 rewarded=10 pool=—       confidence=0.5
* 页面没有直接给出 top10 share，按 0.5 保守估计并在 Evidence 文本里注明。
```

- 重新调度后：Vulbox 9 个 human（confidence 0.5、penalty 0.645），Xianzhi 3 个 human（confidence 0.7、penalty 0.608）；`opportunities=12`、`research_tasks=12 HUMAN_ACTION_REQUIRED`。仍无 research，因为账号能力未观测（fail-closed）。
- `PutOpportunity` 冲突更新现在刷新全部反规范化列（expected_return/per_cost/penalty/confidence/evaluated_at/data），避免“JSON 已更新、列还是旧值”。

### Harness / Tool Registry · `harness`

- 每个工具声明 `RequiredAction`；Harness 用工具的动作覆盖请求，强制 `Action Gateway Evaluate → ALLOW → Authorize → Execute`。`DENY/REQUIRE_HUMAN` 直接停止，工具不会被调用。
- 沙箱：默认拒绝所有 egress，`-allowed-hosts` 支持 `*.example.org`（只匹配子域，不匹配 apex）；`http/https` 以外 scheme 拒绝。
- 预算：每次执行前检查 `-budget`（CNY）与请求数；超限不执行。`Destructive` 工具默认禁用，需显式 `-allow-destructive`。
- 内置工具仅 `view_rules`、`read_scope`、`report_draft`（本地只读，不发起网络/扫描/提交）。本仓库仍不进行任何真实测试。
- 实测（2026-10-31）：
  - `view_rules` → ALLOW → Authorize → 输出 `read-only view_rules completed`
  - 缺失账号 → DENY `CONTEXT_NOT_FOUND`，工具未执行
  - DB：`action_decisions=5`、`tool.executed=1`

```bash
go run ./cmd/bountygrid harness -tool view_rules -request ./request.json
```

### Butian Reader（专属SRC / 公益SRC / GROW 已实测）

- `internal/adapter/butian`：`AuthStatus`、`SyncAccount`（合规考核/实战认证/实名/银行卡，保守 fail-closed）、`SyncPrograms`（37 专属SRC）、`SyncProgram`、`SyncScope`（未报名 GATED）、`SyncPolicy`（保守 `UNKNOWN`）。
- 专属SRC 列表从已渲染 DOM 的 `.plan-exclusive-src .company-card` 读取（不调用被验证码保护的 `/Reward/corps`）；实测 **37/37，259 Fact，1 Evidence**，全部 `RUNNING / PRIVATE_EXCLUSIVE / HUMAN_ACTION_REQUIRED`（报名 + 实战认证双门槛）。
- 列表卡片没有原生 company ID，ProgramID 用公司名 SHA-1 前缀（`butian/src/<hex>`）；后续拿到原生 ID 时再迁移。
- 账号同步已成功（`auth=OK`）：账号 `CanSubmit=false`（本季度合规考核未完成；专属SRC还需实战认证），已落库。之前的账号中心导航 `os error 10060` 是瞬时浏览器连接失败，重试后成功；登录态一直在。
- 37 专属SRC 已落库（178 项目含 Vulbox 50 + Xianzhi 91）；`derive` 产出 74 个 Butian states；调度 34 human（`ACCOUNT_CAPABILITY`）、3 个无赏金数据。Xianzhi 同理：91 任务只读成功，但 0 个已报名，Scope 必须 GATED。
- 公益SRC（`-public-welfare`）：走页面自身的只读 GET `/Reward/pub?p=N`（需 `X-Requested-With: XMLHttpRequest`，否则平台返回 HTML 壳）；`p` 才是分页参数（`page` 被忽略）。实测 **200 页、30 条/页、末页 9 条 = 5,979 家**；ProgramID `butian/pub/<company_id>`，状态 `RUNNING`（confidence 0.8，原因是“在公益列表中”），**Scope 保持 `UNKNOWN`**。
- GROW（`-grow`）：打开 `/Grow` 点“查看规则”取报名/活动日期，再打开 `/Active/center` 取月度阶段；两页分别是独立 Evidence。实测 6 条 Fact（报名 8/25–11/30、活动 9/1–11/30、第二期 10/01–10/31）、`RUNNING / HUMAN_ACTION_REQUIRED`（报名 + 学生认证 + 校园团队 3 个 Gate）、Scope `GATED_BY_SIGNUP + NOT_GRANTED`。
- 月度阶段正则锚定在 GROW 标题后，活动中心里排在前面别的活动日期不会被误当成 GROW 阶段（`docs/advices/2026-10-31-phase-0-1-second-review.md` 第 6 条）。

```bash
# 用户需先在 btprobe 浏览器完成验证码，使项目大厅渲染 37 张卡
go run ./cmd/bountygrid sync -platform butian -programs
# 公益SRC 第 1 页（-pages 最多 200）
go run ./cmd/bountygrid sync -platform butian -programs=false -public-welfare -pages 1
# GROW 校园计划
go run ./cmd/bountygrid sync -platform butian -programs=false -grow
```

### Phase 0 + core kernel hardening

本轮已修复的 P1/P2：

- Policy 只在参与类动作评估 Eligibility；`VIEW_PROGRAM/VIEW_RULES` 不再被未报名/未达标拦截
- `READ_SCOPE` 从执行类动作中拆出：GATED 时 `REQUIRE_HUMAN`，已解锁时 `ALLOW`
- Scope 拆成 `Visibility`（披露）与 `Access`（当前账号访问）；报名后可为 `GATED_BY_SIGNUP + GRANTED`
- Policy 增加 `Asset` 校验：缺失、跨 Program、Scope 外、Conditional 分别处理
- Policy 消费 `AutomationPolicy`：`FORBIDDEN` 不可被人工审批覆盖；`ALLOW` 可覆盖默认条件矩阵
- `HumanApproval` 绑定 `request_id/action/program/asset/resource/parameters_hash/expires/single_use`，并由 PostgreSQL `consumed_approvals` 持久化一次性消费（2026-11-01 起消费点移到 `Gateway.Authorize` 的重评估之后）
- `PRACTICAL_CERTIFICATION` 改为 Program-scoped，不再阻断普通/公益提交
- State 保留输入 Fact/Evidence provenance，并提供 supersede invalidation tracker
- GROW 日期按 `Asia/Shanghai`、date-only、inclusive end 处理
- Evidence/Audit 深拷贝 + 脱敏/secret scan；Fact Store batch atomic + latest + supersede 校验
- PostgreSQL 增加 repositories、`0002_constraints.sql` 与 down migration / `rollback` CLI
- GitHub Actions CI：migrate + unit/integration test + vet + build

### Phase 0/1 second review hardening

- Policy 校验决策上下文与请求一致：`AccountID/ProgramID/Platform` 不一致直接 `DENY`；`Asset.ID` 必须等于请求 `AssetID`，且资产必须属于该 Program
- `AutomationPolicy=CONDITIONAL` 只增加人工门槛，不能把硬拒绝（DoS/内网/横向）降为可审批动作
- `ALLOW` 的 `ExpiresAt` 收敛到 `Program.Status.ValidUntil` 与 `HumanApproval.ExpiresAt` 的较早者；审批账本故障向上返回错误，而不是伪装成“已使用”
- `Scope.CheckAsset` 改为 fail-closed：`ContentsKnown=true` 但 `Include` 为空时不再放行任意资产；`*.example.com` 不再匹配 apex `example.com`
- 执行类决策要求 `Program.Status.Confidence >= 0.8`；State 的置信度来自输入 Fact 的最小值，不再硬编码 0.9
- 约束配置（daily limit / budget cap）非法时 fail-closed；预算币种不一致直接 `DENY`
- Fact 重跑幂等：同 ID 同内容为 no-op，同 ID 不同内容报错；supersede 必须保持 subject/predicate/实体一致，PostgreSQL 增加自环与环检测
- Repository 写入前执行 Domain `Validate`；PostgreSQL AuditStore 与内存实现一致地拒绝含敏感值的 payload；DerivedState 冲突更新刷新全部派生元数据
- `DerivationTracker` 重新记录同一 State 时迁移依赖，避免旧 Fact 误失效当前 State
- Domain 不变量收紧：Fact/State 必须有 ID；`ALLOW` 必须带 `ExpiresAt`；human-only 审批必须 single-use；Program-scoped gate 不再使账号级 `CanSubmit` 失效

### Control-plane hardening (2026-11-01)

- **Asset 真相源**：调用方只能给 `AssetID`；Gateway 从 `Program.Scope.ResolveAsset` 解析 canonical Asset，未存储的 ID 直接 `DENY ASSET_UNKNOWN`。
- **Constraints 执行化**：`ExecutionEnvironment` 进入 Policy，校验时间窗、RPS、并发、VPN、出口 IP、研究员 Header、测试账号与 HTTP 方法；缺运行时证明 → `REQUIRE_MORE_EVIDENCE`，观察到的违规 → `DENY`。
- **Sandbox/Egress 强制**：网络工具只拿到 `harness.Egress`，每次请求与重定向都走 `Sandbox.CheckURL`；空 allowlist 的网络工具直接拒绝。
- **Authorize 重新评估**：Decision 持久化原始 Request；`Authorize` 重新加载 Account/Program、重新解析 Asset 并重跑 Policy，Scope/资格/自动化策略变化后旧 ALLOW 不再可执行。
- **审批消费时点**：`Policy.Decide` 只校验不消费；`Gateway.Authorize` 在重新评估通过后原子消费 single-use approval。
- **幂等执行回执**：`NetworkEgress`/`WritesFiles`/`Destructive` 工具必须有 `IdempotencyKey`；`execution_receipts` 在工具执行前预占 key，重试回放已记录结果，审计失败可在不重复执行的情况下补齐。
- **Decision + Audit 事务**：PostgreSQL `RecordDecisionWithAudit` 在同一个事务写入决策与 `action.decision` 事件。
- **Fact 年龄限制 State**：`DerivedState.ValidUntil` 取 `now+TTL` 与最旧输入 Fact 的 `observed_at+30d` 的较早者；过期 Fact 不能生成新 State。
- **CompetitionSnapshot TTL/decay**：`valid_until` 30 天硬上限；7 天后置信度线性衰减；`top1 <= top3 <= top10` 成为导入不变量。
- **append-only 历史**：ResearchTask/机会评分使用每轮唯一 ID；同一 Program 最多一个 `QUEUED` 任务，被新任务 supersede；Scheduler 从数据库读取 cooldown。
- **Butian 语义层次**：公益SRC 只发 `butian.pub.listed`，RUNNING 是派生状态；专属SRC/GROW 的报名与实战认证 Gate 不再写死 `UNSATISFIED`，由 Policy 结合账号状态解析。
- **文档对齐**：CLAUDE/README/implementation-order 的阶段状态改为“Phase 0 complete; Phase 1–6 partially implemented; active execution disabled”。
- **复核补修**：公益SRC 由 canonical `program.listed` 派生 RUNNING；回执绑定请求身份并按状态处理竞态；`AuthorizeWith` 要求新的 usage/budget/environment（环境 5 分钟过期）；Scheduler 使用账号重算的 Eligibility；列表页显式报名信号写入 `Account.JoinedProgramIDs`；审批参数保留原始字节并接受 canonical hash；回执保留 Evidence。

### Model Router（2026-11-01）

- `internal/model`：`Purpose`/`DataClass`/`ProviderInfo`/`Router`；`SENSITIVE` 只允许本地 Provider，无本地 Provider 直接 `ErrNoProvider`。
- PLAN/ANALYZE 优先强远程模型，CODE/EMBED/REPORT 优先本地；输出 token 上限不足的 Provider 不参与。
- 调用前按 `MaxTokens` 估算成本，调用后按实际 usage 复核；超预算丢弃响应并返回 `ErrBudgetExceeded`。
- 每次尝试写 `model.call` 审计，payload 只含 `prompt_hash/response_hash/usage/cost`，不含原文。
- `go run ./cmd/bountygrid model -purpose ANALYZE -data-class INTERNAL -max-tokens 2000 -budget 100` 只打印路由计划，不调用模型。

### Verification（2026-11-01）

- `internal/verify.RuleVerifier`：Scope（`ResolveAsset` + `CheckAsset`）→ 授权决策（必须 ALLOW、Request context 完整、动作属于执行类）→ 数据最小化/公开已知 → 重复检查 → AI 人工验证 → 复现/影响证据。
- `VERIFIED` 必须 `Reproduced/InScope/ImpactVerified/DataMinimized` 全真且有 Evidence；模型不能单独判 VERIFIED。
- `go run ./cmd/bountygrid verify -file <json> -persist` 从 PostgreSQL 加载 Program/Decision，写 `verifications`。
- 未实现：Finding 状态机自动推进、ReportDraft AIDisclosure、人工复核 UI。

## 文档入口 · Docs

| 文档 | 它约束什么 |
|---|---|
| [`platform-audit/README.md`](platform-audit/README.md) | 三平台账号状态、可参与范围、自动化禁区与接入建议 |
| [`platform-audit/CORRECTIONS.md`](platform-audit/CORRECTIONS.md) | 审计纠正记录（GROW 日期、经验值、项目计数、Eligibility 模型） |
| [`platform-audit/comparison/unified-program-model.json`](platform-audit/comparison/unified-program-model.json) | 统一领域模型 schema 2.0 |
| [`architecture/README.md`](architecture/README.md) | 组件图、数据流、技术形态、包布局、环境分工 |
| [`architecture/domain-model.md`](architecture/domain-model.md) | Go 类型、聚合关系、不变量 |
| [`architecture/evidence-facts.md`](architecture/evidence-facts.md) | Evidence → Fact → State → Decision、TTL、置信度 |
| [`architecture/adapters.md`](architecture/adapters.md) | 三平台 Adapter、Transport、轮询、失败模式 |
| [`architecture/policy-engine.md`](architecture/policy-engine.md) | AccountCapability + EligibilityState + 动作矩阵 |
| [`architecture/opportunity-scheduler.md`](architecture/opportunity-scheduler.md) | CompetitionSnapshot、期望收益/成本、调度 |
| [`architecture/harness-model-router.md`](architecture/harness-model-router.md) | 执行边界、工具注册、模型路由 |
| [`architecture/research-lifecycle.md`](architecture/research-lifecycle.md) | Finding/Verification/Report/Submission/Ledger |
| [`architecture/implementation-order.md`](architecture/implementation-order.md) | 依赖顺序实现计划（非 MVP 切分） |
| [`docs/advices/2026-10-31-phase-0-1-hardening.md`](docs/advices/2026-10-31-phase-0-1-hardening.md) | 对复核意见的逐项修复记录 |
| [`docs/advices/2026-10-31-phase-0-1-second-review.md`](docs/advices/2026-10-31-phase-0-1-second-review.md) | 第二次复核结论、已修问题与遗留项 |
| [`docs/advices/2026-11-01-phase-1-5-high-review.md`](docs/advices/2026-11-01-phase-1-5-high-review.md) | Phase 1–5 high review 的 11 项修复与验证 |
| [`docs/advices/2026-11-01-control-plane-hardening.md`](docs/advices/2026-11-01-control-plane-hardening.md) | 控制层收口：authoritative Asset、运行时约束、Egress、Authorize 重评估、执行回执、状态/竞争 TTL、Butian 语义 |
| [`CLAUDE.md`](CLAUDE.md) | 编码代理的运行约定与仓库红线 |

## 工程原则 · Engineering rules

> BountyGrid 的底线是：**没有证据的状态不存在，没有授权的动作不执行，没有人工批准的提交不发生。**

| 规则 | 要求 |
|---|---|
| 事实先于判断 | 每个状态/日期必须能回溯到 Evidence；没有 `observed_at/confidence/rule_version` 的 State 不参与决策 |
| Eligibility 是 Gates | 禁止把多门槛压成单枚举；账号级门槛放 `AccountCapability`，项目级门槛放 `Program.Gates` |
| Scope 不降级 | `GATED_*` 的 Scope 不得返回空数组；必须显式处理 `UNKNOWN` |
| 动作级授权 | Policy Engine 按动作决策；`ACTIVE_HTTP` 与 `REPORT_SUBMIT` 不是同一个权限 |
| 人工门槛 | 报名、NDA、合规考核、实战认证、提交、提现、绑卡永远 `REQUIRE_HUMAN` |
| 内部接口不是 API | 三平台前端接口统一为 `BROWSER_OBSERVED_API`；不绕过认证、不改请求 |
| 不测试真实资产 | 本仓库不产生 Payload、不发起扫描/爆破/内网渗透/DoS；执行层只做被批准的动作 |
| 不提交敏感数据 | `.env.local`、截图、浏览器 state、Cookie、手机号/邮箱/身份证/银行卡都不入库 |
| 迁移可回滚 | 数据库变更必须版本化；破坏性变更先备份并写回滚路径 |
| 文档同步 | 改 Domain/Policy/Adapter 时同步更新 `architecture/` 与不变量测试 |

## 开发与合并流程

- 从干净的 `main` 切短期分支（`feat/*`、`fix/*`、`docs/*`、`probe/*`）。
- 提交前至少运行：`go test ./...`、`go vet ./...`、`go build ./...`；数据库变更再跑 `go run ./cmd/bountygrid migrate`。
- 验证后以非快进方式合入 `main` 并推送；不改写或 force-push 共享历史。
- 探针改动按 `probe/*` 分支处理；探针不得进入 `internal/` 或改变产品行为。
- 平台事实变化先更新 `platform-audit/`，再改架构与实现；不要把某次页面观察直接写死为领域规则。

## 目录结构 · Layout

```text
cmd/bountygrid/              migrate / rollback / ping / sync / version 入口
internal/domain/             平台/账号/项目/Scope/Eligibility/Action/Research/Ledger 类型与不变量
internal/evidence/           不可变 Evidence Store（接口 + 内存实现）
internal/audit/              审计事件（不可变、可过滤）
internal/facts/              抽取器接口、FactSet、内存 Store、补天 GROW 抽取器
internal/state/              程序状态/报名窗口/Scope/账号能力派生
internal/policy/             Eligibility 合并 + 动作矩阵 + 人工审批
internal/sidecar/            agent-browser 只读客户端（open/wait/eval/get/state save/close）
internal/adapter/            Reader 接口 + Vulbox Reader（公开列表/账号；详情待接入）
internal/store/postgres/     pgxpool、嵌入式迁移、仓储与一次性审批账本
platform-audit/              三平台账号/项目/规则/收款/自动化边界审计与证据
architecture/                完整 Go 架构：Domain/Evidence/Adapter/Policy/Opportunity/Harness/Submission/Ledger
probes/                      本机只读探针（不属于产品代码）
env.example                  本地环境变量模板（DSN 占位）
```

## 构建与维护 · Built by

<div align="center">

<p>由 <strong>Zn070515</strong> 维护。</p>

<p><sub>Maintained by <strong>Zn070515</strong>.</sub></p>

</div>
