# BountyGrid 完整架构设计

- 版本：0.1（一次定完整模型，后续按依赖实现）
- 日期：2026-10-31
- 输入：`platform-audit/` 三平台审计 + 用户复核意见
- 定位：漏洞赏金统一控制层。Go 负责事实、状态、策略、调度、证据、验证、提交审批与收益账本；Harness/Agent/模型只执行被批准的动作。

## 1. 设计公理

1. **平台原生事实优先**：先在底层保存平台自己的字段与原文，再做 Normalization。
2. **事实与判断分离**：`Evidence → ObservedFact → DerivedState → PolicyDecision`；任何状态都带 `observed_at / source / confidence / rule_version`。
3. **Eligibility 不是枚举**：`Decision + Gates[]`；一个项目可同时需要报名、审核、NDA、银行卡、历史信誉、学生认证。
4. **账号级门槛独立**：补天合规考核是 `AccountCapability.CanSubmit=false`，不是某个 Program 的标签。
5. **Scope 的 UNKNOWN/GATED 不是空数组**：未解锁就是 `contents=UNKNOWN`，不能当成“无资产”。
6. **动作级策略**：同一项目，`VIEW_RULES` 与 `ACTIVE_HTTP`、`REPORT_SUBMIT` 的决策不同。
7. **执行层无策略权**：Harness/模型只能提交 `ActionRequest`，Go 返回 `ActionDecision`。
8. **人工不可绕过的动作**：报名、NDA、合规考核、实战认证、绑定收款、提交报告、提现。
9. **竞争数据参与调度**：`CompetitionSnapshot` 进入 Opportunity Engine，不是展示字段。
10. **内部接口不是官方 API**：三平台前端接口统一归类为 `BROWSER_OBSERVED_API`。

## 2. 组件图

```text
                    ┌─────────────────────────────┐
                    │        HTTP / CLI UI         │
                    └──────────────┬──────────────┘
                                   │
┌──────────────────────────────────▼──────────────────────────────────┐
│                        Go Control Plane                              │
│                                                                      │
│  ┌─────────────┐   ┌──────────────┐   ┌──────────────┐               │
│  │  Evidence   │──▶│ Fact Extractor│──▶│ State Deriver │               │
│  │   Store     │   │ (per platform)│   │              │               │
│  └─────────────┘   └──────────────┘   └──────┬───────┘               │
│                                               │                       │
│  ┌─────────────┐   ┌──────────────┐   ┌──────▼───────┐               │
│  │  Domain     │◀──│   Adapters   │   │  Policy      │               │
│  │  Store      │   │ 3 platforms  │   │  Engine      │               │
│  └──────┬──────┘   └──────────────┘   └──────┬───────┘               │
│         │                                     │                       │
│  ┌──────▼───────┐   ┌──────────────┐   ┌──────▼───────┐               │
│  │ Opportunity  │──▶│  Scheduler   │──▶│  Action      │               │
│  │  Engine      │   │              │   │  Gateway     │               │
│  └──────────────┘   └──────────────┘   └──────┬───────┘               │
│                                               │                       │
│  ┌─────────────┐   ┌──────────────┐   ┌──────▼───────┐               │
│  │  Revenue    │◀──│  Submission  │◀──│ Verification │               │
│  │  Ledger     │   │  Service     │   │  Engine      │               │
│  └─────────────┘   └──────────────┘   └──────▲───────┘               │
│                                               │                       │
│  ┌─────────────┐   ┌──────────────┐   ┌──────┴───────┐               │
│  │  Audit Log  │◀──│ Model Router │◀──│   Harness    │               │
│  └─────────────┘   └──────────────┘   └──────────────┘               │
└──────────────────────────────────────────────────────────────────────┘
```

## 3. 数据流

```text
平台页面/公告/截图/接口
        │  (BROWSER_PAGE / BROWSER_OBSERVED_API / MANUAL_IMPORT)
        ▼
Evidence Store（原文、hash、URL、captured_at、脱敏）
        ▼
Fact Extractor（版本化抽取器）
        ▼
ObservedFact（平台原生字段）
        ▼
State Deriver（状态/可见性/竞争/账号能力）
        ▼
Domain Store（Account/Program/Scope/Policy/Competition）
        ▼
Policy Engine（ActionRequest → ActionDecision）
        ▼
Scheduler → Harness → Model Router → Finding
        ▼
Verification → ReportDraft → Human Approval → Submission
        ▼
Revenue Ledger → Opportunity Engine 反馈
```

## 4. 推荐技术形态

- **部署形态**：**单机、单用户、模块化单体**。一个 Go 二进制 + 本机 PostgreSQL；不拆微服务、不做多租户。
- **模块化单体**：按包边界隔离；状态强一致、审计链短。
- **存储**：**PostgreSQL（单机）**。关系表存实体/账本/审批；JSONB 存平台原生事实、证据元数据、策略快照。
- **任务队列**：PostgreSQL `FOR UPDATE SKIP LOCKED`；单机不需要额外消息中间件。
- **浏览器**：Playwright/CDP sidecar（可复用 agent-browser），与 Go 进程隔离；Adapter 只通过受控 RPC 调用。
- **模型**：Model Router 接口；本地 Ollama/llama.cpp，云端 Anthropic/OpenAI-compatible；模型无工具直连权限。
- **可观测**：OpenTelemetry + 结构化日志；每个决策写审计事件。
- **密钥**：OS keychain / 加密 vault；不写 DB、不写日志、不进审计 JSON。

### 4.1 环境分工

| 环境 | 用途 | 允许做什么 |
|------|------|------------|
| 本机（当前 Windows） | 讨论、审计、**只读探针**；代码同步到 GitHub 后**也可在本机开发** | 读页面/公告/API 结构；写探针；Go 模块/PostgreSQL/测试 |
| 台式机 | BountyGrid 开发（可选/并行） | 通过 GitHub 同步同一仓库 |
| 生产运行（未来） | 单机部署 | 单用户控制层 + 本地 PostgreSQL |

探针不是产品代码：它们是可丢弃的只读脚本，用来验证平台事实、选择器、日期解析、会话状态。探针不得报名/签NDA/提交/提现。

### 4.2 PostgreSQL 单机约定

```text
DSN: postgres://bountygrid@localhost:5432/bountygrid?sslmode=disable
schema: 单库单 schema（public）
迁移: 版本化 SQL，可回滚
备份: 本机 pg_dump + 加密存储
并发: 单进程 worker + PostgreSQL 租约
```

- 不做读写分离、不做分库分表。
- 审计表只追加；账本只追加。
- 证据大文件放本地加密目录/对象存储，DB 只存引用与 hash。
- 单用户不等于无 Actor：Human/Harness/Model 仍分别记录，便于审计。

## 5. 包布局

```text
cmd/bountygrid/                  # 主进程
internal/domain/                 # 纯类型/不变量，无外部依赖
internal/evidence/               # 证据存储、脱敏、hash
internal/facts/                  # 抽取器接口 + 三平台实现
internal/state/                  # DerivedState、状态派生规则
internal/adapters/
    common/                      # Transport、Session、RateLimiter
    xianzhi/
    vulbox/
    butian/
internal/policy/                 # Eligibility + ActionPolicy + Decision
internal/opportunity/            # CompetitionSnapshot + Opportunity Engine
internal/scheduler/              # 任务选择、配额、冷却
internal/action/                 # ActionRequest/ActionDecision 网关
internal/harness/                # 执行边界（不含策略）
internal/modelrouter/            # 模型路由
internal/verification/           # 复现/范围/影响/数据最小化/重复风险
internal/submission/             # 报告组装、人工审批、提交
internal/ledger/                 # 收益账本
internal/store/postgres/         # 仓储实现
internal/audit/                  # 审计事件
internal/httpapi/                # REST/SSE（如需要）
```

依赖规则：`domain` 不依赖任何内部包；`adapters/policy/opportunity/scheduler/...` 都依赖 `domain`；`httpapi` 只依赖用例层；禁止 adapter 直接写 domain 表（必须经 store/用例）。

## 6. 文档索引

| 文档 | 内容 |
|------|------|
| `domain-model.md` | Go 类型、不变量、实体关系 |
| `evidence-facts.md` | Evidence → Fact → State → Decision 流水线、TTL、置信度 |
| `adapters.md` | Transport、Adapter 接口、三平台映射、轮询 |
| `policy-engine.md` | AccountCapability、EligibilityState、动作矩阵、审批 |
| `authz.md` | Scope/新鲜度/revision helper、Evidence-bound 平台授权 |
| `opportunity-scheduler.md` | CompetitionSnapshot、期望收益/成本、调度 |
| `harness-model-router.md` | 执行边界、ActionRequest、模型路由 |
| `research-lifecycle.md` | Finding、Verification、Submission、Revenue Ledger |
| `implementation-order.md` | 依赖顺序实现计划（非 MVP 切分） |

## 7. 明确不做

- 不做自动扫描器编排去“发现漏洞”；只调度被策略允许的研究任务。
- 不做自动报名/自动签NDA/自动合规考核/自动提交/自动提现。
- 不把平台内部接口当官方 API；不绕过认证。
- 不把“最高赏金”当期望收益；必须经过 Opportunity Engine。
- 不在日志/数据库/审计文件保存密码、Cookie、身份证、银行卡、手机号。
