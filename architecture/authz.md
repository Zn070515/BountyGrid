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
Authority  这条权限事实（PermissionFact）真的来自平台证据，并且仍然有效吗？
```

`authz` 不执行动作、不生成 `ActionDecision`、不消费审批。

## 2. 平台授权不是规则文件

规则文件的 `source=operator`（默认）只是本地 guardrail：可以收紧，永远不能
抬高上限。此前 `source=platform_evidence` 可以从 JSON 加载，只靠几个字段
（`evidence_refs/observed_at/valid_until/policy_revision`）就获得
`CanRelax`。任何人都能手写一个看似合法、实际指向不存在证据或任意
`policy_revision` 的文件，从而把 `ACTIVE_HTTP=CONDITIONAL` 变成 `ALLOW`。
这是伪造平台授权。

更隐蔽的缺口是「同平台证据 ≠ 授权」：只校验 Evidence ID 存在、平台一致，
并不能证明这条 Evidence 真的一条一条写出了
`ProgramID + Action + Host + Method + Path` 的许可。一个同平台的
**项目列表页**（`SourceType=PROGRAM`）完全可以被拿来编造一条宽泛的
平台 allow。

现在的边界是：

```text
Policy = operator guardrail（只能收紧）
       + Evidence-backed PermissionFact（唯一能放宽平台 CONDITIONAL 的授权）
```

- `rules.Parse` / `rules.LoadFile` 遇到 `source=platform_evidence` 直接报错：
  platform authority is compiled from Evidence via `internal/authz`，不能来自
  手写 JSON。`rules/platform-evidence.example.json` 只作为负例保留。
- 平台授权只接受 `authz.PermissionFact`。它是显式的权限事实：必须写明
  `EvidenceID + EvidenceContentHash + EvidenceQuote + Platform + ProgramID +
  Action + Hosts + Methods + PathPrefixes + ObservedAt + ValidUntil +
  PolicyRevision + Justification`。`Hosts` / `Methods` 不允许为空（空不等于
  “全部”），`Restrictions` 必须为空（非空表示条件型/人工复核，不是自动放宽）。
- `EvidenceQuote` 必须引用 Evidence 内容中的一条显式授权行：
  `PERMISSION program=<id> action=<ACTION> hosts=<h1,h2> methods=<GET,POST>
  [paths=/a,/b] [restrictions=<...>]`。`authz.ExtractPermissions` 按严格语法
  解析这些行；验证器要求 quote 精确匹配某一行、且 `ProgramID/Action/Hosts/
  Methods/PathPrefixes` 与该行完全一致。没有 PERMISSION 行、quote 不匹配、
  selector 对不上、行带 restrictions 都拒绝。页面正文本身不会被当作权限。
- `EvidenceContentReader` 提供 Evidence 的脱敏原文；验证器会重新计算
  `redact.Hash(content)` 并与 `Evidence.ContentHash` 比对。没有 content reader
  时 fail closed（`PLATFORM_EVIDENCE_CONTENT_MISSING`），不能只凭 EvidenceID
  和哈希声明权限。默认实现 `authz.FileContentReader` 只读 `RawRef` 指向的
  本地脱敏文件：相对路径固定在 `-content-root` 下解析、拒绝越界路径/符号链接、
  只接受普通文件且有 8 MiB 上限，文件不存在映射为 `domain.ErrNotFound`。
- `rules` 包不再暴露 `PlatformRule` / `PlatformRuleSpec` / `NewPlatformRule`
  这类可由调用方随意构造的公共路径；operator 规则仍是 JSON-only，平台授权
  只能走 `authz.BuildPlatformPolicy(ctx, program, permissionFacts, store, contents, now)`。
- `authz.BuildPlatformPolicy` 在构造 `PlatformPolicy` 前逐条调用
  `authz.VerifyPlatformEvidence`。验证不通过就没有 `PlatformPolicy`，也就没有
  `CanRelax`。
- `policy.NewEngineWithRules` 继续只接受 operator 规则；
  `Engine.WithPlatformAuthority` / `Engine.SetPlatformPolicy` 是唯一的平台授权
  入口。

`Source` 字符串只用于审计展示；权限判断依赖类型（`PermissionFact` /
`PlatformPolicy`）和验证结果，不依赖 `Rule.Source == "platform_evidence"`。

## 3. Evidence 验证

```go
type EvidenceStore interface {
    GetEvidence(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error)
}
type EvidenceContentReader interface {
    ReadContent(ctx context.Context, evidence domain.Evidence) (string, error)
}
```

`VerifyPlatformEvidence(ctx, permission, program, store, contents, now)` 依次检查：

| 检查 | 失败结果 |
|------|----------|
| 配置了 evidence store | `PLATFORM_EVIDENCE_STORE_MISSING` |
| `permission.ProgramID == Program.ID` | `PLATFORM_PERMISSION_PROGRAM_MISMATCH` |
| `permission.Platform == Program.Platform` | `PLATFORM_EVIDENCE_PLATFORM_MISMATCH` |
| `permission.PolicyRevision == Program.PolicyRevision()` 且非空 | `PLATFORM_PERMISSION_REVISION_MISSING` / `POLICY_REVISION_MISMATCH` |
| `permission.ValidUntil > now` | `PLATFORM_PERMISSION_EXPIRED` |
| `permission.ObservedAt <= now + ClockSkew`（1 分钟） | `PLATFORM_PERMISSION_FROM_FUTURE` |
| `Program.PolicyFreshAt(now)` | `STALE_AUTOMATION_POLICY` |
| `permission.EvidenceID` 存在 | `PLATFORM_EVIDENCE_MISSING` |
| `Evidence.Platform == Program.Platform` | `PLATFORM_EVIDENCE_PLATFORM_MISMATCH` |
| `Evidence.ContentHash == permission.EvidenceContentHash` 且非空 | `PLATFORM_EVIDENCE_CONTENT_HASH_MISMATCH` |
| `Evidence.SourceType` 是 `POLICY` 或 `ANNOUNCEMENT` | `PLATFORM_EVIDENCE_SOURCE_TYPE_NOT_AUTHORIZING` |
| 配置了 content reader 且内容存在 | `PLATFORM_EVIDENCE_CONTENT_MISSING` |
| `redact.Hash(content) == Evidence.ContentHash` | `PLATFORM_EVIDENCE_CONTENT_HASH_MISMATCH` |
| `EvidenceQuote` 非空且精确匹配一条 PERMISSION 行 | `PLATFORM_PERMISSION_QUOTE_MISSING` / `PLATFORM_PERMISSION_NOT_IN_EVIDENCE` |
| `ProgramID/Action/Hosts/Methods/PathPrefixes` 与引用行一致 | `PLATFORM_PERMISSION_NOT_IN_EVIDENCE` |
| 引用行没有 `restrictions` | `PLATFORM_PERMISSION_RESTRICTIONS_PRESENT` |
| `Action.Valid()` | `PLATFORM_PERMISSION_ACTION_INVALID` |
| `Hosts` 非空且每个 host pattern 合法 | `PLATFORM_PERMISSION_HOSTS_MISSING` / `PLATFORM_PERMISSION_HOST_INVALID` |
| `Methods` 非空且每个 method 合法 | `PLATFORM_PERMISSION_METHODS_MISSING` |
| 每个 `PathPrefixes` 可规范化 | `PLATFORM_PERMISSION_PATH_INVALID` |
| `Restrictions` 为空 | `PLATFORM_PERMISSION_RESTRICTIONS_PRESENT` |
| `ObservedAt/ValidUntil` 完整且 `ValidUntil > ObservedAt` | `PLATFORM_PERMISSION_OBSERVATION_INVALID` |
| `Justification` 非空 | `PLATFORM_PERMISSION_JUSTIFICATION_MISSING` |

`PROGRAM`（项目列表/项目详情）、`ACCOUNT`、`PAYOUT`、`SCREENSHOT` 都不是平台
授权来源。它们可以驱动事实与状态，但不能直接产生 `CanRelax`。同平台、存在、
未过期都不够；Evidence 必须声明来源类型为 `POLICY` / `ANNOUNCEMENT`，并且
权限事实必须绑定到该 Evidence 的**内容哈希**。

证据不存在、平台不符、content hash 不符、source type 不可授权、revision 不符、
带 Restrictions、过期都是**拒绝**，不是可重试错误。只有存储/数据库故障以普通
error 返回，避免把临时故障误判成授权。

验证发生在 `BuildPlatformPolicy` 时；`PlatformPolicy.Evaluate` 在决策时再次
完整校验每条 `PermissionFact` 及其 Evidence（存在、平台、content hash、
source type、ProgramID/revision/新鲜度）。因此构建后 Evidence 被删除、被替换、
内容变化、source type 改变，或 Program/时间变化，都会立即移除授权，而不是
沿用旧快照。

## 4. 与 Policy Engine 的集成

`Engine.Decide` 在矩阵、automation、scope/eligibility/新鲜度检查之后分别求值：

```text
operator rules      → 可能收紧；allow 永远不 set CanRelax
platform permissions → 仅 verified/fresh/revision/hash-matching 的 PermissionFact，set CanRelax
```

平台授权只能具体化**平台自己的 CONDITIONAL**：

```text
conditionalGate == true
  && permission.CanRelax
  && !operatorTightened
  && base != DENY
  && overridableByAutomation(action)
```

因此它不能覆盖硬 `DENY`、`AUTOMATION_UNKNOWN/FORBIDDEN`、Scope/资产/资格/
新鲜度/运行时约束，也不能覆盖 operator 的 `require_human`/`forbidden`。
operator allow 仍然只能保持已有 `ALLOW`。

决策记录 `RuleIDs` 与 `RuleSetFingerprint`。指纹覆盖 operator 规则和每条
`PermissionFact` 的全部 provenance（evidence id/hash、观察窗口、policy
revision、selector、Restrictions、justification）。任一变化都会让
`Gateway.Authorize` 拒绝绑定旧指纹的 decision。

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
policy revision（`PermissionFact.PolicyRevision` 本身不允许为空）。

## 6. 失败语义

| 情况 | 结果 |
|------|------|
| 证据缺失 / 平台不符 / content hash 不符 / source type 不可授权 / revision 不符 | 拒绝，无平台授权 |
| 未写明 Hosts/Methods；Action 非法；PathPrefixes 非法 | 拒绝，无平台授权 |
| Restrictions 非空（条件型/人工复核） | 拒绝，无平台授权 |
| 权限过期 / observed_at 明显未来 | 拒绝 |
| Program policy 过期 | `STALE_AUTOMATION_POLICY`，无平台授权 |
| evidence store 未配置 | 拒绝 |
| evidence store 数据库错误 | 向上返回 error（不是授权） |
| Asset 为空/不在 Scope/Scope gated | fail closed（error + UNKNOWN） |

## 7. 测试与残余限制

覆盖：同一平台的 `SourceProgram` 证据被拒；`ACCOUNT`/`PAYOUT`/`SCREENSHOT`
证据被拒；content hash 不符被拒；Hosts/Methods 缺失被拒；host/path 非法被拒；
Restrictions 非空被拒；Action/revision/observation/justification 缺失被拒；
合法 `POLICY` / `ANNOUNCEMENT` 权限被接受；决策时删除 Evidence、改变 content
hash、改变 source type 都会移除 `CanRelax`；operator allow 仍不能抬高平台
ceiling；平台授权不能覆盖 operator guardrail。`cmd/bountygrid policy check`
对 platform_evidence 文件返回加载错误。

残余限制：

- `PlatformPolicy` 在构建时和每次决策时都会重新读取并校验 Evidence，但按
  EvidenceID 在一次调用内缓存：同一政策页被多条 permission 引用时只读一次，
  不是每条 permission 一次。这是有意选择：删除/替换证据必须在下一次决策立即
  移除授权，缓存不能跨调用存活。
- 平台授权已有 CLI 编译入口：`bountygrid policy compile -program <id>
  [-evidence <ids>] [-content-root .]` 通过 `FileContentReader` 读取 `.evidence/`
  下的脱敏原文，`CompilePermissions` 把显式 `PERMISSION` 行转换成
  `PermissionFact`，再由 `BuildPlatformPolicy` 验证并输出
  `platform_policy_fingerprint`。`bountygrid decide -platform-evidence <ids>`
  把同一结果挂到决策引擎（`-request` 与 `-authorize` 都可；`-authorize` 从存储
  决策取 ProgramID 并用同一批 evidence 重建 fingerprint）；该 flag 是显式
  opt-in，仍不能覆盖硬 DENY、operator guardrail 或任何 domain gate。非 `POLICY`/`ANNOUNCEMENT` 证据列为 skipped，
  证据读取失败、PERMISSION 行格式错误或验证失败都直接报错（fail closed）。
- Reader 仍不会从纯正文推断权限。平台专用 Reader 需要把页面中明确写出的
  host/method 许可转换成 `PERMISSION` 行；没有该行的政策页不会产生任何授权，
  这是有意的 fail-closed 选择。
- `CheckRevisionBinding` 是显式 helper；Gateway 的 `Authorize` 仍以重跑
  Policy 为准，没有额外用旧 decision 的 revision 做二次拒绝。
- 一个 `PlatformPolicy` 只服务一个 Program；跨 program 复用会返回空授权。
- `Restrictions` 目前只有「拒绝」这一条路径；未来需要把带条件的权限建模为
  人工复核队列，而不是自动 `CanRelax`。
