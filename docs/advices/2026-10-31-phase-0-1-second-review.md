# Phase 0 + 内核第二次复核

- 日期：2026-10-31
- 范围：canonical Phase 0（domain/evidence/PostgreSQL/CLI）与提前实现的 Phase 2/3 内核（audit/facts/state/policy）
- 结论：**有 4 个 P1、12 个 P2，已在本分支修复；修复后不阻断进入下一阶段。**

## 1. 复核结论

上一轮 hardening 的 P1 均已落地，方向正确。本轮复核发现的新问题集中在**“边界输入是否自洽”**和**“产品存储是否与内存实现同强度”**，不是架构问题。

| 严重度 | 问题 | 影响 | 修复 / 测试 |
|---|---|---|---|
| **P1** | Policy 不校验 `Asset.ID == Request.AssetID` | 请求 a2、上下文传 in-scope a1 时，Scope 校验通过，ALLOW 被记到 a2 上；越界执行缺口 | `Decide` 绑定 ID/Program/Platform；`TestPolicyBindsAssetToRequest` |
| **P1** | Policy 不校验 `DecisionContext` 与 `ActionRequest` 的 account/program/platform 一致 | 用一个账号/项目的结论服务另一个请求 | 上下文不一致直接 `DENY CONTEXT_MISMATCH` / `PLATFORM_MISMATCH`；`TestPolicyRejectsContextMismatch` |
| **P1** | PostgreSQL `AuditStore.Append` 不调用 `Validate` | 产品路径可把手机号等敏感值写进审计表；内存实现却会拒绝 | Append 前校验 + 脱敏扫描；集成测试覆盖 |
| **P1** | `AutomationPolicy=CONDITIONAL` 把硬拒绝动作（DoS/内网/横向）降为 `REQUIRE_HUMAN` | 一张人工审批即可把硬拒绝变成 ALLOW；且 DoS=ALLOWED 反而比 CONDITIONAL 更严 | 只在 base 不是 DENY 时降级；`TestConditionalAutomationCannotLoosenHardDeny` |
| P2 | ALLOW 的 `ExpiresAt` 固定为 now+5m，不收敛到状态/审批的到期时间 | 决策可以比它所依据的 Program 状态或人工授权活得更久 | 取 status/approval 到期时间的最小值；`TestDecisionExpiryCappedByStateAndApproval` |
| P2 | ApprovalLedger 的任何错误都折叠成 `APPROVAL_ALREADY_CONSUMED` | 数据库故障被伪装成“审批已用”，错误被吞掉 | 只在 `domain.ErrApprovalConsumed` 时返回 REQUIRE_HUMAN，其余返回 error；`TestApprovalLedgerFailureSurfaces` |
| P2 | `PutState` 冲突时只更新 state/inputs/valid_until | 重算后的 confidence/rule_version/derived_at 仍是旧值 | 冲突时刷新全部派生列；集成测试覆盖 |
| P2 | `Tracker.Record` 重用 StateID 时未清理旧依赖 | supersede 一个已不再使用的 Fact 会错误失效当前 State | 重新记录前移除旧映射；`TestTrackerRerecordMovesDependencies` |
| P2 | `Scope.CheckAsset` 在 `ContentsKnown=true`、`Include` 为空时返回 `INCLUDED` | 空 Scope 被解释为“全部资产”，fail-open | 返回 `EXCLUDED`；`TestCheckAssetFailsClosedOnEmptyInclude` |
| P2 | `*.example.com` 匹配 apex `example.com` | 静默放大 Scope | 通配符只匹配子域；`TestCheckAssetWildcardDoesNotMatchApex` |
| P2 | `Account.Validate` 把 Program-scoped gate 当作账号级阻断 | 补天实战认证会让合法账号对象无法通过校验 | 跳过 `ProgramID != nil` 的 gate；`TestAccountProgramScopedGateDoesNotInvalidateCanSubmit` |
| P2 | `DeriveProgramState` 硬编码 `Confidence=0.9` | 低置信 Fact 可以驱动执行决策，违反 `<0.8 不得单独驱动执行` | `ProgramInput` 传播输入 Fact 最小值；Policy 要求 `>=0.8`；`TestProgramStateUsesLowestInputConfidence`、`TestLowConfidenceStateBlocksExecution` |
| P2 | Fact 重跑不幂等；同 ID 不同内容语义未定义；supersede 允许跨实体 | 与 implementation-order 2.1 “同一 Evidence 重跑幂等”冲突，可能写坏 fact 链 | 同内容 no-op、不同内容报错；supersede 绑定 subject/predicate/实体；内存与 PostgreSQL 对齐 |
| P2 | Repository 写入前不校验 Domain；`PutEvidence/PutDecision/PutState` 无校验 | 非法对象可绕过 Go 侧不变量 | 写入前 `Validate` |
| P2 | 非法 daily limit / budget cap 行为不确定；预算币种不一致被静默跳过 | 0/负数配置可能全拒或 fail-open | 配置校验 + fail-closed；`TestInvalidConstraintConfigFailsClosed`、`TestBudgetCurrencyMismatchFailsClosed` |
| P2 | Domain 不变量缺口：Fact/State 无 ID 校验；`ALLOW` 无 `ExpiresAt`；human-only 审批不要求 single-use | 不可执行/可复用的决策可被构造 | 补齐不变量；`TestFactRequiresID`、`TestActionDecisionAllowRequiresExpiry`、`TestActionDecisionHumanOnlyApprovalMustBeSingleUse` |

## 2. 验证

```text
go test -count=1 ./...                                  PASS（含 TEST_DATABASE_URL 集成测试）
go vet ./...                                            PASS
go build ./...                                          PASS
migrate -> rollback 0003 -> 0002 -> 0001 -> migrate     PASS（临时库 bountygrid_review）
gh run list（HEAD cdc6795）                              CI success
```

`go test -race` 在本机 Windows 上以 `STATUS_ENTRYPOINT_NOT_FOUND (0xc0000139)` 退出，属工具链/运行库环境问题，CI 未启用 race。

## 3. 仍未处理（交给 Reader/State 接入阶段）

1. **DerivedState 的新鲜度只看 `ValidUntil`，不检查输入 Fact 的 `ObservedAt`。** 重新派生旧 Fact 仍会得到新的 TTL；显式 `program.status` Fact 也能覆盖已过期的结束日期。需要按来源定义最大 Fact 年龄或在 Adapter 同步时强制刷新。
2. **`registration_open` 没有显式关闭 Fact。** 页面写“报名已结束”但没日期时，当前规则只能返回 unknown。
3. **`DerivationTracker` 未接入 Store 管线。** supersede 后 `derived_states` 不会自动失效；目前只有内存 Tracker 的单测。
4. **CompetitionSnapshot / Verification 没有仓储实现**（属 Phase 4/6）。
5. **Decision ID 使用 `UnixNano`**，同请求同纳秒可能碰撞；接入 Action Gateway 时应换成 UUID/序列。
6. **GROW 的 phase 正则在全文取第一个 `YYYY/MM/DD - YYYY/MM/DD`**；如果报名/活动日期用斜杠书写，可能先匹配到错误区间。抽取器失败应保留 Evidence 人工修复。
7. **PostgreSQL 的 append-only 仍是代码约定**，没有 trigger/权限层强制。
8. **canonical Phase 1（Browser Sidecar + Readers）仍未开始。** `implementation-order.md` 的“下一步 Phase 4”与依赖顺序不一致，进入下一阶段前应先确认编号。

## 4. 建议下一步

先做 canonical **Phase 1：Browser Sidecar + Vulbox Reader**（只读），用真实 Account/Program/Scope/Policy 快照回填 Phase 2 Fact，再谈 Opportunity/Scheduler。不要用合成数据先做 Phase 4 排序，否则会把未验证的 Scope/资格假设固化进调度。
