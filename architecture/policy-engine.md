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
| READ_SCOPE | ALLOW if PUBLIC；否则 REQUIRE_HUMAN | GATED_BY_SIGNUP/NDA/APPROVAL |
| PASSIVE_ANALYSIS | ALLOW | Scope PUBLIC/已授权；不得主动请求目标 |
| ACTIVE_HTTP | CONDITIONAL | Program RUNNING；Scope 授权；Constraints 满足 |
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

## 7. 决策算法

```go
func (e *Engine) Decide(ctx context.Context, req domain.ActionRequest) (domain.ActionDecision, error) {
    dc, err := e.loadContext(ctx, req)
    if err != nil { return domain.ActionDecision{}, err }

    // 0. 证据新鲜度
    if !e.evidenceFresh(dc) {
        return deny(req, "STALE_EVIDENCE"), nil
    }

    // 1. 人工专属动作
    if isHumanOnly(req.Action) && req.HumanApproval == nil {
        return requireHuman(req, "HUMAN_ONLY_ACTION"), nil
    }

    // 2. 账号能力
    if blocksAccount(req.Action, dc.Account.Capability) {
        return denyWith(req, "ACCOUNT_CAPABILITY", dc.Account.Capability.BlockingReasons), nil
    }

    // 3. 项目状态
    if req.Action != domain.ActionViewProgram &&
       req.Action != domain.ActionViewRules &&
       dc.Program.Status.Status != domain.StatusRunning {
        return deny(req, "PROGRAM_NOT_RUNNING"), nil
    }

    // 4. 项目门槛
    switch dc.Program.Eligibility.Decision {
    case domain.Ineligible:
        return deny(req, "PROGRAM_INELIGIBLE"), nil
    case domain.HumanActionNeeded:
        return requireHuman(req, "PROGRAM_GATE"), nil
    case domain.EligibilityUnknown:
        return requireMoreEvidence(req, "PROGRAM_GATE_UNKNOWN"), nil
    }

    // 5. Scope 可见性
    if actionNeedsScope(req.Action) && dc.Program.Scope.Visibility != domain.ScopePublic {
        return deny(req, "SCOPE_GATED"), nil
    }

    // 6. 动作矩阵
    base := e.actionMatrix[req.Action]
    if base == DecisionDeny { return deny(req, "ACTION_DENIED"), nil }

    // 7. 项目/平台约束
    if err := e.checkConstraints(dc); err != nil {
        return deny(req, err.Error()), nil
    }

    // 8. 报告完整性
    if req.Action == domain.ActionReportSubmit && !e.reportComplete(dc) {
        return requireMoreEvidence(req, "REPORT_INCOMPLETE"), nil
    }

    // 9. 人工批准
    if base == DecisionRequireHuman && req.HumanApproval == nil {
        return requireHuman(req, "APPROVAL_REQUIRED"), nil
    }

    return domain.ActionDecision{
        Decision: domain.DecisionAllow,
        Constraints: &dc.Program.Constraints,
        ExpiresAt: ptr(now().Add(e.decisionTTL(req.Action))),
    }, nil
}
```

## 8. 人工审批

`HumanApproval` 必须绑定：

```text
approval_id
approved_by（人工账号）
approved_at
action（只能覆盖单个动作）
program_id
scope（项目/资产/时间窗）
comment
expires_at（默认 24h，提交类可 1h）
```

- 报告提交：审批时展示完整报告、AI披露、证据、影响、范围、重复风险。
- 报名/NDA/合规考核/认证：审批只代表“允许打开/发起人工流程”，不代表自动完成。
- 提现/绑卡：BountyGrid 不执行；只提示人工处理。

## 9. 决策缓存

- `ActionDecision` 可缓存，但必须带 `ExpiresAt`。
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
