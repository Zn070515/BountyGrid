# Phase 0 + core kernel hardening（对 advice.md 的修复记录）

- 日期：2026-10-31
- 触发：`advice.md` 复核结论“不通过，不要进入 Opportunity/Scheduler/Harness”
- 范围：不推翻架构，只修 P1/P2，先把安全不变量固定下来

## 1. 修复对照

| 复核项 | 修复 | 位置 / 测试 |
|---|---|---|
| Policy 对所有动作统一评估 Program Eligibility | 新增 `Action.EligibilityRequired()`；只有参与类动作评估 Eligibility；`VIEW_PROGRAM/VIEW_RULES` 不受影响 | `internal/domain/platform.go`、`internal/policy/policy.go`、`TestViewRulesAllowedDespiteIneligible` |
| `READ_SCOPE` 被当作 `NeedsScope()`，GATED 时被 DENY | `NeedsScope()` 移除 `READ_SCOPE`；GATED 时 `REQUIRE_HUMAN`，`Access=GRANTED && ContentsKnown` 时 `ALLOW` | `internal/domain/platform.go`、`internal/policy/policy.go`、`TestXianzhiGatedScopeReadRequiresHuman`、`TestXianzhiUnlockedScopeReadAllowed` |
| Policy 不校验 Asset / Scope | `DecisionContext.Asset`；`Action.NeedsAsset()`；`Scope.CheckAsset` 返回 INCLUDED/CONDITIONAL/EXCLUDED/UNKNOWN；缺失/跨 Program/Scope 外分别 DENY | `internal/domain/scope.go`、`internal/policy/policy.go`、`TestAssetRequiredAndOutOfScope` |
| Policy 不消费 AutomationPolicy | Action → capability 映射；`FORBIDDEN` 直接 DENY 且不可被审批覆盖；`ALLOW` 可覆盖保守矩阵；`CONDITIONAL` 需人工；`UNKNOWN` 要求更多证据 | `internal/policy/policy.go`、`TestAutomationForbiddenCannotBeOverridden`、`TestProjectAutomationCanAllowScan` |
| State 丢失 Fact/Evidence provenance | `ProgramInput` 记录 `FactIDs/Evidence`；`ProgramState` 带 `Inputs/Evidence`；`DerivationTracker` 在 Fact supersede 时失效下游 State | `internal/state/derive.go`、`internal/state/tracker.go`、`TestProgramStateKeepsProvenance`、`TestTrackerInvalidatesByFact` |
| Scope 只有 Visibility，无法表达“报名后已解锁” | 新增 `Scope.Access`（GRANTED/NOT_GRANTED/UNKNOWN）；`Visibility` 保留为 Disclosure；`GATED_BY_SIGNUP + GRANTED + ContentsKnown` 合法 | `internal/domain/program.go`、`internal/domain/scope.go`、`internal/domain/invariants.go` |
| HumanApproval 过宽 | 绑定 `RequestID/Action/ProgramID/AssetID/ResourceID/ParametersHash/ExpiresAt/SingleUse`；`ApprovalLedger` 一次性消费；PostgreSQL `consumed_approvals` 提供 durable 单次消费；`REPORT_SUBMIT` 必须 resource+single-use | `internal/domain/action.go`、`internal/policy/policy.go`、`internal/store/postgres/approval_ledger.go`、`TestApprovalIsSingleUse`、`TestExpiredApprovalRejected` |
| Phase 0 无 Repository / rollback | 新增 account/program/evidence/fact/state/decision/opportunity/task/finding/report/submission/ledger/audit 仓储；`0001_init.down.sql`、`0002_constraints.down.sql`；`rollback` CLI | `internal/store/postgres/repos.go`、`stores.go`、`repos_integration_test.go`、`cmd/bountygrid` |
| Evidence/Audit 浅拷贝、无 hash/redaction/scan | `redact` 包提供 Scan/Redact/Hash；Evidence `Prepare` 脱敏+hash；Evidence/Audit 深拷贝；Audit 拒绝含敏感值的 payload | `internal/redact/`、`internal/evidence/store.go`、`internal/audit/event.go`、`TestPrepareRedactsAndHashes`、`TestMemoryStoreClonesMutableFields` |
| Fact Store batch 部分成功 / latest / supersede | 批量先 Validate/查重再写入；`FactSet.find` 按 ObservedAt 最新；Supersede 拒绝 self/cycle/subject-predicate mismatch | `internal/facts/store.go`、`internal/facts/facts.go`、`internal/facts/store_test.go` |
| GROW 截止日 00:00 UTC 导致当天上午结束；国内时区 | 日期按 `Asia/Shanghai`；date-only 结束日 `23:59:59.999999999`；`Unit=date`；`DeriveRegistrationOpen` 使用 inclusive end | `internal/facts/butian/grow.go`、`internal/state/derive.go`、`TestGrowInclusiveEnd` |
| PracticalCertification 全局阻断 | 从 `DeriveAccountCapability` 移除；Gate 增加 `ProgramID`（nil=账号级，非 nil=仅该项目）；`EvaluateEligibility` 过滤 | `internal/domain/account.go`、`internal/state/derive.go`、`internal/policy/eligibility.go`、`TestPracticalCertificationDoesNotBlockGeneralSubmit`、`TestPracticalCertificationIsProgramScoped` |
| Policy 缺 Asset/Competition | `DecisionContext` 增加 `Asset`、`Competition`、`UsageToday`、`BudgetSpent` | `internal/policy/policy.go` |
| 数据库无 CHECK | `0002_constraints.sql`：platform/status/confidence/evidence_refs/amount/currency/receipt/approval 等约束 | `internal/store/postgres/migrations/0002_constraints.sql` |
| 无 CI 证明 | `.github/workflows/ci.yml`：PostgreSQL service + migrate + test + vet + build | `.github/workflows/ci.yml`、README CI badge |
| Phase 编号漂移 | 文档恢复 canonical：Phase 0 完成；Phase 1 Readers 未开始；Phase 2/3 内核提前硬化；不把 Reader 改叫 Phase 2 | `architecture/implementation-order.md`、README、CLAUDE.md |

## 2. 仍未做的事（明确）

- **Phase 1（Browser Sidecar + 三个平台 Reader）尚未开始。** 本机探针存在，但不是产品 Adapter。
- daily limit / budget 已进入 Policy 输入与测试；真实的 usage 计数器、时间窗、VPN/测试账号报备仍由后续 Adapter/Harness 提供。
- CompetitionSnapshot 已在模型中，Opportunity/Scheduler 尚未实现。
- 提交/收款仍全部人工；本仓库没有自动提交/提现代码。

## 3. 验证

```text
go test -count=1 ./...        PASS
go vet ./...                  PASS
go build ./...                PASS
TEST_DATABASE_URL=... go test ./internal/store/postgres -run TestRepoRoundTrip   PASS
go run ./cmd/bountygrid migrate   PASS
go run ./cmd/bountygrid rollback  PASS (0002, then 0001)
```

CI 在 `.github/workflows/ci.yml`，使用 PostgreSQL 16 service；以 GitHub Actions 运行结果为准。
