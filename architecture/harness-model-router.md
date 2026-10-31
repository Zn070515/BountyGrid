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
    TaskID     domain.TaskID
    Purpose    string // PLAN | ANALYZE | CODE | REPORT | EMBED
    Input      []Message
    Tools      []ToolSpec
    MaxTokens  int
    Budget     domain.Money
    DataClass  DataClass // PUBLIC | INTERNAL | SENSITIVE
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

| 任务 | 默认路由 | 理由 |
|------|----------|------|
| 规划/长上下文分析 | 云端强模型 | 复杂推理 |
| 代码/正则/结构化抽取 | 本地模型或小模型 | 成本低、数据不出境 |
| 报告润色 | 本地/云端按数据等级 | 敏感数据优先本地 |
| Embedding | 本地 | 批量成本 |

规则：

- `DataClass=SENSITIVE` 默认只走本地；除非人工批准且平台规则允许。
- 单任务 token/成本预算硬限制。
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
