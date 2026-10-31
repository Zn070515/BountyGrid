# Platform Adapters

## 1. 分层

```text
Adapter（平台语义）
  ├─ Reader：账号/项目/状态/Scope/Policy 同步
  ├─ Preparer：报告字段映射、附件检查、AI披露
  └─ Submitter：只在 HumanApproval 下调用；默认由人工在浏览器完成

Transport（传输）
  ├─ BrowserPage
  ├─ BrowserObservedAPI
  ├─ OfficialAPI（当前无）
  └─ ManualImport

Browser Sidecar（Playwright/CDP）
  └─ 独立进程；Adapter 通过受控 RPC 调用
```

Adapter 不直接操作数据库；返回 domain 对象 + ObservedFact + EvidenceRef，由用例层落库。

## 2. 接口

```go
type Adapter interface {
    Platform() domain.Platform

    // 登录必须由人完成；Adapter 只检测会话/恢复 state
    AuthStatus(ctx context.Context) (AuthState, error)

    SyncAccount(ctx context.Context) (AccountSnapshot, error)

    SyncPrograms(ctx context.Context, opts SyncOptions) (ProgramBatch, error)
    SyncProgram(ctx context.Context, id domain.ProgramID) (ProgramSnapshot, error)

    // 可能返回 GATED_BY_SIGNUP / GATED_BY_NDA / UNKNOWN
    SyncScope(ctx context.Context, id domain.ProgramID) (ScopeSnapshot, error)
    SyncPolicy(ctx context.Context, id domain.ProgramID) (PolicySnapshot, error)

    // 只生成报告草稿；不提交
    PrepareReport(ctx context.Context, d ReportInput) (PreparedReport, error)
}

type SubmitAdapter interface {
    // 必须携带人工批准；实现应再次校验 token 有效期与 scope
    SubmitReport(ctx context.Context, r PreparedReport, approval domain.HumanApproval) (Receipt, error)
}
```

```go
type AccountSnapshot struct {
    Account  domain.Account
    Facts    []domain.ObservedFact
    Evidence []domain.Evidence
}

type ProgramBatch struct {
    Programs []domain.Program
    Facts    []domain.ObservedFact
    Evidence []domain.Evidence
}

type ScopeSnapshot struct {
    Scope    domain.Scope
    Facts    []domain.ObservedFact
    Evidence []domain.Evidence
}
```

## 3. Transport

```go
type Transport interface {
    Kind() domain.TransportKind
    GetPage(ctx context.Context, url string, opts PageOptions) (Page, error)
    // 仅允许页面自身正常加载的请求；不构造未授权请求
    ObservedJSON(ctx context.Context, req ObservedRequest) (JSONResponse, error)
}

type Page struct {
    URL        string
    Title      string
    HTML       []byte
    Text       string
    Screenshot []byte
    CapturedAt time.Time
}
```

- `BROWSER_OBSERVED_API` 只允许“复现页面正常加载会发出的只读请求”，不得改参数越权、不得调后台管理接口。
- 每个请求带 `X-BountyGrid-Action-ID`? 不修改平台请求头；本地记录 action ID 即可。
- 请求速率受 `Constraints` 与全局 RateLimiter 双重限制。

## 4. 平台 Adapter 设计

### 4.1 先知 / ASRC

```text
Auth: 阿里云账号 OAuth/扫码 → account.aliyun.com → 回调 xianzhi.aliyun.com
Workbench: yundun.console.aliyun.com/?p=xznew
Transport: BROWSER_PAGE + BROWSER_OBSERVED_API
Observed API: /openapi/cst/2023-12-08/DescribeWebsiteTaskList.json 等
```

同步：

- `SyncAccount`：个人资料（实名/积分/赏金/称号）、漏洞管理（0/0/0）、任务列表（91/0）。
- `SyncPrograms`：任务列表分页；读取 `任务状态`、报名/任务时间、人数限制、赏金范围、类型。
- `SyncScope`：详情页“资产范围”页签；未报名返回 `NotSignUp` → `GATED_BY_SIGNUP`。
- `SyncPolicy`：任务详情、公告（AI/红线/提交规范/众测规范）。
- `PrepareReport`：按《先知漏洞提交规范》映射字段；未报名时表单不可达。

关键边界：

- 已结束任务仍显示“我要报名” → 以 `任务状态` + `任务时间` 派生 `ProgramState`，不能看按钮。
- 长期 SRC 的 `任务截止时间` 可能显示 `--`；用报名窗口 + 状态派生。
- 项目级覆盖：淘天反爬允许浏览器自动化/纯脚本，但有日限额/预算/报备；通义实验室不收录越狱/Clickjacking/PDF XSS。

### 4.2 漏洞盒子

```text
Auth: 微信扫码 / 手机验证码 / 账号密码 → Cookie
User Center: user.vulbox.com
Transport: BROWSER_PAGE + BROWSER_OBSERVED_API
Observed API: vapi.vulbox.com/web/*; vbapi.vulbox.com/v1/user/identity_info
```

同步：

- `SyncAccount`：控制台（见习白帽/经验9/信誉100/赛季0/排名9999+）、财务（0/0）、已加入项目（联想、嘉立创）。
- `SyncPrograms`：项目大厅分页（estimated 1513；分类 67/1386/51/5/2）、状态（进行中/暂停中/已结束）。
- `SyncScope`：企业SRC详情页；高级/私密项目 `GATED_BY_SIGNUP/NDA`。
- `SyncPolicy`：FAQ、项目公告、LSRC AI公告、提交表单字段。
- `PrepareReport`：标题/域名/类型/等级/简述/复现步骤/修复建议/附件；支持预存草稿。

关键边界：

- 项目状态会数小时内暂停/重开（联想）→ 状态 TTL 15-60 分钟。
- 企业SRC卡片即使未加入也显示“提交漏洞” → 以 `我的项目` 判断 joined。
- 高级项目申请看资料完整性（手机/微信/银行卡）、排名、历史质量、技能、报名顺序；NDA 必须人工。
- 专属/私密项目入口先弹《保密承诺书》→ Adapter 必须停止并返回 `GATED_BY_NDA`。
- 联想SRC禁止上传webshell；平台默认禁止大流量/大线程/大规模扫描。

#### Phase 1 实现记录（2026-10-31）

```text
internal/sidecar        agent-browser 只读客户端：
                        open / wait --load networkidle / eval --stdin / get /
                        state save / close；不提供 click/fill/submit
internal/adapter        Reader 接口与快照类型
internal/adapter/vulbox AuthStatus / SyncAccount / SyncPrograms
cmd/bountygrid sync     -programs / -account / -pages / -page-size / -session
```

已观测的公开列表接口（页面正常加载时自身发出的只读 GET，不绕过认证）：

```text
GET https://vapi.vulbox.com/web/project/projects
    ?type=-1&status=0&flag=0&search=&page=N&page_size=10..100
    &direction=desc&order_by=normal&limit=
```

字段映射（当前已验证）：

| 原生字段 | 映射 |
|---|---|
| `type=0/1/5/6/7/8` | 普通 / 高级 / 攻防众测 / 线下兼职 / 企业SRC / 公益SRC |
| `status=0/1/3` | `RUNNING` / `PAUSED` / `ENDED`；其他值 → `UNKNOWN` |
| `is_join` / `is_apply` | `PROGRAM_SIGNUP` / `APPLICATION_REVIEW` gate |
| 普通项目无 join/apply 标志 | `EligibilityUnknown`（不猜 ELIGIBLE） |
| `bonus_range` | `RewardPolicy.Min/Max`（CNY，现金）；其他奖励类型未知 |
| `task_stime/task_etime` | `program.start/end`（Unix → UTC time） |

`SyncPrograms` 一次只取 `MaxPages` 页；批次带 `EstimatedTotal` 与 `Estimated=true`（1513 来自分页推算）。每个响应页生成一条 Evidence，Fact ID 由 `evidence/native_id/predicate` 组成，重跑同一 Evidence 幂等。

企业SRC详情（`SyncProgram/SyncScope/SyncPolicy`）已用联想 444 的 `https://lsrc.vulbox.com` 实测：

- 页面 `资产范围` 段：以 `*.` 开头的独占行 → Include；含“暂不收录”的行 → 提取 `*.domain` 到 Exclude，无域名的描述行写入 `vulbox.scope.exclude_notes`。
- 页面 `禁止上传webshell` → `FileUpload=FORBIDDEN`；存在“AI生成漏洞报告提交规范”公告 → `AIAnalysis=CONDITIONAL` + `HumanVerificationRequired=true`。
- 其他自动化能力保持 `UNKNOWN`，等待平台 FAQ/公告合并；未知值不会被写成 `ALLOW`。
- 非企业SRC（普通/高级项目）详情页尚未验证，仍返回 `ErrUnsupported`。

本机会话：`probes/session/relogin.sh vulbox` 使用 `vbprobe` 并保存 state；产品 `sync` 默认复用同一个已登录会话（`-session vbprobe`），不加载旧 state 文件（损坏的 state 会让导航挂起）。其他机器用 `-session` 指定独立会话。

#### Phase 1 Xianzhi 实现记录（2026-10-31）

```text
Task list: https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/all
Cards:     .card-item ; pagination .cs-pagination-item ; 20 cards/page, 5 pages, 91 tasks
Data:      React fiber memoizedProps.task (id/status/type/registration*/task*/registrationLimit)
Status:    4=RUNNING, 5=ENDED, 6=PAUSED ; Type: 1=SRC, 2=企业众测
Scope:     unsigned task detail returns NotSignUp/报名后可见 -> GATED_BY_SIGNUP + NOT_GRANTED
```

- 页面文本只用于 bounty/buttons 等辅助字段；任务 ID/时间/状态以 React `task` 对象为准，避免文本改版误判。
- `SyncPrograms` 每页分两次 eval（0–10、10–20 张卡）避免 CLI 输出截断；翻页用 `eval` 点击 `.cs-pagination-item`，属于只读分页，不点击报名/提交。同一 hash URL 的 `open` 可能不重新取数，`openList` 会 `reload`。
- 2026-10-31 实测：全量 5 页 **91 条 = 34 RUNNING / 54 ENDED / 3 PAUSED，61 SRC / 30 企业众测**，1058 Fact、5 Evidence，与审计一致。
- 曾遇控制台数据接口 `os error 10060`：SPA 壳存在但 `.card-item`=0；reload 后恢复。0 卡片必须视为可重试失败，不能写成空任务列表。
- 账号同步可跑通；个人资料页只有“实名认证”标签、无文本状态，`identity_verified` 暂为 nil（fail-closed）。
- 已报名任务的资产范围解析尚未验证；当前对未报名任务 fail-closed 返回 GATED，不猜 Scope。

### 4.3 补天

```text
Auth: 奇安信统一账号（手机号+密码/短信）→ Cookie
User Center: butian.net/WhiteHat/Center（iframe）
Transport: BROWSER_PAGE + BROWSER_OBSERVED_API
Observed API: /WhiteHat/Center/baseData; /Company/Info/assetRange 等
```

同步：

- `SyncAccount`：实习白帽 LV1、500荣誉币、0积分/0库币/0奖金、0提交、未完成实战认证、合规考核状态。
- `SyncPrograms`：37 专属SRC、公益SRC 200页、1 企业SRC、活动中心、GROW。
- `SyncScope`：专属SRC资产范围报名前 `GATED_BY_SIGNUP`；公益SRC无项目级Scope。
- `SyncPolicy`：行为规范、重申专属SRC规范、AI规范/通告、企业SRC奖励、通用2.0、GROW规则。
- `PrepareReport`：厂商/资产范围/类别/标题/URL/类型/权重/等级/活动/任务/简述/细节/附件/修复方案/行业/地区/联系方式/匿名。

关键边界：

- `AccountCapability.CanSubmit=false`（合规考核未完成）→ 任何提交 DENY；不是项目级。
- 专属SRC还需 `PRACTICAL_CERTIFICATION`（≥1有效漏洞）+ 报名。
- 新用户待审核限5个；第1个有效漏洞后解除。
- 提交页只收 Web/IoT；工控/OS/通用软件“暂停接收”；与通用2.0存在口径冲突 → 记录冲突，人工确认。
- GROW：整体报名 2026-08-25~11-30、活动 2026-09-01~11-30；漏洞挖掘实战第二期 10-01~10-31 是月度阶段。
- 合规考核/实战认证/报名/NDA/收款全部人工。

## 5. 轮询与限速

| 数据 | 频率 | 备注 |
|------|------|------|
| 项目状态 | 15-60 min | 漏洞盒子会快速变更 |
| 项目列表 | 1-6 h | 高级项目通常20:00发布 |
| 规则/公告 | 6-24 h | 规则变化 |
| 账号 | 1 d | 等级/积分/合规/认证 |
| 财务 | 1 d | 余额/打款 |
| 竞争榜 | 1 d | 排名/奖金 |

- 平台级 RateLimiter：每平台独立，默认保守（如 1 req/2s，页面导航 1 req/5s）；具体以项目 Constraints 为准。
- 不做并发抓取同一平台；串行 + 退避。
- 会话过期：返回 `AUTH_REQUIRED`，暂停该平台所有调度，通知人工重新登录。

## 6. 失败模式

| 失败 | 处理 |
|------|------|
| 登录过期/验证码 | `AUTH_REQUIRED`；人工重新登录；不重试爆破 |
| 页面改版/选择器失效 | 抽取器报错；保留 Evidence；不写猜测 Fact |
| Scope 返回 NotSignUp | 返回 `GATED_BY_SIGNUP + Access=NOT_GRANTED`，不返回空 Scope |
| NDA 弹窗 | 停止；返回 `GATED_BY_NDA`；不点击同意 |
| 合规考核弹窗 | 返回 `ACCOUNT_GATE`；不参加 |
| 项目暂停/结束 | 更新 `ProgramState`；撤销未执行任务 |
| 提交按钮 disabled | 记录原因；不绕过 |
| 接口 403/401 | 停止；不重放/不绕过 |
| 页面出现敏感值 | 脱敏后存 Evidence；不写审计 JSON |

## 7. 报告准备

`PrepareReport` 只做字段映射与完整性检查，不提交：

```go
type ReportInput struct {
    FindingID   domain.FindingID
    ProgramID   domain.ProgramID
    Title       string
    AssetURL    string
    VulnType    string
    Severity    string
    Summary     string
    Steps       string
    PoC         string
    Impact      string
    Fix         string
    Attachments []string
    AIDisclosure map[string]string
}

type PreparedReport struct {
    Platform   domain.Platform
    ProgramID  domain.ProgramID
    Fields     map[string]string
    Missing    []string
    Warnings   []string
    AIDisclosure map[string]string
}
```

- 必填字段缺失 → `Missing`，Policy Engine 返回 `REQUIRE_MORE_EVIDENCE`。
- AI 辅助报告：必须填人工验证说明、复现步骤、完整 PoC、关键截图；补天 GROW AI 赛道还需大模型名称/版本、AI工具名称。
- 附件仅辅助；正文必须完整写在详情字段（补天/先知/漏洞盒子均如此）。
