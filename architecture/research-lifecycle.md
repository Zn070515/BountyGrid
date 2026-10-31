# 研究生命周期：Finding → Verification → Submission → Ledger

## 1. 总体状态机

```text
ResearchTask
  CREATED → RUNNING → (FINDING_CANDIDATE | NO_FINDING | ABORTED)
                          ↓
Finding
  CANDIDATE → VERIFYING → VERIFIED / NEEDS_MORE_EVIDENCE / OUT_OF_SCOPE / DUPLICATE_RISK / REJECTED
                          ↓
ReportDraft
  DRAFT → READY_FOR_REVIEW → APPROVED → SUBMITTED → ACCEPTED / REJECTED / DUPLICATE
                          ↓
RevenueLedger
  EXPECTED → SUBMITTED → ACCEPTED → PAID / FEE / TAX / REFUND
```

## 2. Finding

```go
type Finding struct {
    ID         FindingID
    TaskID     TaskID
    ProgramID  ProgramID
    AssetID    *AssetID
    Title      string
    VulnType   string
    Severity   string
    Summary    string
    Evidence   []EvidenceID
    Status     FindingStatus
    CreatedAt  time.Time
}
```

Finding 必须来自：

- 已授权 Scope 内的资产；
- 已执行的 `ActionDecision=ALLOW`；
- 可复现的 Evidence；
- 未越权获取用户数据。

禁止：

- 把模型猜测直接当 Finding；
- 把“可能存在”写成“已验证”；
- 把多个同源问题拆成多个 Finding；
- 把公开已知漏洞当新发现。

## 3. Verification

### 3.1 验证维度

| 维度 | 问题 | 证据 |
|------|------|------|
| Scope | 资产是否在项目范围？ | Scope 快照 + 资产匹配 |
| Reproduced | 能否按步骤复现？ | 请求/响应/截图/视频 |
| Impact | 危害是否真实？ | 数据/权限/业务影响证明 |
| Data Minimized | 是否只取必要数据？ | 证据脱敏记录 |
| Duplicate Risk | 是否重复/同源？ | 平台历史、公开披露、规则 |
| AI Verification | AI 辅助是否人工验证？ | 人工验证说明 + 截图 |
| Fix/Advice | 是否有修复建议？ | 报告字段 |

### 3.2 验证判定

```text
VERIFIED             可提交
NEEDS_MORE_EVIDENCE  补充证据
OUT_OF_SCOPE         拒绝，记录
DUPLICATE_RISK       人工判断是否提交
REJECTED             拒绝
```

### 3.3 验证器

```go
type Verifier interface {
    Verify(ctx context.Context, f domain.Finding) (domain.Verification, error)
}
```

验证器可以是规则 + 人工，不得由模型单独判定 `VERIFIED`。

## 4. ReportDraft

### 4.1 通用字段

- 标题（业务+漏洞类型，避免“某系统”）
- 资产/URL
- 漏洞类型
- 漏洞等级
- 简述
- 详细复现步骤（图文/请求包）
- 完整 PoC/EXP（避免 All-in-One）
- 影响/危害证明
- 修复建议
- 附件（仅辅助）
- AI 使用说明（模型/工具/人工验证）
- 联系方式（按账号资料，不写敏感值）

### 4.2 平台映射

| 平台 | 必填特点 | 附件 | 草稿 |
|------|----------|------|------|
| 先知 | 所属任务、漏洞URL、POST包、账号名、完整PoC/截图 | 截图/视频/文件 | 未报名不可见；未见草稿 |
| 漏洞盒子 | 标题/域名/类型/等级/简述/复现/修复建议/协议 | zip/rar/doc/pdf/xls，≤200MB | 支持预存/草稿箱 |
| 补天 | 厂商/资产范围/类别/权重/等级/活动/任务/细节/行业/地区/联系方式/协议 | zip/rar ≤50MB | 无草稿按钮 |

### 4.3 AI 披露

```json
{
  "ai_assisted": true,
  "model_name": "...",
  "model_version": "...",
  "tool_name": "...",
  "human_verification": "逐条复现并截图，见 evidence-123",
  "ai_generated_sections": ["summary", "steps"],
  "human_edited_sections": ["impact", "poc"]
}
```

- 三平台都要求人工验证；补天 GROW AI 赛道还要求模型/工具字段。
- AI 未验证报告 >3/>5 有处罚；Policy Engine 在提交前检查披露完整性。

## 5. Submission

### 5.1 人工审批

`REPORT_SUBMIT` 永远 `REQUIRE_HUMAN`。审批界面必须展示：

- 完整报告
- Scope 证据
- 复现证据
- 影响证据
- AI 披露
- 重复风险
- 目标平台/项目/资产
- 提交后不可编辑/撤回的说明

审批 token：

```go
type HumanApproval struct {
    ID         string
    ApprovedBy string
    ApprovedAt time.Time
    Action     domain.Action
    ProgramID  domain.ProgramID
    Scope      string
    ExpiresAt  time.Time
    Comment    string
}
```

### 5.2 提交执行

- `SubmitAdapter.SubmitReport` 只接受有效 approval。
- 提交前再次检查 `Program.Status`、Scope、账号 `CanSubmit`。
- 提交后保存 receipt/URL/时间/状态。
- 提交失败不自动重试超过 1 次；防止重复提交。

### 5.3 提交后

- 状态跟踪：待审核/待补充/审核通过/奖金确认/待打款/打款完成（按平台）。
- 补天/先知/漏洞盒子状态机不同，存入 `Submission.Status` + `StatusValues`。
- 需要补充证据时，回到 `ReportDraft`，仍由人工提交补充。
- 评论/沟通由人工进行；Agent 可起草，不自动发送。

## 6. Revenue Ledger

### 6.1 账本条目

```go
type LedgerEntry struct {
    ID           string
    Platform     Platform
    ProgramID    ProgramID
    FindingID    *FindingID
    SubmissionID *SubmissionID
    Kind         LedgerKind
    Amount       Money
    Status       string
    OccurredAt   time.Time
    Evidence     []EvidenceID
}

type LedgerKind string
const (
    LedgerExpected  LedgerKind = "EXPECTED"
    LedgerSubmitted LedgerKind = "SUBMITTED"
    LedgerAccepted  LedgerKind = "ACCEPTED"
    LedgerPaid      LedgerKind = "PAID"
    LedgerFee       LedgerKind = "FEE"
    LedgerTax       LedgerKind = "TAX"
    LedgerRefund    LedgerKind = "REFUND"
)
```

### 6.2 规则

- `EXPECTED` 来自 Opportunity/奖励规则，不是承诺。
- `ACCEPTED` 以平台状态为准。
- `PAID` 以银行/平台到账为准；不保存银行卡号。
- 费用/税单独条目，不污染奖金。
- 每个条目带 Evidence（平台状态截图/公告）。
- 账本只追加，不修改；更正用反向条目。

### 6.3 对账

```text
平台财务页余额
  ↔ PAID 条目汇总
  ↔ 银行到账（人工确认）
```

差异进入 `ReconciliationIssue`，人工处理。

## 7. 反馈到 Opportunity Engine

Ledger 汇总：

```text
per platform/program/asset_tier:
  submissions
  accepted
  paid
  avg_reward
  avg_human_hours
  duplicate_rate
  rejection_reasons
```

用于更新：

- `P(valid)` 先验
- `E[reward]`
- `E[cost]`
- `duplicate_pressure`
- `saturation`

## 8. 禁止事项

1. 自动提交/自动补充/自动评论。
2. 未验证的 AI 报告。
3. 提交超出 Scope 的资产。
4. 提交公开已知/重复漏洞。
5. 保存用户数据/源码/银行卡。
6. 用“提交数量”当 KPI；以“有效/已支付”为准。
