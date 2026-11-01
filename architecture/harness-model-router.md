# Harness 与 Model Router

## 1. 边界

```text
Scheduler → ResearchTask → Harness → ActionRequest → Policy Engine → ActionDecision
                                      ↓
                                  Tool Registry
                                      ↓
                              Target / Browser / Model
```

- Harness **没有策略权**：不能自行决定扫描、请求、上传、提交。
- Harness 只能执行 `ActionDecision=ALLOW` 且未过期的动作；`REQUIRE_HUMAN` 必须回到人工队列。
- Model Router 只负责“用哪个模型”，不负责“能不能做”。
- 平台提交、报名、NDA、合规考核、收款永远不经过 Harness。

## 2. Harness

### 2.1 输入

```go
type ResearchTask struct {
    ID            domain.TaskID
    OpportunityID domain.OpportunityID
    ProgramID     domain.ProgramID
    AssetID       *domain.AssetID
    ScopeSnapshot domain.Scope
    Constraints   domain.Constraints
    Objective     string
    Budget        Budget
    ExpiresAt     time.Time
}

type Budget struct {
    MaxWallClock  time.Duration
    MaxRequests   int
    MaxTokens     int
    MaxCost       domain.Money
    MaxHumanHours float64
}
```

### 2.2 工具注册表

每个工具声明它需要的 Action：

```go
type ToolSpec struct {
    Name          string
    RequiredAction domain.Action
    NetworkEgress  EgressPolicy
    ReadsFiles     bool
    WritesFiles    bool
    Destructive    bool
    Description    string
}
```

示例：

| 工具 | RequiredAction | 说明 |
|------|----------------|------|
| `http.get` | ACTIVE_HTTP | 单请求；受 RPS/并发/Scope 限制 |
| `http.replay` | ACTIVE_HTTP | 重放请求；需 Evidence |
| `browser.navigate` | ACTIVE_HTTP | 页面导航 |
| `browser.screenshot` | ACTIVE_HTTP | 截图 |
| `passive.dns` | PASSIVE_ANALYSIS | 被动解析 |
| `code.read` | PASSIVE_ANALYSIS | 本地代码阅读；不访问目标 |
| `poc.run` | RCE_VERIFY / ACTIVE_HTTP | 需人工批准的条件动作 |
| `report.draft` | REPORT_DRAFT | 本地生成 |
| `model.call` | 无平台动作 | 仅本地/云模型调用 |

工具执行前：

```text
toolCall -> ActionRequest -> Policy.Decide -> ALLOW? -> execute
                                      └─ DENY/REQUIRE_HUMAN -> stop
```

### 2.3 执行循环

```text
1. 加载 ResearchTask + 最新 Scope/Constraints
2. 检查 Program.Status / Decision 有效期
3. 规划下一步动作（模型或规则）
4. 为每个动作构造 ActionRequest
5. Policy Engine 决策
6. ALLOW -> 执行工具；记录 Evidence
7. DENY -> 记录原因；换路径或结束
8. REQUIRE_HUMAN -> 暂停任务，进入人工队列
9. 预算/时间/请求超限 -> 终止
10. 产出 FindingCandidate + EvidenceBundle
```

### 2.4 沙箱

- 执行工作区按任务隔离（临时目录/容器）。
- 网络出口按目标 Scope 白名单；默认拒绝。
- 文件写入仅限工作区；禁止读取用户主目录敏感文件。
- 工具进程以低权限运行；不继承浏览器 Cookie。
- 任何下载文件视为不可信数据；不执行、不 `import`、不作为脚本运行。

### 2.5 中止条件

- Program 状态变为 PAUSED/ENDED。
- ActionDecision 过期。
- 预算耗尽。
- 命中 forbidden action。
- 检测到敏感数据（立即停止、删除、报告）。
- 人工取消。
- 平台会话失效。

## 3. Model Router

### 3.1 接口

```go
type ModelRequest struct {
    TaskID      domain.TaskID
    StepID      string // worker step; feeds StableCallID
    Purpose     string // PLAN | ANALYZE | CODE | REPORT | EMBED
    Input       []Message
    Tools       []ToolSpec
    MaxTokens   int
    Budget      domain.Money
    DataClass   DataClass // PUBLIC | INTERNAL | SENSITIVE
    ModelEgress domain.ModelEgressPolicy // LOCAL_ONLY | REMOTE_REDACTED_ONLY | REMOTE_ALLOWED | UNKNOWN
    Redacted    bool     // caller attestation for REMOTE_REDACTED_ONLY
    Escalation  EscalationReason // empty | STALLED | LOW_CONFIDENCE | HIGH_VALUE_TASK | VERIFICATION_CONFLICT
    CallID      string   // idempotency key; required in worker mode
}

type ModelResponse struct {
    Content    string
    ToolCalls  []ToolCall
    Usage      Usage
    Model      string
    Provider   string
    Latency    time.Duration
    Confidence float64
}

type Router interface {
    Route(ctx context.Context, req ModelRequest) (ModelResponse, error)
}
```

### 3.2 路由策略

路由是 **local-first**，与 purpose 无关：

```text
确定性规则/工具  ->  本地小模型  ->  更强的本地模型  ->  远程强模型
```

只有显式 `EscalationReason` 才允许把远程强模型提到本地模型之前；`Escalation` 为空时，即使 egress 允许远程，也先用本地。

| 层级 | 选择条件 | 排序 |
|------|----------|------|
| 确定性层 | 规则、schema、evidence grounding、policy | 不调用模型 |
| 本地小模型 | `ProviderInfo.IsLocal()` | `Strength` 升序（小模型优先） |
| 更强本地 | 同上 | `Strength` 升序 |
| 远程强模型 | egress 允许且数据等级允许 | `Strength` 降序（强模型优先），永远排在本地之后 |
| 升级路由 | `Escalation != ""` | 远程强模型在前，其次本地强模型在前 |

规则：

- `Program.ModelEgressPolicy` 是程序级 egress 上限，不是 Provider 能力：
  - `LOCAL_ONLY` / `UNKNOWN`（以及零值）只允许 `IsLocal()`；
  - `REMOTE_REDACTED_ONLY` 仅当 `Redacted=true` 且有效数据等级不是 `SENSITIVE` 时允许远程，本地始终允许；
  - `REMOTE_ALLOWED` 按数据等级规则（`SENSITIVE` 仍只走本地）。
- `DataClass=SENSITIVE` 永不离开本进程，即使 `REMOTE_ALLOWED` 也一样。
- 单任务 token/成本预算硬限制；fallback 链累计预算，失败尝试的估算也计入。
- 模型输出不直接产生 Action；必须经过 Harness 的 ActionRequest。
- 模型不能持有平台 Cookie/Token；浏览器会话只在 Adapter/Browser Sidecar。

### 3.3 证据与可追溯

每次模型调用记录：

```json
{
  "task_id": "...",
  "purpose": "ANALYZE",
  "model": "...",
  "provider": "...",
  "prompt_hash": "...",
  "response_hash": "...",
  "usage": {"input_tokens": 0, "output_tokens": 0},
  "cost": {"amount": 0, "currency": "CNY"},
  "data_class": "INTERNAL",
  "started_at": "...",
  "ended_at": "..."
}
```

模型输出作为 Finding 证据时，必须附：

- 原始输入（脱敏）
- 模型/版本
- 人工验证结果
- 复现证据

## 4. Harness 与 Model 的权限矩阵

| 能力 | Harness | Model | Human |
|------|---------|-------|-------|
| 读取公开规则 | ✅ | ✅ | ✅ |
| 读取 GATED Scope | ❌ | ❌ | ✅（报名/NDA后） |
| 发起 Active HTTP | 需 ALLOW | ❌ | ✅ |
| 自动化扫描 | 需 ALLOW（默认DENY） | ❌ | ✅ |
| 生成报告草稿 | ✅ | ✅ | ✅ |
| 提交报告 | ❌ | ❌ | ✅ |
| 报名/NDA/合规考核 | ❌ | ❌ | ✅ |
| 提现/绑卡 | ❌ | ❌ | ✅ |

## 5. 失败与降级

- 模型不可用 → 回退规则/本地模型；任务不自动扩大权限。
- 浏览器会话失效 → `AUTH_REQUIRED`；暂停平台队列。
- 页面改版 → 抽取器失败；不猜测；人工修复。
- 预算耗尽 → 任务挂起，等待人工决定。
- 发现敏感数据 → 立即停止、删除本地副本、记录事件；按平台要求报备。

## 5.1 Phase 5 实现记录（2026-11-01）

- `internal/harness`：Registry 校验工具名/RequiredAction/重复；Harness 强制 `Evaluate → ALLOW → Budget → Authorize → Execute`，`DENY/REQUIRE_HUMAN` 不调用工具；`DeniedError` 带决策。预算检查在 Authorize 之前，避免预算拒绝消耗一次性审批。
- Sandbox/Egress：默认拒绝 egress；`-allowed-hosts` 精确匹配，`*.example.org` 只匹配子域；`http/https` 以外拒绝。网络工具只拿到绑定 `ExecutionGrant` 的 `harness.Egress`：approved method、canonical asset host/path、RPS、并发与日限额在每个请求/重定向上重新校验；空 allowlist 的网络工具直接拒绝。`Egress` 使用专用 `http.Transport`（连接/握手/响应头超时、空闲连接上限）并限制响应体大小（默认 8 MiB），工具拿不到裸网络客户端。连接前解析 DNS 并拒绝 loopback/private/link-local/ULA/multicast，固定到校验后的 IP，默认不使用环境代理。文件工具只能拿到 `harness.Workspace`，其底层是 `os.Root`，`../` 与指向根外的符号链接由操作系统边界拒绝；没有 workspace 的文件工具直接拒绝。
- 网络代理边界：`internal/harness/proxy.go` 是逐请求边界。`NetworkPolicy` 对每个 HTTP 请求（host/port/method/path）或 CONNECT（host/port）返回 `allow/deny/ask`；无交互 UI 时 `ask`=403。先审计后转发，`allow` 审计失败则不转发；请求/响应 8 MiB 上限、hop-by-hop 剥离、绝不使用环境代理。`Policy`/`Audit` 缺失时 `Validate`/`Serve`/`ListenAndServe` 直接失败，handler 也拒绝所有请求（`AllowUnauditedForTests` 仅测试）。CONNECT 默认关闭（`AllowOpaqueConnect=false`），启用后也只是公网 IP 过滤后的 host 级 opaque tunnel（无 TLS MITM，路径级 grant 无法满足）。`RulePolicy` 桥接 `internal/policy/rules`，是 guardrail（未命中默认 pass-through，`DenyUnmatched` 可收紧），生产必须包在 `GrantPolicy` 里；`GrantPolicy` 校验 grant 非 nil、未过期、method 与 host/path 覆盖请求，`Next` 只能收紧。`AuditSink` 写 `network.policy_decision`（path 只存 hash）。`Egress` 仅在显式 `Sandbox.ApprovedProxy` 时走代理，非法配置 fail closed。
- 预算与限流：`Budget.Reserve` 原子预占（检查 + 计数在同一锁内），`Commit/Release` 结算；并发 worker 不会同时越过 `MaxRequests/MaxCost`。预算预占仍在 Authorize 之前，避免预算拒绝消耗一次性审批。HTTP 日限额、RPS 与并发由 base `Egress` 的 `programLimiter` 按 `AccountID+ProgramID+interface` 共享（无身份则 per-grant），`WithGrant` 不再重置计数；路径前缀按 segment-aware `rules.PathWithin` 匹配。
- 执行回执：`NetworkEgress`/`WritesFiles`/`Destructive` 工具必须带 `IdempotencyKey`；执行前预占 `execution_receipts`（`STARTED` → `EXECUTED` → `COMPLETED`/`FAILED`/`AMBIGUOUS`）。重试回放结果；`STARTED` 视为未决、拒绝重复执行；网络/破坏性工具报错默认 `AMBIGUOUS`（可能已到达目标，必须人工对账），只有工具返回 `domain.ErrNotSent` 才落 `FAILED` 并允许同 key 重试。工具已执行但审计失败时回执保持 `EXECUTED`，重试只补审计。内置只读工具不需要 key。
- 内置工具 `view_rules/read_scope/report_draft`：本地只读，不发起网络/扫描/提交。`tool.executed` 写入审计；决策审计由 Action Gateway 负责（PostgreSQL 使用同一事务）。
- 实测：`view_rules` ALLOW → Authorize → 执行成功；缺失账号 DENY `CONTEXT_NOT_FOUND`，工具未执行；`action_decisions=5`、`tool.executed=1`。
- 未实现：ResearchTask 自动领取、真实执行工具（本仓库禁止真实测试）。

### 5.2 Model Router 实现记录（2026-11-01）

- `internal/model`：`Purpose`（PLAN/ANALYZE/CODE/REPORT/EMBED）、`DataClass`（PUBLIC/INTERNAL/SENSITIVE）、`ProviderInfo`、`Provider`、`Router`。
- 数据分级：`EffectiveDataClass = max(DataClass, Provenance...)`，分类只升不降；`SENSITIVE` 只允许 `IsLocal()` 的 Provider。`Local` 由 transport 身份决定（loopback/unix/npipe/stdio/static 或 in-process），`Router.New` 拒绝“声明 local 但 endpoint 是远程 URL”的 Provider；无本地 Provider → `ErrNoProvider`，绝不降级到云端。
- 程序级 egress：`domain.Program.ModelEgressPolicy`（`LOCAL_ONLY` / `REMOTE_REDACTED_ONLY` / `REMOTE_ALLOWED` / `UNKNOWN`，零值等同 `UNKNOWN`）。`UNKNOWN` 按 `LOCAL_ONLY` 执行，旧 Program 记录不会意外获得远程权限。`REMOTE_REDACTED_ONLY` 需要请求携带 `Redacted=true` 且有效数据等级不是 `SENSITIVE`；`REMOTE_ALLOWED` 仍受 `SENSITIVE` 本地限制。
- 路由：默认 local-first，与 purpose 无关——本地小模型（`Strength` 升序）→ 更强本地 → 远程强模型（`Strength` 降序）。只有显式 `EscalationReason`（`STALLED` / `LOW_CONFIDENCE` / `HIGH_VALUE_TASK` / `VERIFICATION_CONFLICT`）才把远程强模型排到本地之前；升级不能绕过 egress 或 `SENSITIVE` 限制。`MaxOutputTokens`、`MaxContextTokens`（PromptTokens+MaxTokens）、`RequiredCapabilities`（TOOL_CALLING/VISION/STRUCTURED_OUTPUT）不满足的 Provider 不参与。
- 预算：整条 fallback 链累计预算，失败的 Provider 也计入估算；调用后按实际 usage 复核，累计超预算则返回 `ErrBudgetExceeded`。所有价格以“分/百万 token、CNY”表示。
- 调用持久化：逻辑调用与 provider 尝试分离。`CallStore` 在调用前预占 `model_calls`（`CallID` 幂等；重复的显式 CallID 返回 `ErrDuplicateCall`），每次 provider 调用追加一条 `model_call_attempts`，最后用 `FinalStatus`、`TotalCost`、`HasAmbiguousAttempt` 完成逻辑调用。`TotalCost` 覆盖已完成尝试的实际成本与 ambiguous 尝试的估算（`FAILED_BEFORE_SEND` 不计费）。详见 `architecture/model-audit.md`。
- 稳定 CallID：worker 请求带 `StepID`，用 `StableCallID(taskID, stepID, purpose, promptHash)` 生成确定性 ID；`Options.RequireStableCallID` 在 worker 模式下拒绝空 `CallID`。交互/adhoc 仍使用带序列号的自动 ID。
- 审计：每次尝试写 `model.call`，payload 只有 `attempt_id/call_id/step_id/task_id/purpose/data_class/provider/model/status/estimated_cost/actual_cost/prompt_hash/response_hash/usage/duration_ms/error`，不含 prompt/response 原文。
- 失败降级：Provider 报错时记录尝试并按路由顺序尝试下一个；全部失败返回最后一个错误，逻辑调用保留 `AMBIGUOUS` 或 `FAILED_BEFORE_SEND`。
- `bountygrid model -purpose ... -data-class ... -model-egress ... [-redacted] [-escalation ...] -max-tokens ... -budget ...` 只打印路由计划（`Candidates` + 估算成本），不调用模型、不发网络请求。默认 `UNKNOWN` 只显示本地候选。
- 未实现：真实本地/云端 Provider 适配器（需要 env 配置与密钥管理）、Model Router 与 ResearchTask 的自动领取。`StaticProvider` 仅用于测试/本地 dry-run；PostgreSQL `model_calls` 存储已实现，但 CLI 尚未接入真实调用链。

## 6. 审计事件

- `harness.task.started/stopped`
- `harness.action.requested`
- `policy.decision`
- `tool.executed`
- `model.called`
- `finding.created`
- `verification.completed`
- `report.drafted`
- `submission.approved/submitted`
- `ledger.updated`

每个事件带 `task_id/action_id/decision_id/evidence_refs`，不可修改。
