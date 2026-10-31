# Competition、Opportunity 与 Scheduler

## 1. 为什么竞争数据必须进入调度

“最高赏金 100,000 元”是宣传数字，不是期望收益。真正决定是否值得投入的是：

```text
期望收益 = P(有效) × E[奖金 | 有效] × (1 - 竞争惩罚)
期望成本 = 研究时间 + 模型/工具成本 + 人工审核 + 风险成本
排序依据 = 期望收益 / 期望成本 × 置信度
```

竞争数据（Top10 集中度、重复压力、饱和度）直接进入分母/惩罚项，不是展示字段。

## 2. CompetitionSnapshot

```go
type CompetitionSnapshot struct {
    ID                    string
    Platform              domain.Platform
    Scope                 string // "PLATFORM" | "PROGRAM"
    ProgramID             *domain.ProgramID
    ObservedAt            time.Time

    ActiveResearchers     *int
    RewardedResearchers   *int

    Top1Share             *float64
    Top3Share             *float64
    Top10Share            *float64

    NewcomerPool          *bool
    NewcomerRankingExists *bool

    DuplicatePressure     *float64 // 0..1
    Saturation            *float64 // 0..1
    RewardPool            *domain.Money

    Confidence            domain.Confidence
    Evidence              []domain.EvidenceID
}
```

### 2.1 指标定义

| 指标 | 定义 | 来源 |
|------|------|------|
| ActiveResearchers | 榜单可见活跃人数 | 排行榜 |
| RewardedResearchers | 有赏金/有效漏洞人数 | 排行榜 |
| Top1Share | 第1名赏金 / 可见总赏金 | 排行榜 |
| Top3Share | 前3名赏金 / 可见总赏金 | 排行榜 |
| Top10Share | 前10名赏金 / 可见总赏金 | 排行榜 |
| NewcomerPool | 是否有新秀/新人榜或新人活动 | 平台页面 |
| DuplicatePressure | 重复/同源合并规则的严格度 + 历史重复率 | 规则 + 历史（初期用规则估计） |
| Saturation | 活跃人数 × 奖励池/项目数 的拥挤度 | 排行榜 + 项目数 |
| RewardPool | 奖金池/预算上限 | 活动页/项目页 |

### 2.2 已观察样本

| 平台 | 数据 | 解释 |
|------|------|------|
| 先知 | 2026-10 Top10 赏金占可见总额 70.8%；第1名3,900，第10名300 | 头部集中；中尾部收益快速衰减 |
| 漏洞盒子 | Top10 全为传说；积分9,276~2,500；30位传说分30,000池 | 段位垄断；新人需走企业SRC/闪测 |
| 补天 | 赏金猎人 Top10 合计164,012元；战神榜 Top1 5,980+有效漏洞；313,879白帽 | 体量最大；头部赏金/漏洞量都高 |

> 这些是 **snapshot**，不是永久事实；每次同步更新并记录 `observed_at/confidence`。

Phase 4 当前实现：`bountygrid competition -file <json> -persist` 导入带 Evidence 的快照（`ManualImport`，原文脱敏到本地），写入 `competition_snapshots`；`schedule` 读取每平台最新 `PLATFORM` 快照。已落库先知 0.708/conf0.7、漏洞盒子 0.5/conf0.5、补天 0.5/conf0.5；后两者页面没有直接给出 top10 share，按 0.5 保守估计并在 Evidence 文本注明。

### 2.3 置信度

- 榜单完整可见：0.9
- 只看到 Top10/TopN：0.7
- 分页推算人数：0.5
- 无数据：0.2，并让 Opportunity 的 Confidence 降低

## 3. Opportunity Engine

```go
type Opportunity struct {
    ID                    domain.OpportunityID
    ProgramID             domain.ProgramID
    AssetID               *domain.AssetID

    EstimatedReward       domain.Range
    AcceptanceProbability *float64
    ResearchCost          domain.CostEstimate

    ExpectedReturn        *float64
    ExpectedReturnPerCost *float64
    CompetitionPenalty    float64

    Confidence            domain.Confidence
    Inputs                []string
    EvaluatedAt           time.Time
}
```

### 3.1 期望收益

```text
E[reward] =
    P(valid) ×
    (w_min × min + w_median × median + w_max × max) ×
    asset_tier_multiplier ×
    campaign_multiplier ×
    (1 - duplicate_penalty)
```

- `P(valid)`：历史有效率；新账号用保守先验（如 0.3），随 Ledger 反馈更新。
- `asset_tier_multiplier`：平台原生资产等级（先知超级/关键/核心；漏洞盒子核心/普通/边缘；补天产品A-E/厂商权重）。
- `campaign_multiplier`：双倍/活动系数。
- `duplicate_penalty`：平台重复规则 + 竞争快照。

### 3.2 期望成本

```text
E[cost] =
    human_hours × hourly_rate +
    compute_cost +
    tool_cost +
    review_hours × review_rate +
    risk_cost
```

- `human_hours`：研究 + 验证 + 报告 + 提交确认。
- `review_hours`：人工审批/复现成本。
- `risk_cost`：账号处罚风险、NDA/合规风险（可用权重近似）。

### 3.3 竞争惩罚

```text
competition_penalty =
    w1 × top10_share +
    w2 × duplicate_pressure +
    w3 × saturation
```

建议初始权重（可配置）：

```text
w1 = 0.4
w2 = 0.35
w3 = 0.25
```

### 3.4 排序分数

```text
score = (E[reward] × (1 - competition_penalty)) / max(E[cost], epsilon)
score = score × confidence
```

输出 `ExpectedReturnPerCost` 供 Scheduler 排序；不直接展示“最高赏金”。

### 3.5 硬过滤（先于打分）

- `Program.Status != RUNNING` → 排除。
- `AccountCapability.CanSubmit == false` → 只允许“人工准备”类 opportunity，不进入执行队列。
- `Eligibility.Decision != ELIGIBLE` → 进入 `HumanActionQueue`，不是执行队列。
- `Scope.Access != GRANTED` → 只允许 `READ_SCOPE` 人工解锁，不进入测试队列；Disclosure 可以是 GATED。
- `ActionPolicy` 对所需动作 DENY → 排除。

## 4. Scheduler

### 4.1 队列

```text
OpportunityQueue   候选机会（按 score）
ResearchQueue      已批准的研究任务
HumanActionQueue   报名/NDA/合规考核/认证/收款
ReviewQueue        待人工审核的 Finding/Report
SubmissionQueue    待人工批准的提交
```

### 4.2 调度约束

- 每平台最大并发任务数（默认1-2）。
- 每项目最大并发任务数（默认1）。
- 人工审批带宽：每人每天可审 N 个报告；超出排队。
- 冷却：失败/暂停/NDA 后冷却 24h。
- 公平性：避免只做高赏金项目；保留新人/校园/企业SRC配额。
- Kill switch：派发前重新读 `Program.Status`；TTL 过期则刷新；非 RUNNING 直接撤销。

### 4.3 调度循环

```text
1. 拉取到期 State/CompetitionSnapshot
2. 硬过滤
3. Opportunity Engine 打分
4. 选择 Top-K（受并发/配额限制）
5. 对每个机会创建 ActionRequest(VIEW_RULES/READ_SCOPE/...)
6. Policy Engine 返回 Decision
   - ALLOW → 创建 ResearchTask
   - REQUIRE_HUMAN → 进入 HumanActionQueue
   - DENY → 记录原因，降权
7. Harness 执行 ResearchTask
8. Finding → Verification → ReviewQueue
9. ReportDraft → SubmissionQueue → HumanApproval → Submit
10. Ledger 反馈 P(valid)/cost/competition
```

### 4.4 新人/学生策略

- 漏洞盒子：优先企业SRC（已加入）+ 闪测（有活跃期次时）；高级项目仅在有审核通过可能时申请。
- 补天：先走合规考核 → 公益SRC/人生第一洞 → 实战认证 → 专属SRC；GROW 学生赛道单独建模（报名至11-30）。
- 先知：先人工报名1-2个长期SRC，解锁 Scope 后再进入机会队列。

## 5. 反馈闭环

Ledger 提供：

```text
per platform/program/asset_tier:
  submissions
  accepted
  paid
  average_reward
  average_human_hours
  duplicate_rate
```

Opportunity Engine 用这些更新：

- `P(valid)` 先验；
- `E[reward]` 分布；
- `E[cost]`；
- `duplicate_pressure`。

没有历史数据时，使用保守先验并在 `Confidence` 中体现；不能把宣传最高赏金当期望值。

## 5.1 Phase 4 实现记录（2026-10-31）

- `internal/opportunity.Engine`：`E[reward]=P(valid)×(0.25min+0.5median+0.25max)×tier×campaign×(1−duplicate)`；`penalty=0.4 top10+0.35 duplicate+0.25 saturation`；`score=E[reward]×(1−penalty)/cost×confidence`。缺 Snapshot 时 penalty=0.5 三项、confidence=0.2。`Inputs[]` 可回放。
- `Scheduler`：RUNNING 硬过滤 + cooldown；CanSubmit/Eligibility/Scope 只进 HumanActionQueue；Top-K/平台/项目配额/企业SRC保留位；Persist 写 `opportunities` + `research_tasks`（`QUEUED`/`HUMAN_ACTION_REQUIRED`），不执行。
- 实测 Vulbox：9 RUNNING 可评分 → 0 research / 9 human（账号能力未观测）、40 paused/ended 排除；无平台动作。
- 未接入：CompetitionSnapshot 采集、Harness 执行、人工队列 UI、Ledger 反馈。

## 6. 反模式

1. 用“最高赏金”排序。
2. 把 Top10 集中度当“平台难度”唯一指标。
3. 没有 CompetitionSnapshot 就给高置信度。
4. 把“项目多”当“机会多”；大量项目是已结束/历史项目。
5. 忽略人工审核带宽，导致 Report 堆积。
6. 调度前不读 Kill Switch。
