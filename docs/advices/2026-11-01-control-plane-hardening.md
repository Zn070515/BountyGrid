# Control-plane hardening（2026-11-01）

对 `advice.md`（Phase 1–5 复核）逐项修复的记录。目标不是加功能，而是让 **任何未来 Harness 都不可能绕过 Go 控制层**。

## 结论

- 阶段状态统一为：**Phase 0 complete; Phase 1–5 partially implemented; active execution remains disabled.**
- 本轮没有接入任何真实 ACTIVE_HTTP / 扫描 / 提交工具，也没有对厂商资产发起测试。
- `go test -count=1 ./...`、`go vet ./...`、`go build ./...` 通过；`migrate` 在本地 PostgreSQL 16 容器执行成功；`TestRepoRoundTrip`（含新增回执/任务/竞争/事务用例）通过。

## 逐项修复

| # | 问题 | 修复 |
|---|------|------|
| 1 | Asset 由调用方提供完整对象 | 删除 `EvaluateInput.Asset`；Gateway 从 `Program.Scope.ResolveAsset(AssetID)` 取 canonical Asset。未存储的 ID → `DENY ASSET_UNKNOWN`；`ResolveAsset` 的优先级是 exclude > conditional > include。 |
| 2 | Constraints 只存不执行 | 新增 `domain.ExecutionEnvironment`（RPS、并发、VPN、EgressIP、研究员 Header、测试账号、测试账号报备、HTTP method）。Policy 对执行类动作检查时间窗、RPS、并发、VPN、出口 IP、Header、测试账号、方法白名单；缺证明 → `REQUIRE_MORE_EVIDENCE`，明确违反 → `DENY`。新增 `Constraints.AllowedEgressIPs` 做精确出口 IP 校验。 |
| 3 | Sandbox 只交给 Tool 自律 | `ToolContext` 不再暴露 `Sandbox`；网络工具只拿到 `harness.Egress`，每次请求与重定向都执行 `Sandbox.CheckURL`。`NetworkEgress` 工具在空 allowlist 下直接拒绝。 |
| 4 | Authorize 不重新验证 | `ActionDecision` 持久化 `Request/ResourceID/UsageToday/BudgetSpent/Environment`。`Authorize` 重新加载 Account/Program、重新解析 Asset、重跑 Policy；Scope/Eligibility/Automation/状态变化后旧 ALLOW 失败。旧格式 Decision 保留窄校验。 |
| 5 | 审批在 Decide 时消费 | `Policy.Decide` 只校验 `HumanApproval`；`Gateway.Authorize` 在重评估通过后原子调用 `ApprovalLedger.Consume`。预算检查移到 Authorize 之前，避免预算拒绝烧掉审批。 |
| 6 | IdempotencyKey 未使用 | 新增 `domain.ExecutionReceipt` 与 `execution_receipts`（`idempotency_key` 唯一）。`NetworkEgress/WritesFiles/Destructive` 工具必须带 key；执行前预占 `STARTED`，成功转 `EXECUTED`→`COMPLETED`，失败转 `FAILED`。重试回放结果；`STARTED` 视为未决、拒绝重复执行。 |
| 7 | ResearchTask 覆盖历史、cooldown 未接通 | 任务 ID 改为 `task/<program>/<schedule-run>`；`PutResearchTask` 更新全部关系列，并在同一事务把旧 `QUEUED` 标为 `SUPERSEDED`；迁移增加 “每 Program 最多一个 QUEUED” 的部分唯一索引。`schedule` 通过 `LatestResearchTaskTimes` 从数据库读取 cooldown。 |
| 8 | Opportunity 覆盖历史 | Opportunity ID 改为 `opp/<program>/<evaluation-time>`，每次评分 append-only。 |
| 9 | CompetitionSnapshot 无过期 | 增加 `ValidUntil`；迁移回填 `observed_at + 30d`；查询过滤过期快照；Opportunity Engine 在 7 天后线性衰减 confidence，30 天后忽略，并记录 `competition_age_hours`/`competition_decay`。导入时校验 `top1 <= top3 <= top10`。 |
| 10 | State 可给旧 Fact 续命 | `ProgramInput` 记录所选最新 Fact 的最早 `observed_at`；`ValidUntil = min(now+TTL, oldest+30d)`，`ObservedAt` 使用输入观察时间；超过 30 天的 Fact 不能生成 State。 |
| 11 | PROGRAM_SIGNUP/PAYOUT 未纳入 Capability | `PROGRAM_SIGNUP` 要求 `CanJoinPrograms` 且 Program `RUNNING`；`PAYOUT` 要求 `CanPayout`。 |
| 12 | Butian 公益状态被写成 ObservedFact | 只发 `butian.pub.listed=true`；`RUNNING` 是 `Program.Status` 派生状态（0.8 confidence，Inputs 指向 listed Fact），不再发 `program.status/program.paused` Fact。 |
| 13 | Butian 实战认证永久 UNSATISFIED | 专属SRC 的 `PRACTICAL_CERTIFICATION` gate 改为 `UNKNOWN`（要求本身），Policy 在运行时用 `Account.PracticalCertification` 解析；`JoinedProgramIDs` 可满足 `PROGRAM_SIGNUP`。 |
| 14 | GROW 把“有报名时间”当成“未报名” | 只有明确 `立即报名/未报名` 才是 UNSATISFIED，`已报名/已加入/报名成功` 才是 SATISFIED，仅出现报名时间时为 UNKNOWN。 |
| 15 | Sidecar 被描述为只读 API | 包注释与 `Eval` 注释明确它是传输层、不是安全边界；Harness/Model 不得持有 Client。 |
| 16 | Harness 执行后审计失败可能重试执行 | Receipt 的 `EXECUTED` 状态允许重试只补审计、不重复执行；`STARTED` 状态拒绝重复执行；key 绑定工具名。 |
| 17 | Decision 与 Audit 非同一事务 | PostgreSQL `RecordDecisionWithAudit` 在同一事务写入 `action_decisions` 与 `audit_events`；Gateway 优先使用该接口。 |
| 18 | `uniqueFacts()` 静默丢弃冲突 | `uniqueFacts/uniqueEvidence` 同 ID 同内容去重、同 ID 不同内容报错。 |
| 19 | 文档阶段状态漂移 | README/CLAUDE/implementation-order/architecture 统一为真实阶段状态；新增本记录。 |

## 新增/更新测试

- Domain：`ResolveAsset` 优先级；CompetitionSnapshot 不变量。
- Policy：运行时约束（VPN/RPS/出口 IP/方法/时间窗）、`PROGRAM_SIGNUP`/`PAYOUT` capability、账号状态满足 Program gate、审批不在 Decide 消费。
- Gateway：Asset 从 Scope 解析、未知 AssetID、Authorize 重评估、审批在 Authorize 消费、Decision+Audit 事务接口、失败事务不落 Decision。
- Harness：副作用工具必须有 key、完成回执回放、审计失败只补审计、未决回执拒绝重复执行、网络工具空 allowlist 拒绝、Egress 对每个请求检查。
- Opportunity：append-only ID、过期快照忽略、旧快照置信度衰减。
- State：Fact 年龄上限、State 有效期不超过输入 Fact。
- Butian：公益列表只发 listed Fact、专属SRC/GROW 报名 gate 的三态推断。
- PostgreSQL 集成：Decision+Audit 事务、执行回执唯一键、竞争快照过期过滤、唯一 QUEUED 任务、DB cooldown 时间。

## 仍然不做

- 不接真实 `ACTIVE_HTTP` / 扫描 / 爆破 / 提交 / 提现工具。
- Model Router、Verification、Submission、Ledger 仍未进入实现。
- 外部 Harness 进程的 OS/container 级 egress 隔离未实现；当前强制边界是 Go Harness 的 `Egress` + allowlist，进程内工具仍可绕过 Go API（这是 Go 进程隔离的固有限制，接外部进程时应在网络命名空间层再收口）。
