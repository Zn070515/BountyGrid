# Phase 1–5 High Review

- 日期：2026-11-01
- 范围：`d615a3c..HEAD`（Readers / Facts / State / Policy / Gateway / Opportunity / Harness / CLI）
- 结论：**通过；已应用 10 项修复 + 1 项复核后补充修复。**

## 1. 已修复

| 严重度 | 问题 | 修复 |
|---|---|---|
| P1 | Vulbox 缺失/零 `task_etime` 被转成 1970，导致 RUNNING 被派生为 ENDED | 只在时间戳 >0 时发 `program.start/end` Fact；`TestProgramFromProjectMissingEndDateIsNot1970` |
| P1 | Butian `parseCompliance` 把“本季度合规考核”本身当未完成，已完成页面被误判 | 只匹配明确的“合规考核未完成”；补充“已完成”回归 |
| P1 | Xianzhi `openList` 缓存导致后续从账号页/第 N 页读取 | 移除缓存，每次重开并 reload 到第 1 页 |
| P2 | Scheduler 企业SRC保留位替换可能突破 `MaxPerPlatform` | 替换前检查平台配额并维护计数；新增配额回归 |
| P2 | Butian `-program <native name>` 与存储 ID `butian/src/<hash>` 不匹配 | `SyncProgram` 同时匹配 `NativeID`；匹配后统一使用存储 ID |
| P2 | CompetitionSnapshot 缺 `Top10Share` 仍继承高 Confidence | 缺失 share 时 confidence 上限 0.5；新增回归 |
| P2 | Vulbox `closeOverlays` 在 home/myProject 无谓等待“已实名” | 仅 profile 页等待，并接受“未实名” |
| P2 | Vulbox scope 排除措辞只识别“暂不收录” | 识别“不收录”统一措辞 |
| P3 | CLAUDE.md 点击规则未覆盖只读关闭促销浮层 | 明确允许关闭只读促销浮层，仍禁止报名/NDA/提交 |
| P3 | `HumanResolvable` 文档注释错位 | 移回对应函数 |
| P2 | **复核后补充**：Butian native-name 命中后，Scope/Facts 仍用 `butian/<name>` | 归一化为存储 ID；新增 `TestSyncProgramNativeNameUsesStoredID` |

## 2. 验证

```text
go test -count=1 ./...        PASS
go vet ./...                  PASS
go build ./...                PASS
PostgreSQL integration        PASS
```

## 3. 已知未处理

- `opportunity.Input.Status/Account` 目前不参与打分（保留为 forward-compat）。
- `butian.SyncScope` 的 Scope 统一为 GATED；没有按 program ID 做差异（当前只会在项目大厅同步后调用）。
- 账号能力/Reader 数据缺口仍在：Xianzhi 实名、Butian 公益SRC/GROW、Vulbox 普通/高级详情。
