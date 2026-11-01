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
type Input struct {
    Finding  domain.Finding
    Program  domain.Program
    Decision *domain.ActionDecision
    Reproduced bool
    ReproductionEvidence []domain.EvidenceID
    ImpactVerified bool
    ImpactEvidence []domain.EvidenceID
    DataMinimized *bool // nil = unknown, false = violation
    DuplicateChecked bool
    DuplicateRisk float64
    PubliclyKnown bool
    AIAssisted bool
    HumanVerified bool
    HumanVerificationRef string
    VerifiedBy string
    Now time.Time
}

type Verifier interface {
    Verify(ctx context.Context, in Input) (domain.Verification, error)
}
```

验证器可以是规则 + 人工，不得由模型单独判定 `VERIFIED`。

**当前实现（2026-11-01）**：`internal/verify.RuleVerifier` 实现规则验证，接受 `Finding + Program + ActionDecision + 复现/影响证据 + 数据最小化 + 重复风险 + AI 披露`。判定顺序：Finding 完整性 → Program 一致性 → Scope（`ResolveAsset` + `CheckAsset`，conditional 为 `NEEDS_MORE_EVIDENCE`）→ 授权决策（必须 ALLOW、Context 完整、动作属于 scope 执行类）→ 数据最小化/公开已知 → 重复检查 → AI 人工验证 → 复现/影响证据。`VERIFIED` 必须同时满足 `Reproduced/InScope/ImpactVerified/DataMinimized` 且有 Evidence；模型无法单独产生。`bountygrid verify -file <json> [-persist]` 从 PostgreSQL 加载 Program/Decision，写 `verifications`。

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

**当前实现（2026-11-01）**：`internal/report.Build` 只接受 `VERIFIED` 的 Verification，组装 `ReportDraft`（`DRAFT`）并返回 `Missing/Ready`。通用必填：`title/asset_url/vuln_type/severity/summary/steps/poc/impact/remediation`；Xianzhi 额外要求 `account_name`（`task_id` 默认 Program.NativeID）；Butian 额外要求 `vendor/category/asset_scope/weight/activity/task_id/industry/region/contact`（`vendor` 默认 Program.Name）。`contact` 是联系方式引用（如“账号资料”），不是原始手机号/邮箱；DB 不存完整联系方式，真实值在平台表单由人工填写。AI 披露要求 `human_verification`，Butian GROW 还要求 `model_name/model_version/tool_name`。附件按平台校验扩展名（Vulbox 文档/压缩包；Butian 只收 zip/rar）。report 文本中的 cookie/token/密码/证件/银行卡/完整手机号/邮箱会被拒绝。`bountygrid report -file [-persist]` 从 PostgreSQL 加载 Program/Finding/Verification，`-persist` 写 `report_drafts`。本步骤不提交、不勾选协议、不代签 NDA。

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

**当前实现（2026-11-01）**：`internal/submit.Service` 加载 `ReportDraft + Program + Finding + Verification`，用 `report.Recheck` 复核完整性，构造 `REPORT_SUBMIT` `ActionRequest`（`request_id=submit/<report>/<approval>`、参数 `{"report_id":...,"report_content_hash":...}`、`ResourceID=report_id`），交给 Action Gateway。content hash 覆盖字段、AI 披露、附件和 VerificationID，审批后修改草稿会使 hash 不匹配。Gateway 返回 `ALLOW` 且 `AuthorizeWith` 消费 single-use approval 之后，才调用 `Submitter`。本仓库只有 `DryRunSubmitter`（本地 receipt，无网络/平台动作）；`PUT /submissions` 持久化 `Submission`，`0006_submission_idempotency.sql` 对 `approval_id` 加唯一索引。`bountygrid submit -print-binding` 打印审批必须绑定的 request_id/parameters_hash；`submit -report -account -approval` 执行 dry-run。真实平台 SubmitAdapter 尚未实现，且本仓库禁止真实提交演练。

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

**当前实现（2026-11-01）**：`internal/ledger` 实现 append-only 现金账本（v1 仅 CNY）。`PAID` 是实际到账净额，`FEE/TAX` 只做信息展示、不重复扣减；`EXPECTED/SUBMITTED` 不计入净现金。`REVERSAL` 是现金冲正（v1 只允许冲 `PAID/REFUND`），`ExternalRef` 对 `PAID/REFUND/REVERSAL` 必填并在 `(platform,kind,external_ref)` 唯一。`Append` 校验 Evidence 存在、Submission 非 `DRY_RUN`、冲正不超过目标金额；同 ID 同内容幂等、不同内容报错。`Summarize` 输出 Expected/Submitted/GrossAccepted/Fees/Taxes/Refunds/PaidNet/NetCash/AvgAccepted/AvgPaid；`Reconcile` 只报平台/银行差异，不自动改账；`Feedback` 输出 Opportunity 可用的提交/验收/到账指标（不伪造 duplicate_rate）。`bountygrid ledger -file [-persist] / -summary / -reconcile` 已接入。非现金奖励（KB/RYB/积分/荣誉/实物/优惠券）留 `NonCashRecorder` 接口，v1 不混入金额账本。

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
