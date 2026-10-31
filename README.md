# BountyGrid

漏洞赏金统一控制层（Go）。当前阶段：**平台配置审计完成 + 完整架构设计定稿**，尚未开始真实资产测试。

- GitHub（public）：https://github.com/Zn070515/BountyGrid
- 本地仓库：`~/Desktop/BountyGrid`（`main` 跟踪 `origin/main`）

## 目录

```text
platform-audit/     三平台账号/项目/规则/收款/自动化边界审计
  README.md         总览与账号状态
  CORRECTIONS.md    审计纠正记录（GROW 日期、经验值、计数、Eligibility）
  xianzhi/ vulbox/ butian/
  comparison/       能力矩阵、统一模型、Adapter 设计、横向发现

architecture/       完整 Go 架构设计（一次定稿，按依赖实现）
  README.md         组件图/数据流/技术形态/包布局
  domain-model.md   Go 类型与不变量
  evidence-facts.md Evidence → Fact → State → Decision
  adapters.md       三平台 Adapter/Transport
  policy-engine.md  AccountCapability + EligibilityState + 动作矩阵
  opportunity-scheduler.md CompetitionSnapshot + 期望收益/成本
  harness-model-router.md 执行边界与模型路由
  research-lifecycle.md Finding/Verification/Submission/Ledger
  implementation-order.md 依赖顺序实现计划
```

## 当前账号状态（2026-10-31）

| 平台 | 状态 | 可用性 |
|------|------|--------|
| 漏洞盒子 | 实名，见习白帽，信誉100 | 已加入联想、嘉立创企业SRC，可直接提交 |
| 先知 | 实名，0战绩 | 91任务可见，0已报名；Scope 报名后解锁 |
| 补天 | 实名，实习白帽LV1 | 合规考核未完成，提交硬阻断；专属SRC需实战认证 |

## 设计原则（摘要）

- `Eligibility = Decision + Gates[]`，不是单枚举。
- 账号级门槛（补天合规考核）归 `AccountCapability`，不归 Program。
- `Evidence → ObservedFact → DerivedState → PolicyDecision`；状态带 observed_at/confidence。
- `Scope.Visibility != PUBLIC` 时 contents=UNKNOWN，不是空数组。
- Policy Engine 评估 `Actor + Account + Program + Asset + Action + Time`。
- 内部前端接口统一为 `BROWSER_OBSERVED_API`，不是 OfficialAPI。
- CompetitionSnapshot 参与 Opportunity/Scheduler，不用“最高赏金”排序。
- 报名/NDA/合规考核/实战认证/提交/提现永远人工；自动化上限 LEVEL 4。

## Go 环境

- 当前工具链：`go1.27.3 windows/amd64`（通过 `GOTOOLCHAIN=go1.27.3` 切换；`go env GOROOT` 指向工具链缓存）
- 回退：`go env -w GOTOOLCHAIN=local`
- 说明：基础安装位于 `D:\Program Files`（无管理员写权限），未覆盖系统目录。

## 本地开发（Phase 0 已落地）

已实现：

- `internal/domain`：平台/账号/项目/Scope/Eligibility/Action/Competition/Finding/Ledger 类型与不变量
- `internal/evidence`：不可变 Evidence Store 接口 + 内存实现
- `internal/store/postgres`：pgxpool 连接 + 嵌入式 SQL 迁移
- `cmd/bountygrid`：`migrate` / `ping` / `version`
- 单元测试覆盖 Scope GATED、Eligibility 决策一致性、补天合规考核 `CanSubmit`、人工-only Action、提交审批、负金额账本、Fact 证据引用

PostgreSQL（本机 Docker）：

```text
container: bountygrid-pg
image:     postgres:16-alpine
dsn:       postgres://bountygrid:***@127.0.0.1:55434/bountygrid?sslmode=disable
```

连接串在 `.env.local`（gitignored，不提交）。首次：

```bash
cp env.example .env.local   # 填入本地密码（或使用已有 .env.local）
go run ./cmd/bountygrid migrate
go run ./cmd/bountygrid ping
go test ./...
```

## 环境分工

| 环境 | 职责 |
|------|------|
| 本机（当前 Windows） | 讨论、审计、**只读探针**；**已可开发**（Phase 0 已跑通） |
| 台式机 | BountyGrid 开发（可选/并行），通过 GitHub 同步 |
| 存储 | **PostgreSQL 单机**、单用户、单库单 schema |
| 部署 | 模块化单体，一个 Go 二进制 + 本机 PostgreSQL |

探针目录：`probes/`。探针是可丢弃的只读脚本，不属于产品代码，也不进入 Go module。

## 下一步

按 `architecture/implementation-order.md` 继续：

1. `internal/audit`（审计事件）
2. `internal/facts` + `internal/state`（抽取器与状态派生）
3. `internal/policy`（Eligibility + Action Matrix）
4. Vulbox Reader（第一个 Adapter）
5. Opportunity/Scheduler、Harness、Verification、Submission、Ledger
