# Evidence → ObservedFact → DerivedState → PolicyDecision

## 1. 为什么必须分层

GROW 日期错误是典型案例：

- 活动中心页面显示“漏洞挖掘实战-第二期 2026-10-01~10-31”；
- 但 GROW 整体/AI 赛道规则是“报名 2026-08-25~11-30、活动 2026-09-01~11-30”；
- 如果直接把 `end_date = 2026-10-31` 存进数据库，整个调度会提前一个月停止。

正确做法：

```text
Evidence:      补天 GROW 规则弹窗截图 + 活动中心页面
ObservedFact:  grow.registration_end = 2026-11-30
               grow.activity_end = 2026-11-30
               phase2.end = 2026-10-31
DerivedState:  grow.registration_open = true (at 2026-10-31)
PolicyDecision: ALLOW_VIEW; REQUIRE_HUMAN_TO_REGISTER
```

### 日期语义

网页日期通常表示“截止到当天结束”，且国内平台应按 `Asia/Shanghai` 解释，而不是 UTC midnight：

```text
2026.11.30 报名截止
  -> start: 2026-11-30 00:00:00 +08
  -> end:   2026-11-30 23:59:59.999999999 +08
  -> ObservedFact.Unit = "date"
```

- 日期精度存入 `Unit`（`date` / `datetime`），不把 date-only 硬转成 UTC midnight。
- 结束边界按 `INCLUSIVE_END` 处理；`now.After(end)` 只在本地日期真正过去后才为 true。
- 若未来出现明确时刻（如 `2026-11-30 18:00`），使用 `datetime` 并保留时区。

## 2. Evidence

### 2.1 类型

| SourceType | 例子 | 保存内容 |
|------------|------|----------|
| ACCOUNT | 个人中心、控制台 | 页面文本、截图、URL、captured_at |
| PROGRAM | 项目详情、资产范围 | 页面文本、截图、URL |
| POLICY | 帮助中心、规则页 | 全文/结构化段落 |
| PAYOUT | 财务中心、提现页 | 状态字段，不保存卡号 |
| ANNOUNCEMENT | 公告详情 | 标题、日期、全文 |
| SCREENSHOT | 关键页面截图 | 文件引用 + hash |

### 2.2 Transport

```text
OFFICIAL_API            官方文档化 API（当前三平台都没有）
BROWSER_PAGE            浏览器可见页面
BROWSER_OBSERVED_API    页面正常加载时观察到的前端接口
MANUAL_IMPORT           人工录入/导入
```

三平台内部接口一律 `BROWSER_OBSERVED_API`：

- 先知 `openapi/cst/2023-12-08/*`
- 漏洞盒子 `vapi.vulbox.com/web/*`
- 补天 `/WhiteHat/Center/*`、`/Company/Info/*`

### 2.3 存储

```sql
CREATE TABLE evidence (
    id            TEXT PRIMARY KEY,
    platform      TEXT NOT NULL,
    source_type   TEXT NOT NULL,
    transport     TEXT NOT NULL,
    url           TEXT NOT NULL,
    page_title    TEXT,
    captured_at   TIMESTAMPTZ NOT NULL,
    content_hash  TEXT NOT NULL,
    raw_ref       TEXT NOT NULL,
    redactions    JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- `raw_ref` 指向对象存储/本地加密目录；数据库不存大文本。
- 原文保存前做脱敏：手机号、邮箱、身份证、银行卡、Cookie、Token、IP。
- `content_hash` 用于去重与变更检测；同一 URL + hash 不重复抽取。

### 2.4 证据新鲜度

每个来源类型有 TTL：

| 数据 | TTL | 说明 |
|------|-----|------|
| 项目状态 | 15-60 分钟 | 漏洞盒子会数小时内暂停/重开 |
| 项目列表 | 1-6 小时 | 新项目/活动 |
| 规则/公告 | 6-24 小时 | 规则会更新 |
| 账号状态 | 1 天 | 等级/积分/合规考核 |
| 财务 | 1 天 | 余额/打款 |
| 竞争榜 | 1 天 | 排名 |

超过 TTL 的 State 不得用于执行类决策；Policy Engine 返回 `REQUIRE_MORE_EVIDENCE`。

## 3. ObservedFact

### 3.1 结构

```go
type ObservedFact struct {
    ID               FactID
    Subject          FactSubject
    Platform         Platform
    AccountID        *AccountID
    ProgramID        *ProgramID
    AssetID          *AssetID
    Predicate        string
    Value            any
    Unit             string
    ObservedAt       time.Time
    EvidenceRefs     []EvidenceID
    Confidence       Confidence
    ExtractorVersion string
}
```

### 3.2 命名规范

使用平台原生字段名，不做跨平台重命名：

```text
xianzhi.task.state = "任务暂停"
xianzhi.task.signup_start = 2026-10-16T15:55:53+08:00
vulbox.project.status = "暂停中"
vulbox.account.exp = 9
butian.grow.registration_end = 2026-11-30
butian.account.compliance_exam_completed = false
```

Normalization 在上层做：例如 `"任务暂停"` / `"暂停中"` / `"已暂停"` → `PAUSED`。

### 3.3 抽取器版本

每个平台一套抽取器，带版本号：

```text
extractor: xianzhi/task-list@1.0.0
extractor: vulbox/project-detail@1.0.0
extractor: butian/grow-rules@1.0.0
```

当页面改版或发现抽取错误，提升版本并重跑 Evidence；旧 Fact 保留但标记 `superseded_by`。

### 3.4 置信度

| 场景 | Confidence |
|------|------------|
| 页面结构化字段 + 截图一致 | 0.95 |
| 页面文本明确写日期 | 0.9 |
| 页面文本需推断（如“报名时间已过”但无明确日期） | 0.6 |
| 分页推算的数量 | 0.5 |
| 用户口述/未验证 | 0.3 |

任何 `< 0.8` 的 Fact 不能单独驱动执行决策。

## 4. DerivedState

### 4.1 派生规则

```text
registration_open =
    registration_start <= now <= registration_end
    AND not explicitly closed

program_status =
    if explicit pause/stop -> PAUSED
    else if now > end -> ENDED
    else if now < start -> UPCOMING
    else -> RUNNING

scope_visibility =
    if page says NotSignUp/报名后可见 -> GATED_BY_SIGNUP
    if NDA page -> GATED_BY_NDA
    if public list -> PUBLIC
    else UNKNOWN

account_can_submit =
    identity_verified
    AND compliance_exam_completed != false
    AND not banned
    AND (program-specific certification satisfied for that program)
```

### 4.2 State 失效

```sql
CREATE TABLE derived_states (
    id            TEXT PRIMARY KEY,
    subject       TEXT NOT NULL,
    platform      TEXT NOT NULL,
    account_id    TEXT,
    program_id    TEXT,
    state         JSONB NOT NULL,
    inputs        JSONB NOT NULL, -- fact IDs
    observed_at   TIMESTAMPTZ NOT NULL,
    derived_at    TIMESTAMPTZ NOT NULL,
    valid_until   TIMESTAMPTZ NOT NULL,
    confidence    DOUBLE PRECISION NOT NULL,
    rule_version  TEXT NOT NULL
);
```

- `valid_until` 到期 → 重新派生。
- 任一输入 Fact 被 supersede → 所有下游 State 失效。
- State 不能手写；只能由规则计算。

## 5. PolicyDecision

### 5.1 输入

```text
ActionRequest
+ Account（含 Capability/Gates）
+ Program（含 Status/Eligibility/Scope/Constraints/Automation）
+ Asset（如果动作针对资产）
+ CompetitionSnapshot（如果动作涉及机会选择）
+ now
+ PolicyVersion
```

### 5.2 输出

```go
type ActionDecision struct {
    Decision       Decision // ALLOW/DENY/REQUIRE_HUMAN/REQUIRE_MORE_EVIDENCE
    Reasons        []string
    GatesEvaluated []EligibilityGate
    PolicyVersion  string
    ExpiresAt      *time.Time
    Constraints    *Constraints
    HumanApproval  *HumanApproval
}
```

### 5.3 审计

每个 Decision 写一条审计事件：

```json
{
  "decision_id": "...",
  "request_id": "...",
  "platform": "butian",
  "program_id": "...",
  "action": "REPORT_SUBMIT",
  "decision": "REQUIRE_HUMAN",
  "reasons": ["COMPLIANCE_EXAM unsatisfied", "human-only action"],
  "policy_version": "policy-2026-10-31.1",
  "evidence_refs": ["ev-..."],
  "decided_at": "2026-10-31T..."
}
```

审计事件不可修改；只能追加。

## 6. 反模式（禁止）

1. 直接把 Agent 输出的 `status/end_date` 存为事实。
2. 把 `NotSignUp` 解析成 `scope = []`。
3. 把分页推算的数量当精确值。
4. 把“按钮 enabled”当“有资格”。
5. 把项目级规则当平台级规则。
6. 没有 EvidenceRef 的 Fact。
7. 没有 observed_at 的 State。
8. 没有 policy_version 的 Decision。
