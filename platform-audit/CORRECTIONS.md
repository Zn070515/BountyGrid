# 审计纠正记录

- 纠正时间：2026-10-31
- 触发：用户复核 `platform-audit.zip`、JSON、Markdown 与截图
- 原则：原始观察值可追溯；纠正必须写清“原值 → 新值 → 证据”。

## 1. 补天 GROW 日期（重要）

### 原错误

多处把 GROW 写成：

```text
2026-10-01 ~ 2026-10-31
```

### 纠正后

```text
GROW 2026 整体报名：2026-08-25 ~ 2026-11-30
GROW 2026 活动时间：2026-09-01 ~ 2026-11-30
漏洞挖掘实战“第二期”：2026-10-01 ~ 2026-10-31（这是月度阶段，不是整个 GROW 的结束时间）
```

### 证据

- `butian/evidence/bt-grow-rule.png`：页面弹窗“AI 辅助漏洞挖掘规则”明确写：
  - 报名时间：2026.8.25~2026.11.30
  - 活动时间：2026.9.1~2026.11.30
- 补天活动中心：`2026GROW计划漏洞挖掘实战-第二期` 显示 `2026/10/01 - 2026/10/31`，仅是月度阶段的展示。
- 用户提供的补天官网链接同样显示战队报名到 11 月 30 日。

### 影响

- “现在来不及参加 GROW”是错误结论；实际还有约一个月。
- `programs.json` 的 GROW 条目已补 `signup_window` / `task_window`，并把名称改为
  “2026 GROW计划（AI辅助漏洞挖掘赛道 / 漏洞挖掘实战第二期）”。

## 2. 漏洞盒子经验值口径

### 原错误

```text
经验值 9/100；距下一级还差 81
```

`9 + 81 = 90`，与 `/100` 不一致；截图只明确显示经验值 9、下一等级初级白帽需经验值 81。

### 纠正后

```text
经验值 9；下一等级初级白帽需经验值 81；截图未显示总上限。
```

不再写死 `/100`。

## 3. 漏洞盒子项目总数

### 原错误

把分页推算的 `1513` 当成硬事实，且分类计数合计 `1511`。

### 纠正后

- `estimated_project_count: 1513`
- `count_confidence: "estimated"`
- `observed_category_counts_sum: 1511`
- 明确：相差 2 属于分页/筛选边界/推荐位差异，只能作量级参考，不能作为数据库强约束。

## 4. Eligibility 模型（架构级纠正）

### 原错误

把 eligibility 设计成单枚举：

```text
AVAILABLE_NOW | REQUIRES_REVIEW | REQUIRES_NDA | LOCKED_BY_REPUTATION | ...
```

无法表达“同时需要报名 + 审核 + NDA + 银行卡 + 历史信誉 + 学生认证”。

### 纠正后

```go
type EligibilityState struct {
    Decision      EligibilityDecision // ELIGIBLE | INELIGIBLE | HUMAN_ACTION_REQUIRED | UNKNOWN
    Gates         []EligibilityGate   // 每个门槛独立记录 required/satisfied/blocking/evidence
    EvaluatedAt   time.Time
    PolicyVersion string
}
```

并且把补天合规考核从 `program.gates` 移到 `AccountCapability`：

```go
type AccountCapability struct {
    CanSubmit       bool
    CanViewScope    bool
    CanPayout       bool
    BlockingReasons []EligibilityGate
}
```

详见 `comparison/unified-program-model.json`（schema_version 2.0）与 `architecture/policy-engine.md`。

## 5. 事实/判断分层（架构级纠正）

原模型直接存：

```json
{"status": "RUNNING", "end_date": "2026-10-31"}
```

纠正为：

```text
Evidence → ObservedFact → DerivedState → PolicyDecision
```

每个状态/日期都带 `observed_at`、`source/evidence_refs`、`confidence`、`rule_version`。抽取错了可回溯重算，不污染数据库。详见 `architecture/evidence-facts.md`。

## 6. 未改动的部分

- 三平台账号状态、已加入项目、提现/收款状态、AI 政策、测试红线、项目计数（除漏洞盒子总数标注 estimated 外）未改动。
- 未进行任何真实资产测试；未报名/未签NDA/未提交/未提现。
