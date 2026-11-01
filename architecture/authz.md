# Authorization Plane

## 1. 职责

`internal/authz` 是授权链上的可复用原语，不是新的决策引擎。它把此前散落在
Policy、Action Gateway、Verification、Submission 中的重复检查收敛成一份
fail-closed 实现，并把「平台授权」从普通规则文件里彻底分离出来。

它回答四类问题：

```text
Scope  这个 AssetID 在当前 Program.Scope 中是什么状态？
Fresh  这批授权输入（Status/Policy/Capability/Scope）现在还能用吗？
Bind   这份 scope/policy revision 还是当前 Program 的那一份吗？
Authority  这条 platform_evidence 规则真的来自证据，并且仍然有效吗？
```

`authz` 不执行动作、不生成 `ActionDecision`、不消费审批。

## 2. 平台授权不是规则文件

规则文件的 `source=operator`（默认）只是本地 guardrail：可以收紧，永远不能
抬高上限。此前 `source=platform_evidence` 可以从 JSON 加载，只靠几个字段
（`evidence_refs/observed_at/valid_until/policy_revision`）就获得
`CanRelax`。任何人都能手写一个看似合法、实际指向不存在证据或任意
`policy_revision` 的文件，从而把 `ACTIVE_HTTP=CONDITIONAL` 变成 `ALLOW`。
这是伪造平台授权。

现在的边界：

- `rules.Parse` / `rules.LoadFile` 遇到 `source=platform_evidence` 直接报错：
  platform authority is compiled from Evidence via `internal/authz`，不能来自
  手写 JSON。`rules/platform-evidence.example.json` 只作为负例保留。
- 平台授权使用独立类型 `rules.PlatformRule`，没有 JSON loader；字段不导出，
  只能通过 `rules.NewPlatformRule(PlatformRuleSpec)` 构造，且必须是
  evidence-bound、allow-only。
- `authz.BuildPlatformPolicy` 在构造 `PlatformPolicy` 前逐条调用
  `authz.VerifyPlatformEvidence`。验证不通过就没有 `PlatformPolicy`，也就没有
  `CanRelax`。
- `policy.NewEngineWithRules` 继续只接受 operator 规则；
  `Engine.WithPlatformAuthority` / `Engine.SetPlatformPolicy` 是唯一的平台授权
  入口。

`Source` 字符串只用于审计展示；权限判断依赖类型（`PlatformRule` /
`PlatformPolicy`）和验证结果，不依赖 `Rule.Source == "platform_evidence"`。

## 3. Evidence 验证

```go
type EvidenceStore interface {
    GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error)
}
```

`VerifyPlatformEvidence(ctx, rule, program, store, now)` 依次检查：

| 检查 | 失败结果 |
|------|----------|
| 配置了 evidence store | `PLATFORM_EVIDENCE_STORE_MISSING` |
| 规则绑定当前 Program（`ProgramID` 或 `Programs` 包含 `Program.ID`） | `PLATFORM_RULE_PROGRAM_MISMATCH` |
| `rule.PolicyRevision == Program.PolicyRevision()` | `POLICY_REVISION_MISMATCH` |
| `rule.ValidUntil > now` | `PLATFORM_RULE_EXPIRED` |
| `rule.ObservedAt <= now + ClockSkew`（1 分钟） | `PLATFORM_RULE_FROM_FUTURE` |
| `Program.PolicyFreshAt(now)` | `STALE_AUTOMATION_POLICY` |
| 每个 `evidence_refs` 都存在 | `PLATFORM_EVIDENCE_MISSING` |
| 每条 Evidence 的 `Platform == Program.Platform` | `PLATFORM_EVIDENCE_PLATFORM_MISMATCH` |

证据不存在、平台不符、revision 不符、过期都是**拒绝**，不是可重试错误。
只有存储/数据库故障以普通 error 返回，避免把临时故障误判成授权。

验证发生在 `BuildPlatformPolicy` 时；`PlatformPolicy.Evaluate` 在决策时再次
检查 `Program.Platform`、`Program.PolicyRevision()`、`Program.PolicyFreshAt`
以及规则自身的有效期。因此构建后 Program 改变或时间流逝都不会继续沿用旧
授权。

## 4. 与 Policy Engine 的集成

`Engine.Decide` 在矩阵、automation、scope/eligibility/新鲜度检查之后分别求值：

```text
operator rules   → 可能收紧；allow 永远不 set CanRelax
platform policy  → 仅 verified/fresh/revision-matching allow，set CanRelax
```

平台授权只能具体化**平台自己的 CONDITIONAL**：

```text
conditionalGate == true
  && rule.CanRelax
  && !operatorTightened
  && base != DENY
  && overridableByAutomation(action)
```

因此它不能覆盖硬 `DENY`、`AUTOMATION_UNKNOWN/FORBIDDEN`、Scope/资产/资格/
新鲜度/运行时约束，也不能覆盖 operator 的 `require_human`/`forbidden`。
operator allow 仍然只能保持已有 `ALLOW`。

决策记录 `RuleIDs` 与 `RuleSetFingerprint`。指纹覆盖 operator 规则和平台
授权的全部 provenance（evidence refs、观察窗口、policy revision、selector）。
任一变化都会让 `Gateway.Authorize` 拒绝绑定旧指纹的 decision。

## 5. 公共 helper

### ResolveInScopeAsset

```go
func ResolveInScopeAsset(program domain.Program, assetID domain.AssetID) (*domain.Asset, domain.ScopeMatch, error)
```

- 只接受 ID，Asset 的 value/program/platform 来自持久化 Scope；
- 返回 `Scope.CheckAsset` 的分类（INCLUDED/CONDITIONAL/EXCLUDED/UNKNOWN），
  因此 Access 未授权或 `ContentsKnown=false` 时是 UNKNOWN，不是 INCLUDED；
- 空 ID → `ErrAssetRequired`；Scope 中不存在 → `ErrAssetUnknown`；
- `ContentKnown=true` 且 `Include` 为空表示没有可执行资产。

Gateway、Verification、Submission 共用该函数，删除了三份重复的
`ResolveAsset + CheckAsset` 组合。

### CheckAuthorizationFreshness

```go
func CheckAuthorizationFreshness(program, account, action, now) error
```

返回 `*authz.FreshnessError`，reason 为既有的稳定码：
`STALE_PROGRAM_STATE`、`LOW_CONFIDENCE_STATE`、
`STALE_AUTOMATION_POLICY`、`STALE_ACCOUNT_CAPABILITY`、`STALE_SCOPE`。
查看类动作不要求新鲜度；执行/提交分别检查各自输入，任一 stale 都
`REQUIRE_MORE_EVIDENCE`。

### CheckRevisionBinding

```go
func CheckRevisionBinding(program, scopeRevision, policyRevision) error
```

空字符串表示该 revision 不绑定；非空且与 `Scope.Revision()` /
`PolicyRevision()` 不一致时返回 `*authz.RevisionError`，reason 为
`SCOPE_REVISION_MISMATCH` / `POLICY_REVISION_MISMATCH`。平台授权用它绑定
policy revision。

## 6. 失败语义

| 情况 | 结果 |
|------|------|
| 证据缺失 / 平台不符 / revision 不符 | 拒绝，无平台授权 |
| 规则过期 / observed_at 明显未来 | 拒绝 |
| Program policy 过期 | `STALE_AUTOMATION_POLICY`，无平台授权 |
| evidence store 未配置 | 拒绝 |
| evidence store 数据库错误 | 向上返回 error（不是授权） |
| Asset 为空/不在 Scope/Scope gated | fail closed（error + UNKNOWN） |

## 7. 测试与残余限制

覆盖：fake evidence 被拒；错误平台/错误 revision/过期/未来 observed 被拒；
verified platform rule 可放宽 CONDITIONAL；operator allow 仍不能；stale
Program policy 不能；operator guardrail 优先于平台授权；三个公共 helper 的
正向与反向用例；fingerprint 覆盖 provenance。`cmd/bountygrid policy check`
对 platform_evidence 文件返回加载错误。

残余限制：

- `PlatformPolicy` 在构建时验证 Evidence 存在；决策时不再逐条重读 Evidence
  （避免每个 decision 都打数据库）。Evidence 在构建与使用之间被删除属于
  运维时间窗问题，需要重新构建 policy。
- 平台授权尚未接入 CLI/DB 的自动编译流程；目前只有 Go API
  （`BuildPlatformPolicy` + `WithPlatformAuthority`）。
- `CheckRevisionBinding` 是显式 helper；Gateway 的 `Authorize` 仍以重跑
  Policy 为准，没有额外用旧 decision 的 revision 做二次拒绝。
- 一个 `PlatformPolicy` 只服务一个 Program；跨 program 复用会返回空授权。
