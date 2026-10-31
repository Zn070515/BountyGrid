# BountyGrid 只读探针

- 本机（当前 Windows）只做讨论、审计与**只读探针**；产品代码在台式机开发。
- 探针是**可丢弃脚本**，不属于产品代码，不进入 Go module。
- 探针只验证：会话状态、页面选择器、日期/字段解析、项目计数、账号状态。
- **禁止**：报名、领取任务、签NDA、合规考核、实战认证、提交漏洞、提现、绑定银行卡、修改资料。

## 运行前提

1. 已安装 `agent-browser`。
2. 在 Git Bash / WSL 中运行；需要 `python`（仅用于 JSON 解析/格式化）。
3. 探针只读页面；如遇登录失效，会输出 `logged_in:false`，不会尝试登录。

## 登录态（每个平台独立会话 + state）

探针**自动携带登录凭证**，不再依赖单个 `bounty` 会话：

| 平台 | 会话 | state 文件 |
|------|------|-----------|
| 先知 | `xzprobe` | `%USERPROFILE%\.agent-browser\state\bounty-xianzhi.json` |
| 漏洞盒子 | `vbprobe` | `%USERPROFILE%\.agent-browser\state\bounty-vulbox.json` |
| 补天 | `btprobe` | `%USERPROFILE%\.agent-browser\state\bounty-butian.json` |

`probe.sh` 的 `open_wait` 会在启动时带 `--state` 恢复 Cookie/localStorage。若 Cookie 已过期：

```bash
bash probes/session/relogin.sh vulbox    # 或 xianzhi / butian
```

该脚本只打开登录页、等待人工登录、成功后保存 state；**不代填账号、不代勾协议、不点登录**。

## 目录

```text
probes/
├── README.md
├── lib/probe.sh                 # 通用 shell 辅助
├── session/check-sessions.sh    # 三平台登录态检查
├── butian/grow-dates.sh         # GROW 报名/活动日期（纠正回归）
├── butian/account-summary.sh    # 补天账号能力/合规/认证
├── vulbox/account-summary.sh    # 漏洞盒子等级/经验/信誉/赛季/已加入项目
└── xianzhi/task-summary.sh      # 先知任务总数/状态/类型
```

## 输出

- 默认输出 JSON 到 stdout；可重定向到 `probes/out/<name>.json`。
- 不输出 Cookie、密码、手机号、邮箱、身份证、银行卡。
- 每个探针末尾打印 `SAFETY: read-only`。

## 约定

- 会话名默认 `bounty`，可 `SESSION=xxx ./probes/...` 覆盖。
- 页面等待统一 `wait --load networkidle` + 短等待；不硬编码长 sleep。
- 选择器以当前页面为准；页面改版时探针失败是预期行为，不猜测。
- 发现敏感值：只记录“存在/已配置”，不写入输出。
