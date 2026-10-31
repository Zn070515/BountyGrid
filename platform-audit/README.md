# 漏洞赏金平台配置审计（先知 / 漏洞盒子 / 补天）

- 审计时间：**2026-10-31**
- 审计对象：当前用户已注册的三个国内漏洞平台账号
- 审计性质：**平台配置审计**。未对任何厂商域名/IP/API/App 发起测试；未报名、未签NDA、未提交漏洞、未提现、未修改收款/实名。
- 结论时效：平台规则与项目状态会变化，接入 Go 前必须重新核对 `observed_at`。

## 目录

```text
platform-audit/
├── README.md
├── xianzhi/   account.json platform.json programs.json policy.json payout.json notes.md
├── vulbox/    account.json platform.json programs.json policy.json payout.json notes.md
├── butian/    account.json platform.json programs.json policy.json payout.json notes.md
└── comparison/
    ├── unified-program-model.json
    ├── capability-matrix.md
    ├── adapter-design.md
    └── findings.md
```

---

## 一、当前账号状态

### 先知 / ASRC（新先知平台）

| 项目 | 状态 |
|------|------|
| 注册 | 已注册，阿里云主账号 |
| 实名 | **已完成**（个人扫脸；支付宝扫码未认证） |
| 等级/称号 | **无**（未获得白帽子等级/王牌A称号） |
| 积分/赏金 | 积分 **0**；赏金 **¥0** |
| 新人状态 | 无显式“新秀”标识；当前无新秀榜；2024-11 曾有新人专享活动（已结束） |
| 收款 | **未配置**；赏金累计/待打款均显示“--”；有“修改支付信息”入口 |
| 任务 | 可见 **91** 个；参与 **0** 个；“我的任务：暂无数据” |
| 主要限制 | 未报名任务时提交按钮 disabled；资产范围/变更记录/排行榜返回 `NotSignUp`；未报名无法查看表单 |

### 漏洞盒子

| 项目 | 状态 |
|------|------|
| 注册 | 已注册（微信扫码登录） |
| 实名 | **已完成**；手机/邮箱/微信已绑定 |
| 等级 | **见习白帽**，经验值 9；下一等级初级白帽需经验值81（截图未显示总上限） |
| 信誉/排名 | 信誉 **优秀 100**；S3赛季积分 0，排名 **9999+**，暂未上榜 |
| 新人状态 | 见习白帽；符合新秀榜参与条件（赛季前无有效漏洞），但新秀榜奖励需 ≥500 积分 |
| 已加入项目 | **2 个企业SRC：联想集团、嘉立创**（均可提交） |
| 收款 | **未配置**；未绑定银行卡；普通账户/专属账户均 0 |
| 主要限制 | 高级项目需申请+审核+NDA；专属/私密项目先签《保密承诺书》；银行卡未绑定无法提现 |

### 补天

| 项目 | 状态 |
|------|------|
| 注册 | 已注册；**注册时间 2026-10-31 18:55:04（当天新号）** |
| 实名 | **已认证**；手机已绑定；微信未绑定 |
| 等级 | **实习白帽 LV1**；补天排名 “-”（未上榜） |
| 资产 | 0 奖金、0 库币、**500 荣誉币**、0 积分 |
| 新人状态 | 实习白帽；0 有效漏洞；新人成长计划历史活动均已结束；校园 GROW 计划可参与 |
| 收款 | **未配置**；银行卡信息为空 |
| 主要限制 | **本季度合规考核未完成 → 所有提交被硬阻断**；**实战认证未完成（需至少1个有效漏洞）→ 专属SRC不可提交**；新用户待审核限 **5** 个 |

---

## 二、当前能直接参与什么

> 这里只回答“基于当前登录账号的真实状态”，不是平台介绍。

### 漏洞盒子：唯一已有可直接提交项目的平台

- **现在就能提交**：联想集团安全应急响应中心、嘉立创安全应急响应中心（均已加入，进行中）。
- **可一键加入**：第38期 斗象产品漏洞收集（奖励为入场券，非现金）。
- **可申请但低概率**：高级项目（需运营审核；看资料完整性/排名/历史质量/技能/报名顺序；0历史+银行卡未绑定，通过概率低）。
- **不可直接参与**：专属/私密项目（先签《保密承诺书》）；当前闪测期次均已结束；攻防/线下兼职当前均结束。

### 先知：需要先人工报名任务

- **当前 0 个已报名任务，因此严格来说不能提交任何漏洞**。
- 人工报名长期SRC后，可解锁对应任务：阿里云产品（最高10万）、阿里云、通义实验室（最高3万）、淘天集团、1688、菜鸟、钉钉、高德、优酷&土豆、千问C端等（31个常驻品牌SRC）。
- 2026年10月众测：报名窗口已过（10-16~10-21）但任务进行中、按钮仍可用，资格 **UNKNOWN**。
- 已暂停：灵犀互娱、端点科技、银泰（不可参与）。

### 补天：先过合规考核

- **当前不能提交任何漏洞**（合规考核未完成）。
- 完成合规考核后：可提交 **公益SRC / 未注册厂商 / 通用漏洞**（但提交页提示通用软件近期暂停接收，需人工确认）。
- 拿到 **1 个有效漏洞** 后自动完成 **实战认证** → 解锁 **37 个专属SRC** 的提交权限。
- **校园 GROW 计划 2026**：报名 **2026-08-25~2026-11-30**，活动 **2026-09-01~2026-11-30**，当前进行中；漏洞挖掘实战另有月度阶段（第二期为10-01~10-31）。学生认证+校园战队，最高2万元，含 **AI辅助漏洞挖掘赛道**；这是当前最贴合用户身份的机会，但需人工报名/认证。
- 补天众测：INVITE_ONLY（排名前300/近一年专属SRC高危5+/其他SRC前3/精英内推，满足两条+签NDA+邀请码），当前不满足。

---

## 三、目前绝对不能自动化什么

### 绝对禁止（法律/平台红线）

- 对任何厂商域名、IP、API、App、服务器进行漏洞测试。
- 发送 SQL注入、XSS、命令执行、SSRF、目录穿越等攻击 Payload。
- 端口扫描、目录扫描、子域名爆破、暴力破解、大流量/高并发扫描。
- Burp Intruder、Nuclei、ffuf、nmap 等工具探测真实资产。
- 内网渗透、横向移动、提权、驻留后门、DoS/DDoS。
- 批量读取/拖库/留存用户数据；测试后不删除数据。
- 绕过认证、修改请求、调用未授权/后台管理接口。

### 必须人工完成（Agent 不得代做）

| 操作 | 原因 |
|------|------|
| 平台登录（勾选《用户协议》《隐私政策》） | 法律条款不可代接受 |
| 任务/项目报名 | 可能触发协议、名额、审核 |
| 签署 NDA / 《保密承诺书》 | 法律文件不可代签 |
| 补天季度合规考核 | 账号级合规门槛 |
| 补天实战认证 | 需真实有效漏洞 |
| 绑定银行卡/支付宝/收款信息 | 敏感账户变更 |
| 提交漏洞报告 | 误报/法律责任由账号持有人承担 |
| 提现 | 资金操作 |
| 修改实名/手机号/密码/MFA | 账号安全 |

### 可以自动化（只读/辅助）

- 读取项目列表、状态、奖励范围、公告、FAQ、排行榜。
- 读取当前账号的等级/积分/待审核/收款状态（不读取敏感值）。
- 生成报告草稿、整理证据、检查字段完整性。
- 项目状态轮询与 kill switch 告警。
- **报告提交前必须人工逐条确认**（本系统设计上限 LEVEL 4）。

---

## 四、哪个平台最适合率先接入 Go

评分维度（1-5）：

| 维度 | 先知 | 漏洞盒子 | 补天 |
|------|------|----------|------|
| 数据可获取性 | 3 | **5** | 2 |
| 项目数量 | 3 | **5** | **5** |
| 规则清晰度 | 4 | 4 | **5** |
| 新人机会 | 2 | **4** | **4** |
| Agent 可用程度 | 3 | **4** | 2 |
| 收款便利程度 | 2 | **4** | **4** |
| **合计** | **17/30** | **26/30** | **22/30** |

### 结论：漏洞盒子第一，先知第二，补天第三

**漏洞盒子最适合率先接入 Go**，原因：

1. 已有 **2 个可直接提交的企业SRC**（联想、嘉立创），是唯一“现在就能进入报告流程”的平台。
2. **约1513个项目（估计值）**、51 个企业SRC、项目状态/奖励范围/规则公开度高。
3. FAQ 把 **提现（最低300、次月10-15）、审核流程、NDA、重复漏洞、信誉值** 讲得最清楚。
4. 前端 API 结构最规整（`vapi.vulbox.com/web/*`），适合做只读同步。
5. 新人路径明确：见习白帽 → 企业SRC/闪测 → 高级项目；新秀榜存在。

**先知第二**：高赏金、长期SRC多（91个任务、31个常驻品牌），但资产范围必须报名后才可见，且当前0任务；适合在人工报名1-2个长期SRC后接入。

**补天第三**：规则最细、体量最大（6,655企业、313,879白帽），但当前账号被 **合规考核 + 实战认证** 双重锁死，连公益SRC都不能提交；适合先人工完成合规考核/学生认证，再接入。**但补天校园GROW的AI辅助挖洞赛道是对用户学生身份+AI Agent方向最有战略价值的入口，建议优先人工处理。**

---

## 五、Go 统一模型缺哪些字段

原模板只有平台/账号/项目/奖励/收款等基础字段。**最重要的纠正：Eligibility 不能是单枚举**，必须是 `Decision + Gates[]`；补天合规考核属于账号级 `AccountCapability`，不是 Program 属性。完整模型见 `comparison/unified-program-model.json`（schema 2.0）与 `../architecture/policy-engine.md`。

### 账号级（Account）

0. `EligibilityState{Decision, Gates[]}`（一个项目可同时需要报名+审核+NDA+银行卡+历史信誉+学生认证）
1. `AccountCapability.CanSubmit` + `BlockingReasons[]`（补天合规考核未完成=全平台不能提交）
2. `practical_certification` + `certification_condition`（补天专属SRC需至少1个有效漏洞）
3. `bank_bound` / `wechat_bound`（漏洞盒子高级项目筛选、补天收款/通知）
4. `pending_submission_limit`（补天新用户5个待审核）
5. `level_exp`（漏洞盒子见习经验值9；补天实习LV1）
6. `joined_program_ids`（漏洞盒子2个已加入企业SRC）
7. `team_required` / `student_verification_required`（补天GROW校园战队）

### 项目级（Program）

8. `EligibilityGate{Kind, Required, Satisfied, Blocking, Evidence}`（先知报名、补天实战认证、漏洞盒子NDA/审核）
9. `gates.nda_required` + `nda_document_type`（漏洞盒子《保密承诺书》、补天众测NDA）
10. `gates.application_review_required` / `invitation_code_required`
11. `scope.visibility`（PUBLIC / GATED_BY_SIGNUP / GATED_BY_NDA / GATED_BY_APPROVAL；**UNKNOWN 不能当空范围**）
12. `scope.asset_tiers` 的原生语义（先知超级/关键/核心/一般/边缘；漏洞盒子核心/普通/边缘；补天厂商规模/权重、产品A-E）
13. `scope.scope_change_history`（先知变更记录；漏洞盒子公告变更；补天公告）
14. `constraints.per_interface_daily_limits` + `budget_cap` + `first_come_first_served`（先知淘天反爬）
15. `constraints.test_account_reporting_required` + `vpn_required` + IP白名单
16. `constraints.same_type_pause_threshold`（补天通用2.0同类型>3暂停）
17. `submission.accepted_categories` / `paused_categories`（补天 Web/IoT vs 工控/OS/通用软件暂停）
18. `submission.draft_supported` / `edit_supported` / `withdraw_supported`（漏洞盒子有草稿箱；补天无草稿；提交后一般不可编辑）
19. `reward.tax_basis`（税前/税后；补天存在两种表述）
20. `reward.staged_payment`（补天通用2.0分期付款）
21. `reward.commission`（漏洞盒子众测提成金）
22. `automation_policy.ai_disclosure_fields`（补天GROW需填大模型名称+版本、AI工具名称）
23. `status_changed_at` / `status_frequency`（漏洞盒子项目会数小时内暂停/重开；不能长缓存）
24. `platform.programmatic_interface.internal_api_observed`（三平台都有前端内部API，但无官方文档）

### 架构设计文档

- `../architecture/README.md`：组件图、数据流、技术形态、包布局
- `../architecture/domain-model.md`：完整 Go 类型与不变量
- `../architecture/evidence-facts.md`：Evidence → Fact → State → Decision
- `../architecture/adapters.md`：三平台 Adapter / Transport
- `../architecture/policy-engine.md`：AccountCapability + EligibilityState + 动作矩阵
- `../architecture/opportunity-scheduler.md`：CompetitionSnapshot + 期望收益/成本 + 调度
- `../architecture/harness-model-router.md`：执行边界 + 模型路由
- `../architecture/research-lifecycle.md`：Finding/Verification/Submission/Ledger
- `../architecture/implementation-order.md`：依赖顺序实现计划
- `CORRECTIONS.md`：GROW 日期、经验值、项目计数、Eligibility 模型纠正记录

---

## 六、证据与不确定性

- 每个平台的 `notes.md` 有逐页证据、页面标题、URL、`observed_at`。
- 关键原始规则保存在 `xianzhi/evidence/`（公告、任务列表、项目详情、排行榜）。
- 无法确认的字段一律写 `UNKNOWN`，并说明原因；没有用旧博客或其他SRC类推。
- 敏感信息（手机号、邮箱、身份证、银行卡、Cookie、IP）未写入审计文件。

### 最高优先级提醒

1. 补天当前 **不能提交任何漏洞**，先人工完成合规考核。
2. 先知当前 **0 个已报名任务**，资产范围不可见；先人工报名长期SRC。
3. 漏洞盒子已有 **2 个可提交企业SRC**，但报告提交仍必须人工确认。
4. 任何情况下都不要把“浏览器自动化”理解为“可以大规模扫描”；项目级禁令优先于平台级，平台级禁令优先于任何自动化设计。
