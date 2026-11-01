# Model 调用审计：逻辑调用与尝试

## 1. 为什么分成两层

一次“模型调用”可能包含多次 provider 尝试：

```text
逻辑调用 call/stable/<hash>
  ├─ attempt/.../1  local-small   AMBIGUOUS          （超时，可能已计费）
  └─ attempt/.../2  local-large   COMPLETED          （fallback 成功）
```

如果只保留一行，就无法同时回答两个问题：

- 这个任务步骤最终成功了吗？（逻辑调用）
- 我们究竟调用了几次 provider、每次可能花了多少钱？（尝试账本）

因此审计模型分为 `model.ModelCall`（逻辑）和 `model.ModelCallAttempt`
（尝试）。两者都只保存 hash、usage、cost 和状态，不保存 prompt/response
原文。

## 2. 逻辑调用 `ModelCall`

```go
type ModelCall struct {
    ID                  string
    TaskID              domain.TaskID
    StepID              string
    Purpose             Purpose
    DataClass           DataClass
    FinalStatus         CallStatus
    TotalCost           domain.Money
    HasAmbiguousAttempt bool
    StartedAt           time.Time
    EndedAt             time.Time
    Error               string
}
```

- `FinalStatus`：逻辑调用的最终状态。
  - `COMPLETED`：至少有一次尝试成功返回。
  - `AMBIGUOUS`：没有成功，且至少一次尝试可能已到达/计费。
  - `FAILED_BEFORE_SEND`：所有尝试都证明请求未发出（`domain.ErrNotSent`），
    或没有候选 provider。
  - `RESERVED` / `STARTED` 只出现在调用进行中；崩溃后停留在 `STARTED`
    的记录按 ambiguous 处理，必须人工对账。
  - **整条 fallback 链期间逻辑调用保持 `STARTED`**：某次 provider 返回
    `ErrNotSent` 只写 `FAILED_BEFORE_SEND` 的 attempt，不提前把逻辑调用
    置为可回收状态；只有链结束才写终态（有成功 → `COMPLETED`，任一
    attempt 可能已发送 → `AMBIGUOUS`，全部证明未发送 → `FAILED_BEFORE_SEND`）。
    这样并发重试无法在链中途 `Reclaim` 同一个 CallID。
  - 状态迁移是 CAS：`CallStore.Complete` 只允许 `RESERVED/STARTED → STARTED/终态`，
    已终态的行不能被迟到 writer 覆盖（`ErrInvalidCallTransition`）。PostgreSQL
    实现用 `WHERE status IN ('RESERVED','STARTED')` + 回读判定。
- `TotalCost`：已完成尝试的**实际**成本 + ambiguous 尝试的**估算**成本。
  `FAILED_BEFORE_SEND` 尝试不计费。该值用于预算复核与人工对账，不是收入。
- `HasAmbiguousAttempt`：只要任一次尝试为 `AMBIGUOUS` 就为 true，即使逻辑
  调用最终 `COMPLETED`。这提示“这个成功结果旁边可能还有一笔已扣费调用”。

逻辑调用是幂等边界：`CallStore.Reserve` 在 provider 调用前写入；同一
`CallID` 的重复请求不会再次付费。

## 3. 尝试 `ModelCallAttempt`

```go
type ModelCallAttempt struct {
    AttemptID     string
    CallID        string
    Provider      string
    Model         string
    Status        CallStatus // COMPLETED | AMBIGUOUS | FAILED_BEFORE_SEND
    EstimatedCost domain.Money
    ActualCost    domain.Money
    Usage         Usage
    PromptHash    string
    ResponseHash  string
    StartedAt     time.Time
    EndedAt       time.Time
    Error         string
}
```

每次 provider 调用（成功、ambiguous、失败）追加一行。尝试行不可修改；
重复写同一个 `AttemptID` 被忽略（幂等）。

状态语义：

| 状态 | 含义 | 是否可能计费 |
|------|------|--------------|
| `COMPLETED` | provider 返回了响应 | 是，按实际 usage |
| `AMBIGUOUS` | provider 报错但没有证明请求未发出 | 可能，按估算成本计入 |
| `FAILED_BEFORE_SEND` | provider 证明请求未发出（`domain.ErrNotSent`） | 否 |

## 4. 稳定 CallID

worker 模式必须使用确定性 ID：

```go
req.StepID = "analyze-scope"
req.CallID = model.StableCallID(req.TaskID, req.StepID, req.Purpose, model.PromptHash(req))
```

`StableCallID` 对 `(taskID, stepID, purpose, promptHash)` 做 SHA-256，相同输入
永远得到相同 ID。worker 崩溃重启后用同一 ID 重新进入 `Route`：

- 已 `COMPLETED` → `ErrDuplicateCall`，不会重复付费；
- `AMBIGUOUS` / `STARTED` / `RESERVED` → `ErrCallAmbiguous`，必须人工对账；
- `FAILED_BEFORE_SEND` → 回收后重试。

`Router.Options.RequireStableCallID=true` 会拒绝空 `CallID`，防止 worker 误用
自动 ID。交互/adhoc 调用可以继续使用自动 ID（带时间戳与序列号）。

## 5. 路由策略与 Provider 信任边界

`ModelRequest.ModelEgress`、`Redacted`、`Escalation` 只是 CLI/测试提示，
**不是授权**。Router 通过 `Options.PolicyResolver` 获取权威 `ModelPolicy`：

- `PolicyResolver` 必须从持久化的 `Program.ModelEgressPolicy` 推导
  `Egress`，从实际脱敏流程推导 `Redacted`，从可信的控制层 escalation 决策
  推导 `Escalation`。resolver 返回错误时 Route 直接失败，不调用 provider；
  `Candidates` 返回空计划。
- 未配置 resolver 时 fail closed 为 `LOCAL_ONLY`、无 escalation、未脱敏。
- `ProviderTrust` 分三类：`IN_PROCESS`（本进程/loopback/unix/static）、
  `LOCAL_NETWORK`（内网另一台机器，endpoint 必须是 IP 字面量且落在
  `Options.TrustedNetworks` CIDR 内）、`THIRD_PARTY`（云/远程 API）。
- `SENSITIVE` 只允许 `IN_PROCESS`；`LOCAL_NETWORK` 是另一台主机，可能记录或
  转发 prompt，视为离开进程。
- `THIRD_PARTY` 只有在 resolver 给出非空 `Escalation` 时才进入候选；
  没有 escalation 时，本地 provider 全部失败也不会隐式 fallback 到远程。
- 空 `Endpoint` + `Local=true` 以及 loopback/unix 端点按 `IN_PROCESS` 处理，
  兼容旧 catalog；显式 `Trust` 优先。

## 6. 数据库

`model_calls`（逻辑）：

| 列 | 说明 |
|----|------|
| `id` | 主键，幂等边界 |
| `task_id`, `step_id` | 任务与 worker 步骤 |
| `status` | `FinalStatus` |
| `total_cost_cents`, `currency` | 逻辑总成本 |
| `has_ambiguous_attempt` | 是否出现过 ambiguous 尝试 |
| `started_at`, `ended_at`, `error` | 时间与最终错误类别 |
| `data` | 完整 `ModelCall` JSON |

`model_call_attempts`（尝试，迁移 `0014_model_call_attempts.sql`）：

| 列 | 说明 |
|----|------|
| `attempt_id` | 主键 |
| `call_id` | 外键 → `model_calls(id)`，级联删除 |
| `provider`, `model`, `status` | 尝试身份与结果 |
| `estimated_cost_cents`, `actual_cost_cents`, `currency` | 估算/实际成本 |
| `input_tokens`, `output_tokens` | usage |
| `prompt_hash`, `response_hash` | 内容 hash，不含原文 |
| `started_at`, `ended_at`, `error` | 时间与错误类别 |
| `data` | 完整 `ModelCallAttempt` JSON |

`status` 约束只允许尝试终态：`COMPLETED`、`AMBIGUOUS`、
`FAILED_BEFORE_SEND`。逻辑表的状态约束保持
`RESERVED/STARTED/COMPLETED/AMBIGUOUS/FAILED_BEFORE_SEND`。

## 7. 对账与限制

- `FAILED_BEFORE_SEND` 的逻辑调用可以被 `Reclaim`；级联删除其尝试后，同一
  `CallID` 可以重新预占。
- `AMBIGUOUS` 逻辑调用不可自动重试；尝试账本保留每次估算成本，供人工与平台
  账单对账。
- 逻辑调用 `COMPLETED` 但 `HasAmbiguousAttempt=true` 时，成功结果可以继续
  使用，但必须保留该标志，不能把 ambiguous 尝试的成本当作 0。
- 当前没有真实 provider 适配器，`StaticProvider` 仅用于测试/本地 dry-run；
  尝试账本的字段已为真实 adapter 预留。
- 模型审计不产生 `ActionDecision`，也不能触发平台动作。
