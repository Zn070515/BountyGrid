<div align="center">

<h1>BountyGrid</h1>

<p><strong>漏洞赏金统一控制层</strong> · <strong>Bug bounty control plane for Xianzhi, Vulbox and Butian</strong></p>

<p>
  <a href="https://github.com/Zn070515/BountyGrid/actions/workflows/ci.yml"><img src="https://github.com/Zn070515/BountyGrid/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI status"></a>
  <img src="https://img.shields.io/badge/Go-1.27.3-00ADD8?logo=go&logoColor=white" alt="Go 1.27.3">
  <img src="https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white" alt="PostgreSQL 16">
  <img src="https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white" alt="Docker">
  <img src="https://img.shields.io/badge/Phase-0%20%2B%20kernel%20hardening-blue" alt="Phase 0 + kernel hardening">
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
| [`internal/`](internal/) | 实现线 | Go 类型/不变量、Evidence Store、PostgreSQL 迁移/仓储、Audit/Facts/State/Policy | Phase 0 完成；核心内核硬化完成；Phase 1 Readers 未开始 |

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

| 阶段 | 内容 | 状态 |
|---|---|---|
| Phase 0 | `internal/domain`、`internal/evidence`、`internal/store/postgres`（含仓储/rollback）、`cmd/bountygrid` | 完成 |
| 核心内核硬化 | `internal/audit`、`internal/facts`（含补天 GROW 抽取器）、`internal/state`、`internal/policy` | 完成（对应 canonical Phase 2/3 的提前实现） |
| Phase 1 | Browser Sidecar + Vulbox / Xianzhi / Butian Readers | 未开始 |
| Phase 2/3 | Facts/State/Policy | 已提前硬化；Reader 接入后回填真实 Fact |
| Phase 4 | Opportunity / Scheduler | 下一步 |
| Phase 5+ | Harness / Model Router / Verification / Submission / Ledger | 规划中 |

### Phase 0 + core kernel hardening

本轮已修复的 P1/P2：

- Policy 只在参与类动作评估 Eligibility；`VIEW_PROGRAM/VIEW_RULES` 不再被未报名/未达标拦截
- `READ_SCOPE` 从执行类动作中拆出：GATED 时 `REQUIRE_HUMAN`，已解锁时 `ALLOW`
- Scope 拆成 `Visibility`（披露）与 `Access`（当前账号访问）；报名后可为 `GATED_BY_SIGNUP + GRANTED`
- Policy 增加 `Asset` 校验：缺失、跨 Program、Scope 外、Conditional 分别处理
- Policy 消费 `AutomationPolicy`：`FORBIDDEN` 不可被人工审批覆盖；`ALLOW` 可覆盖默认条件矩阵
- `HumanApproval` 绑定 `request_id/action/program/asset/resource/parameters_hash/expires/single_use`，并由 `ApprovalLedger` 一次性消费
- `PRACTICAL_CERTIFICATION` 改为 Program-scoped，不再阻断普通/公益提交
- State 保留输入 Fact/Evidence provenance，并提供 supersede invalidation tracker
- GROW 日期按 `Asia/Shanghai`、date-only、inclusive end 处理
- Evidence/Audit 深拷贝 + 脱敏/secret scan；Fact Store batch atomic + latest + supersede 校验
- PostgreSQL 增加 repositories、`0002_constraints.sql` 与 down migration / `rollback` CLI
- GitHub Actions CI：migrate + unit/integration test + vet + build

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
cmd/bountygrid/              migrate / ping / version 入口
internal/domain/             平台/账号/项目/Scope/Eligibility/Action/Research/Ledger 类型与不变量
internal/evidence/           不可变 Evidence Store（接口 + 内存实现）
internal/audit/              审计事件（不可变、可过滤）
internal/facts/              抽取器接口、FactSet、内存 Store、补天 GROW 抽取器
internal/state/              程序状态/报名窗口/Scope/账号能力派生
internal/policy/             Eligibility 合并 + 动作矩阵 + 人工审批
internal/store/postgres/     pgxpool、嵌入式迁移（Postgres 仓储实现待 Phase 2）
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
