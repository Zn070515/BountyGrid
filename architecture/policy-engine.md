# Policy Engine

## 1. 职责

Policy Engine 只回答一个问题：

> 在**此刻**，这个 Actor 对**这个账号 + 这个项目 + 这个资产**执行**这个动作**，允许吗？

它不发现漏洞、不生成报告、不调用模型、不操作浏览器。它输出 `ActionDecision`，由 Action Gateway 执行。

## 2. 决策上下文

```go
type DecisionContext struct {
    Request     domain.ActionRequest
    Account     domain.Account
    Program     domain.Program
    Asset       *domain.Asset
    Competition *domain.CompetitionSnapshot
    Now         time.Time
    Policy      PolicyVersion
}
```

判断对象：

```text
Actor + PlatformAccount + Program + Asset + Action + Time
```

同一个项目：

```text
VIEW_RULES       → ALLOW
READ_SCOPE       → ALLOW (PUBLIC) / REQUIRE_HUMAN (GATED)
PASSIVE_ANALYSIS → ALLOW
ACTIVE_HTTP      → CONDITIONAL
AUTOMATED_SCAN   → DENY
REPORT_DRAFT     → ALLOW
REPORT_SUBMIT    → REQUIRE_HUMAN
PAYOUT           → REQUIRE_HUMAN
```

## 3. 前置：账号能力

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

账号级 Gate：

| Gate | 平台 | 说明 |
|------|------|------|
| IDENTITY_VERIFIED | 三平台 | 实名 |
| COMPLIANCE_EXAM | 补天 | 季度合规考核；未完成 → CanSubmit=false |
| PRACTICAL_CERTIFICATION | 补天 | 专属SRC需≥1有效漏洞；不满足 → 专属SRC不可提交 |
| BANK_BINDING | 漏洞盒子/补天 | 提现前置 |
| WECHAT_BINDING | 漏洞盒子/补天 | 通知/高级项目筛选 |
| PROFILE_COMPLETION | 漏洞盒子 | 高级项目筛选 |
| REPUTATION | 漏洞盒子 | 高级项目门槛 |
| HISTORY_VALID_VULNS | 补天/漏洞盒子 | 实战认证/高级项目 |

**关键**：`COMPLIANCE_EXAM` 不能放在 `Program.Gates`。它属于 `Account.Gates`，影响整个账号。

## 4. 项目状态与门槛

```go
type EligibilityState struct {
    Decision      EligibilityDecision
    Gates         []EligibilityGate
    EvaluatedAt   time.Time
    PolicyVersion string
}
```

项目级 Gate：

| Gate | 平台示例 |
|------|----------|
| PROGRAM_SIGNUP | 先知所有任务；补天专属SRC；漏洞盒子高级项目 |
| APPLICATION_REVIEW | 漏洞盒子高级项目；补天众测 |
| INVITATION | 漏洞盒子专属/私密；补天众测 |
| NDA | 漏洞盒子专属/私密/高级；补天众测 |
| STUDENT_VERIFICATION | 补天 GROW |
| TEAM_MEMBERSHIP | 补天 GROW 校园战队 |
| TEST_ACCOUNT_REPORTING | 先知淘天反爬；漏洞盒子VPN项目 |

## 5. Eligibility 计算

```go
func EvaluateEligibility(acc domain.Account, p domain.Program) domain.EligibilityState {
    gates := make([]domain.EligibilityGate, 0, len(acc.Gates)+len(p.Gates))
    gates = append(gates, copyAccountGatesFor(acc, p)...)
    gates = append(gates, p.Gates...)

    hasBlockingUnsatisfied := false
    hasUnknown := false
    humanOnly := false

    for _, g := range gates {
        if !g.Required || g.Satisfied == domain.GateNotRequired {
            continue
        }
        switch g.Satisfied {
        case domain.GateUnsatisfied:
            if g.Blocking {
                hasBlockingUnsatisfied = true
                if isHumanResolvable(g.Kind) {
                    humanOnly = true
                }
            }
        case domain.GateUnknown:
            hasUnknown = true
        }
    }

    switch {
    case humanOnly:
        return domain.EligibilityState{Decision: domain.HumanActionNeeded, Gates: gates}
    case hasBlockingUnsatisfied:
        return domain.EligibilityState{Decision: domain.Ineligible, Gates: gates}
    case hasUnknown:
        return domain.EligibilityState{Decision: domain.EligibilityUnknown, Gates: gates}
    default:
        return domain.EligibilityState{Decision: domain.Eligible, Gates: gates}
    }
}
```

`isHumanResolvable`：

```text
PROGRAM_SIGNUP, APPLICATION_REVIEW, NDA, COMPLIANCE_EXAM,
PRACTICAL_CERTIFICATION, BANK_BINDING, WECHAT_BINDING,
STUDENT_VERIFICATION, TEAM_MEMBERSHIP, PAYOUT_SETUP
```

这些只能人工完成，返回 `HUMAN_ACTION_REQUIRED`，不是 `INELIGIBLE`。

## 6. 动作策略矩阵

策略分三层：

```text
HARD_DENY      平台/法律绝对禁止，不可被项目覆盖
PLATFORM_DEFAULT 平台默认，项目可明确放宽/收紧
PROGRAM_OVERRIDE 项目级明确规则，优先于平台默认
```

### 6.1 硬拒绝（任何项目都不可放宽）

```text
INTERNAL_NETWORK
LATERAL_MOVEMENT
DOS
CREDENTIAL_STUFFING（默认；仅项目明确“证明型”例外）
SENSITIVE_DATA_READ（默认；仅项目明确“证明型/限额”例外）
```

> 注意：平台原文允许“证明型”的，归为 `CONDITIONAL + 限额`，不是硬拒绝；但绝不能取用户数据。

### 6.2 动作矩阵

| Action | 默认 | 条件 |
|--------|------|------|
| VIEW_PROGRAM / VIEW_RULES | ALLOW | 公开页面 |
| READ_SCOPE | ALLOW if Access=GRANTED 且 contents known；否则 REQUIRE_HUMAN | Disclosure 可以是 GATED_*；READ_SCOPE 本身不是执行 |
| PASSIVE_ANALYSIS | ALLOW | Scope Access=GRANTED；不得主动请求目标 |
| ACTIVE_HTTP | CONDITIONAL | Program RUNNING；Access=GRANTED；Asset 在 Scope；Constraints 满足 |
| DIRECTORY_ENUMERATION | DENY | 仅项目明确允许且低影响 |
| SUBDOMAIN_DISCOVERY | CONDITIONAL | 仅授权范围 |
| AUTOMATED_SCAN | DENY | 仅项目明确允许（如淘天反爬）且限额满足 |
| HIGH_CONCURRENCY | DENY | 不得影响稳定性 |
| BRUTE_FORCE | CONDITIONAL | 项目明确允许 + 次数上限 + 不测真实用户 |
| CREDENTIAL_STUFFING | DENY | 仅项目明确“成功证明”例外 |
| FILE_UPLOAD | CONDITIONAL | 仅证明；禁止驻留后门（联想SRC绝对禁止 webshell） |
| STATE_MUTATION | DENY | 仅项目明确最小影响例外；禁止核心CRUD自动化遍历 |
| RCE_VERIFY | CONDITIONAL | 证明即停；禁止读源码/提权/横向 |
| SENSITIVE_DATA_READ | DENY | 仅证明存在；SQLi 仅库表名；硬上限 |
| INTERNAL_NETWORK / LATERAL / DOS | DENY | 不可覆盖 |
| REPORT_DRAFT | ALLOW | 需 Finding/Verification |
| REPORT_SUBMIT | REQUIRE_HUMAN | 永远 |
| PROGRAM_SIGNUP / NDA_ACCEPT / COMPLIANCE_EXAM / PRACTICAL_CERTIFICATION | REQUIRE_HUMAN | 永远 |
| PAYOUT / BANK_BINDING | REQUIRE_HUMAN | 永远 |

### 6.3 项目覆盖示例

```text
先知 淘天电商反爬专项：
  BROWSER_AUTOMATION → ALLOW
  PURE_SCRIPT → ALLOW
  ACTIVE_HTTP → CONDITIONAL
  AUTOMATED_SCAN → CONDITIONAL (按接口日限额/预算/先到先得)
  但：BROWSER_AUTOMATION 仍受 daily limits / budget / test account reporting

漏洞盒子 联想SRC：
  FILE_UPLOAD → CONDITIONAL
  WEBSHELL_UPLOAD → DENY（项目明确禁止）

补天 理想汽车专属SRC：
  AUTOMATED_SCAN → DENY
  HIGH_CONCURRENCY → DENY
  INTERNAL_NETWORK → DENY
```

限额与预算不是注释：`DecisionContext.UsageToday` 按 `PerInterfaceDailyLimits.Interface` 计数，`BudgetSpent` 对 `BudgetCap` 比较；达到上限直接 `DENY`（`DAILY_LIMIT_EXCEEDED` / `BUDGET_EXCEEDED`），不能靠人工审批绕过。

### 6.4 声明式规则覆盖（`internal/policy/rules`）

规则是数据，不是权限本身。每条规则按 action/program/host/method/path 选择请求，携带 `allow|require_human|forbidden` 与 `justification`，并可在加载时用 `match`/`not_match` 样例自测：

```json
{
  "id": "vulbox-444-read-only",
  "actions": ["ACTIVE_HTTP"],
  "programs": ["vulbox/444"],
  "hosts": ["*.example.com"],
  "methods": ["GET", "HEAD"],
  "decision": "allow",
  "justification": "read-only scope",
  "match": [{"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://api.example.com/v1", "method": "GET"}],
  "not_match": [{"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://example.com/v1", "method": "GET"}]
}
```

- 选择器内是 OR，不同字段之间是 AND；空选择器表示 any。
- `*.example.com` 只匹配子域，`**.example.com` 才匹配 apex；全局 `*`、带 scheme/port/path 的 host 在加载时拒绝。
- URL 在匹配前规范化：host 小写/去尾点/去端口/去 IPv6 括号，path 百分号解码 + 强制前导 `/` + `path.Clean`。
- **权限层级**：平台授权是 ceiling，本地规则只能收紧。规则文件的 `source` 只有 `operator`（默认）；`source=platform_evidence` 的文件被加载器直接拒绝（platform authority 只能由 Evidence 编译，不能手写 JSON）。`operator` 的 `allow` 永远不能把 `REQUIRE_HUMAN`/`DENY` 变成 `ALLOW`。
- **平台授权独立求值**：`rules.PlatformRule` 是独立类型（无 JSON loader，字段私有，只能由 `NewPlatformRule` 构造）。`authz.BuildPlatformPolicy` 在构造前验证 evidence 存在、`Evidence.Platform == Program.Platform`、程序绑定、`PolicyRevision`、观察窗口和 `Program.PolicyFreshAt`。`Engine.WithPlatformAuthority` 只接受这种已验证的 policy；决策时再检查平台/revision/新鲜度。只有它能把平台自身的 `CONDITIONAL` 放宽为 `ALLOW`（`CanRelax=true`），且不能覆盖硬 `DENY`、`AUTOMATION_UNKNOWN/FORBIDDEN`、Scope/资产/资格/新鲜度/运行时约束，也不能覆盖 operator 的 `require_human`/`forbidden`。详见 [`authz.md`](authz.md)。
- 多规则命中取最严：`forbidden > require_human > allow`（operator 层内）。
- 路径前缀 segment-aware：`/api` 匹配 `/api`、`/api/users`，不匹配 `/apix`、`/api2`、`/api-admin`；匹配前统一 `CanonicalPath`（单次 percent-decode、拒绝残留 `%2f/%2e/%5c`、反斜杠归一、`path.Clean`）。
- 决策记录 `RuleIDs` 与 `RuleSetFingerprint`；指纹覆盖 operator 规则与平台授权的 provenance，`Authorize` 拒绝指纹变化后的旧授权。
- `bountygrid policy check -rules <file> -action ...` 加载并自测 operator 规则、输出命中规则（`can_relax` 恒为 false）；`decide`/`harness`/`submit` 可用可选 `-rules`。平台授权目前只通过 Go API 注入。

规则层与 `ExecutionGrant` 的关系：规则决定“是否需要人工审批”，`ExecutionGrant` 决定“这个请求到底能打到哪个 host/method/path、以什么 RPS/并发/日限额”。两者都不能被对方替代。

## 7. 决策算法

```go
func (e *Engine) Decide(ctx context.Context, req domain.ActionRequest) (domain.ActionDecision, error) {
    dc, err := e.loadContext(ctx, req)
    if err != nil { return domain.ActionDecision{}, err }

    // 0a. 决策上下文必须与请求一致，不能用一个 Program 的结论服务另一个请求
    if dc.Account.ID != req.AccountID || dc.Program.ID != req.ProgramID ||
       dc.Account.Platform != dc.Program.Platform {
        return deny(req, "CONTEXT_MISMATCH"), nil
    }

    // 0b. 证据新鲜度与置信度：执行/提交要求新鲜且 confidence >= 0.8；
    //     查看规则/项目不要求
    if requiresFreshState(req.Action) {
        if !e.evidenceFresh(dc) {
            return requireMoreEvidence(req, "STALE_PROGRAM_STATE"), nil
        }
        if dc.Program.Status.Confidence < 0.8 {
            return requireMoreEvidence(req, "LOW_CONFIDENCE_STATE"), nil
        }
    }

    // 1. 账号能力
    if req.Action == domain.ActionReportSubmit && !dc.Account.Capability.CanSubmit {
        return deny(req, "ACCOUNT_CANNOT_SUBMIT"), nil
    }

    // 2. 项目状态：执行/提交要求 RUNNING；查看不要求
    if (req.Action.NeedsScope() || req.Action == domain.ActionReportSubmit) &&
       dc.Program.Status.Status != domain.StatusRunning {
        return deny(req, "PROGRAM_NOT_RUNNING"), nil
    }

    // 3. 项目门槛：只有参与类动作才评估 Eligibility
    if req.Action.EligibilityRequired() {
        switch dc.Program.Eligibility.Decision {
        case domain.Ineligible:
            return deny(req, "PROGRAM_INELIGIBLE"), nil
        case domain.HumanActionNeeded:
            return requireHuman(req, "PROGRAM_GATE_HUMAN_ACTION"), nil
        case domain.EligibilityUnknown:
            return requireMoreEvidence(req, "PROGRAM_GATE_UNKNOWN"), nil
        }
    }

    // 4. READ_SCOPE 是人工解锁动作，不是执行
    if req.Action == domain.ActionReadScope {
        if dc.Program.Scope.Visibility == domain.ScopeUnknown {
            return requireMoreEvidence(req, "SCOPE_UNKNOWN"), nil
        }
        if dc.Program.Scope.Access == domain.ScopeAccessGranted && dc.Program.Scope.ContentsKnown {
            return allow(dc)
        }
        return requireHuman(req, "SCOPE_GATED"), nil
    }

    // 5. 执行要求 Access=GRANTED 且 contents known
    if req.Action.NeedsScope() &&
       (dc.Program.Scope.Access != domain.ScopeAccessGranted || !dc.Program.Scope.ContentsKnown) {
        return deny(req, "SCOPE_GATED"), nil
    }

    // 6. Asset 必须显式属于 Scope，且必须是请求指定的那个资产
    if req.Action.NeedsAsset() {
        if req.AssetID == nil || dc.Asset == nil {
            return deny(req, "ASSET_REQUIRED"), nil
        }
        if *req.AssetID != dc.Asset.ID {
            return deny(req, "ASSET_MISMATCH"), nil
        }
        if dc.Asset.ProgramID != req.ProgramID {
            return deny(req, "ASSET_PROGRAM_MISMATCH"), nil
        }
        switch dc.Program.Scope.CheckAsset(*dc.Asset) {
        case domain.ScopeMatchIncluded:
        case domain.ScopeMatchConditional:
            return requireHuman(req, "ASSET_CONDITIONAL"), nil
        case domain.ScopeMatchExcluded:
            return deny(req, "ASSET_OUT_OF_SCOPE"), nil
        default:
            return requireMoreEvidence(req, "ASSET_SCOPE_UNKNOWN"), nil
        }
    }

    // 7. AutomationPolicy：FORBIDDEN 不可被人工审批覆盖
    base := e.actionMatrix[req.Action]
    if cap, ok := automationCapability(dc.Program.Automation, req.Action); ok {
        switch cap {
        case domain.CapForbidden:
            return deny(req, "AUTOMATION_FORBIDDEN"), nil
        case domain.CapUnknown:
            return requireMoreEvidence(req, "AUTOMATION_UNKNOWN"), nil
        case domain.CapConditional:
            // CONDITIONAL 只能加人工门槛，不能把硬拒绝降级为可审批动作
            if base != domain.DecisionDeny {
                base = domain.DecisionRequireHuman
            }
        case domain.CapAllowed:
            if overridableByAutomation(req.Action) {
                base = domain.DecisionAllow
            }
        }
    }

    // 7b. operator 规则与平台授权分开求值，避免 operator allow 冒充平台授权
    ruleReq := rules.Request{Action: req.Action, Program: req.ProgramID,
        Asset: dc.AssetValue(), Method: req.Method, Now: now}
    operatorTightened := false
    if e.rules != nil {
        rr, err := e.rules.Evaluate(ruleReq)
        if err != nil { return deny(req, "RULE_REQUEST_INVALID"), nil }
        switch rr.Decision {
        case rules.DecisionForbidden:
            return denyWithRules(req, rr), nil
        case rules.DecisionRequireHuman:
            if base != domain.DecisionDeny { base = domain.DecisionRequireHuman }
            operatorTightened = true
        case rules.DecisionAllow:
            // Operator allow never raises the ceiling.
        }
    }
    if e.platform != nil {
        pr, err := e.platform.Evaluate(dc.Program, ruleReq)
        if err != nil { return deny(req, "RULE_REQUEST_INVALID"), nil }
        // 只有已验证平台规则、且 automation 确实是 CONDITIONAL、且 operator
        // 没有收紧时，才允许放宽。
        if pr.CanRelax && !operatorTightened && conditionalGate &&
            base != domain.DecisionDeny && overridableByAutomation(req.Action) {
            base = domain.DecisionAllow
        }
    }

    // 8. 动作矩阵 / 人工批准
    if base == domain.DecisionDeny { return deny(req, "ACTION_DENIED"), nil }
    if req.Action.HumanOnly() && req.HumanApproval == nil {
        return requireHuman(req, "HUMAN_ONLY_ACTION"), nil
    }
    if base == domain.DecisionRequireHuman && req.HumanApproval == nil {
        return requireHuman(req, "APPROVAL_REQUIRED"), nil
    }
    if req.HumanApproval != nil {
        // Validate only. Consumption happens in Gateway.Authorize after the
        // decision is re-evaluated against fresh state.
        if err := validateApproval(req, dc.ResourceID); err != nil {
            return requireHuman(req, "INVALID_APPROVAL"), nil
        }
    }
    return allow(dc)
}
```

## 8. 人工审批

`HumanApproval` 必须绑定：

```text
approval_id
approved_by（人工账号）
approved_at
request_id（必须等于本次 ActionRequest）
action（只能覆盖单个动作）
program_id
asset_id（与请求一致，允许两者都为空）
resource_id（REPORT_SUBMIT 必填：report/finding ID）
parameters_hash（请求参数 SHA-256）
scope（项目/资产/时间窗）
comment
expires_at（默认 24h，提交类更短）
single_use（human-only 动作必须 true）
```

`REPORT_SUBMIT` 必须做到**单报告、单审批、单消费**：审批绑定 `resource_id` 与 `parameters_hash`。`Policy.Decide` 只校验审批；`Gateway.Authorize` 在重新加载 Account/Program、重新解析 Asset 并重跑 Policy 之后，才由 `ApprovalLedger` 原子消费；重复使用在 Authorize 阶段失败，不能凭一张旧审批提交多份报告。产品实现使用 PostgreSQL `consumed_approvals`（`INSERT ... ON CONFLICT DO NOTHING`），进程重启后仍保持单次消费。`parameters_hash` 同时接受原始 SHA-256 与 canonical JSON SHA-256，避免 JSONB 重新编码导致合法审批被拒。

- 报告提交：审批时展示完整报告、AI披露、证据、影响、范围、重复风险。
- 报名/NDA/合规考核/认证：审批只代表“允许打开/发起人工流程”，不代表自动完成。
- 提现/绑卡：BountyGrid 不执行；只提示人工处理。

## 9. 决策缓存

- `ActionDecision` 可缓存，但必须带 `ExpiresAt`。
- `ExpiresAt` 取 `now+5m`、`Program.Status.ValidUntil`、`HumanApproval.ExpiresAt` 三者中最早的时间；决策不能比它所依据的状态或授权活得更久。
- ApprovalLedger 只有返回 `domain.ErrApprovalConsumed` 才映射为 `REQUIRE_HUMAN`；数据库/网络故障必须向上返回错误，不能伪装成“审批已使用”。
- 项目状态 TTL 内有效；状态变化/证据 supersede 时立即失效。
- 执行前 Action Gateway 必须二次校验 `ExpiresAt` 与 `Program.Status`。`bountygrid decide -authorize` 实现：ALLOW + 未过期 + human-only 有审批；执行类动作要求 Program RUNNING 且 `Status.ValidUntil > now`。Gateway 本身不执行动作，DENY 无副作用。
- Decision ID 使用带分隔符的时间戳，避免 16-19 位连续数字被 secret scanner 误判为银行卡号。审计 payload 不重复 decision ID（由 `audit_events.entity_id` 承载）。

## 10. 审计

每个 Decision 写入不可变审计：

```json
{
  "decision_id": "dec-...",
  "request_id": "req-...",
  "actor": {"kind": "HARNESS", "id": "harness-1"},
  "account_id": "acc-...",
  "program_id": "prog-...",
  "asset_id": "asset-...",
  "action": "ACTIVE_HTTP",
  "decision": "ALLOW",
  "reasons": [],
  "gates_evaluated": [
    {"kind": "COMPLIANCE_EXAM", "satisfied": "SATISFIED", "blocking": true}
  ],
  "policy_version": "policy-2026-10-31.1",
  "constraints": {"max_rps": 2, "max_concurrency": 2},
  "expires_at": "2026-10-31T...",
  "decided_at": "2026-10-31T..."
}
```

## 11. 测试矩阵

必须覆盖：

1. 补天 `COMPLIANCE_EXAM=UNSATISFIED` → 所有提交 DENY，查看 ALLOW。
2. 补天 `PRACTICAL_CERTIFICATION=UNSATISFIED` → 专属SRC DENY，公益SRC 不受影响。
3. 先知 `Scope=GATED_BY_SIGNUP` → READ_SCOPE REQUIRE_HUMAN，ACTIVE_HTTP DENY。
4. 漏洞盒子 `Program.Status=PAUSED` → ACTIVE_HTTP DENY，VIEW_RULES ALLOW。
5. 淘天反爬 `AUTOMATED_SCAN=ALLOW` 但超日限额 → DENY。
6. 联想SRC `WEBSHELL_UPLOAD` → DENY。
7. `REPORT_SUBMIT` 无 HumanApproval → REQUIRE_HUMAN。
8. `HumanApproval` 过期 → REQUIRE_HUMAN。
9. `Scope.Visibility=UNKNOWN` → 不得当空 Scope；READ_SCOPE REQUIRE_MORE_EVIDENCE。
10. `ObservedAt` 超 TTL → REQUIRE_MORE_EVIDENCE。
11. `ACTIVE_HTTP` 缺 Asset / Asset 不在 Scope → DENY；Conditional Asset → REQUIRE_HUMAN。
12. `AutomationPolicy=FORBIDDEN`（如联想禁止 webshell 上传）→ DENY，人工审批也不能覆盖。
13. `REPORT_SUBMIT` 审批 single-use → 第二次使用 REQUIRE_HUMAN。
14. `DecisionContext` 的 account/program/platform 与请求不一致，或 `Asset.ID != Request.AssetID` → DENY。
15. `ContentsKnown=true` 且 `Include` 为空 → 任意资产 EXCLUDED；`*.example.com` 不匹配 apex。
16. `Program.Status.Confidence < 0.8` → 执行类 REQUIRE_MORE_EVIDENCE；查看类不受影响。
17. 同一 Evidence 重跑抽取幂等；同 Fact ID 不同内容 → 拒绝。

## 12. Control-plane hardening（2026-11-01）

- **声明式规则覆盖**：`internal/policy/rules` 在矩阵/automation 之后、人工批准之前求值；operator 规则内多规则最严合并。平台授权是独立的 ceiling：`operator` allow 只能保持已有 `ALLOW`；`source=platform_evidence` 的 JSON 文件被加载器拒绝，平台授权只能由 `internal/authz` 从 Evidence 编译为 `rules.PlatformRule` 并通过 `VerifyPlatformEvidence`。只有已验证、仍新鲜、revision 匹配且未被 operator 收紧的规则才能具体化 `CONDITIONAL`，且不能覆盖硬 `DENY`、`AUTOMATION_UNKNOWN/FORBIDDEN`、Scope/资产/资格/新鲜度/运行时约束。路径前缀 segment-aware（`/api` 不匹配 `/apix`），统一 `CanonicalPath`。规则文件加载时验证 `match`/`not_match`；决策记录 `RuleIDs`/`RuleSetFingerprint`（含平台授权 provenance），`Authorize` 拒绝规则/授权变更后的旧授权。`bountygrid policy check` 可离线验证 operator 规则文件，`can_relax` 恒为 false。
- **Authoritative Asset**：调用方只能提交 `AssetID`。Action Gateway 从 `Program.Scope.ResolveAsset` 取 canonical Asset；未存储的 ID → `DENY ASSET_UNKNOWN`，不再接受调用方提供的 Asset 对象。
- **Authorize 重新评估**：`ActionDecision` 持久化 `Request/ResourceID/UsageToday/BudgetSpent/Environment`。`Authorize` 重新加载 Account/Program、重新解析 Asset、重跑 Policy；任一门槛变化 → 执行前失败。旧格式（无 Request）Decision 只保留窄校验。
- **审批消费时点**：`Policy.Decide` 校验但不消费 single-use approval；`Gateway.Authorize` 在重新评估通过后调用 `ApprovalLedger.Consume`。审批过期/已消费 → 不执行。
- **Runtime constraints**：执行类动作检查 `AllowedTimeWindows`、`MaxRPS`、`MaxConcurrency`、`VPNRequired`、`DedicatedIPRequired`、`ResearcherHeaderRequired`、`TestAccountRequired`、`TestAccountReportingRequired`、`AllowedMethods/ForbiddenMethods`。缺 `ExecutionEnvironment` 证明 → `REQUIRE_MORE_EVIDENCE`；明确违反 → `DENY`。`AllowedEgressIPs` 在 `Constraints.Validate` 阶段用 `net/netip` 拒绝非法地址，运行时用规范化地址（`Unmap`）比较；时间窗 `TZ` 在 `Constraints.Validate` 阶段校验，业务时间由 `time/tzdata` 内嵌的 Asia/Shanghai 支撑。
- **账号能力映射**：`PROGRAM_SIGNUP` 要求 `CanJoinPrograms` 且 Program `RUNNING`；`PAYOUT` 要求 `CanPayout`；`REPORT_SUBMIT` 要求 `CanSubmit`。
- **Gate 运行时解析**：Program gate 只记录要求；`EvaluateEligibility` 用 `Account.PracticalCertification/JoinedProgramIDs/ComplianceExamCompleted/...` 覆盖 satisfaction。Reader 不得把 `PRACTICAL_CERTIFICATION` 或 `PROGRAM_SIGNUP` 冻结成 `UNSATISFIED`。
