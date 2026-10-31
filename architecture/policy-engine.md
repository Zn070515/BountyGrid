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

    // 8. 动作矩阵 / 人工批准
    if base == domain.DecisionDeny { return deny(req, "ACTION_DENIED"), nil }
    if req.Action.HumanOnly() && req.HumanApproval == nil {
        return requireHuman(req, "HUMAN_ONLY_ACTION"), nil
    }
    if base == domain.DecisionRequireHuman && req.HumanApproval == nil {
        return requireHuman(req, "APPROVAL_REQUIRED"), nil
    }
    if req.HumanApproval != nil {
        if err := validateApproval(req, dc.ResourceID); err != nil {
            return requireHuman(req, "INVALID_APPROVAL"), nil
        }
        if req.HumanApproval.SingleUse {
            if err := e.ledger.Consume(ctx, req.HumanApproval.ID); err != nil {
                return requireHuman(req, "APPROVAL_ALREADY_CONSUMED"), nil
            }
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

`REPORT_SUBMIT` 必须做到**单报告、单审批、单消费**：审批绑定 `resource_id` 与 `parameters_hash`，由 `ApprovalLedger` 原子消费；重复使用返回 `REQUIRE_HUMAN`，不能凭一张旧审批提交多份报告。产品实现使用 PostgreSQL `consumed_approvals`（`INSERT ... ON CONFLICT DO NOTHING`），进程重启后仍保持单次消费。

- 报告提交：审批时展示完整报告、AI披露、证据、影响、范围、重复风险。
- 报名/NDA/合规考核/认证：审批只代表“允许打开/发起人工流程”，不代表自动完成。
- 提现/绑卡：BountyGrid 不执行；只提示人工处理。

## 9. 决策缓存

- `ActionDecision` 可缓存，但必须带 `ExpiresAt`。
- `ExpiresAt` 取 `now+5m`、`Program.Status.ValidUntil`、`HumanApproval.ExpiresAt` 三者中最早的时间；决策不能比它所依据的状态或授权活得更久。
- ApprovalLedger 只有返回 `domain.ErrApprovalConsumed` 才映射为 `REQUIRE_HUMAN`；数据库/网络故障必须向上返回错误，不能伪装成“审批已使用”。
- 项目状态 TTL 内有效；状态变化/证据 supersede 时立即失效。
- 执行前 Action Gateway 必须二次校验 `ExpiresAt` 与 `Program.Status`。

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
