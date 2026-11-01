# 本地小模型 Harness 调研

> 目标：BountyGrid 的 harness 主要驱动本地/量化模型（当前目标是 Strata + Qwen3.8-Flash-Next IQ3_S 一类本地端点）。本文调研“小模型固有缺点”对应的 harness 特化，不修改代码。
>
> 结论先行：**小模型的问题大多不是“不会推理”，而是工具协议、上下文、失败反馈、验证和 serving stack 的问题。** BountyGrid 应继续坚持“模型只提议，确定性控制层做决定”，并把模型-facing 的 ACI（Agent-Computer Interface）设计成窄、结构化、可验证、可恢复的流水线，而不是把 Codex/Claude Code 的自主循环直接套给小模型。

## 1. 调研范围与证据强度

本文依据三类材料：

1. **同行评审/arXiv 论文**：harness 综述、SWE-agent、Agentless、约束解码、失败反馈、上下文管理、自验证、prompt injection 防御。
2. **开源 harness 实现**：OpenAI Codex、DeepSeek Harness（dsh）、SWE-agent、Claude Code 的公开设计模式。
3. **本地模型/serving 实测**：Ollama/vLLM/SGLang 的 tool-call 差异、BFCL 小模型结果、Strata/Qwen3.8-Flash-Next 的社区实测。

证据强度说明：

- **强**：多来源一致、有可复现实验或生产数据（如 transport 失败占比、约束解码的结构修复、verbatim failure 的反效果）。
- **中**：单篇论文或单一 harness 的结论（如 planning 对弱模型更有用、observation masking 优于 summarization）。
- **弱/社区**：Strata 的具体吞吐、量化退化、Qwen3.8-Flash-Next 的社区 benchmark；这些应作为待验证假设，不要写死成产品事实。

## 2. 关键发现

### 2.1 大多数失败发生在“动作之后”，不是“不会思考”

一项对 39 个本地 open-weight 模型、8,199 次数据库 agent 运行的实测（arXiv:2609.21341）发现：

- 2,100 次模型归因失败中，**1,590 次（75.7%）来自至少调用过一次工具的 run**。
- 失败分类：**transport 36.2%**（用了工具但没交付结果）、**clock 25.8%**（超时/时间）、**verification 20.7%**、**capability only 17.3%**（完全没调用工具）。
- transport 失败大多只是**参数形状错误**（字段放错 key、值放错字段、缺必填字段）。
- 只改服务端 5 处、不碰模型/prompt/sampling，就让 6 个模型从 0/30 → 21/30、0/30 → 16/30、10/30 → 24/30。

**含义**：BountyGrid 的 harness 应该优先修“工具协议、参数校验、错误反馈、超时/重试、结果交付”，而不是先换更大模型。

### 2.2 工具调用可靠性是 serving stack 的一部分

arXiv:2609.26693 发现：

- Ollama 的默认 `tools=` 请求会按模型静态 template flag 被接受/拒绝；Phi-3、Gemma-3 可能在推理前就被拒。
- 同一个请求在 Ollama、vLLM、SGLang 上的处理不同；拒绝和重试耗尽如果没被记录为结构化失败，会被误判成“模型没有调用工具”，甚至报告 0% fidelity。
- 约束解码能消除 parse failure，但可能造成**不终止**；turn-pooled 与 per-instance 估计可差约 **55 个百分点**。

**含义**：本地 harness 必须把 **模型 + 量化 + 推理引擎 + chat template + tool-call 协议 + context cap + 解码参数** 当作一次调用的完整身份记录；否则评测和回归都会把 serving 问题误算成模型能力。

### 2.3 约束解码修结构，不修语义

arXiv:2609.23742 对 0.6B–4B 的 5 个模型、14 个结构化任务：

- 原生 schema validity 78.6–92.9% → 使用 Outlines/XGrammar 后 **100%**。
- **content accuracy 仍有 scale-dependent semantic gap**。
- type coercion 可被约束解码救回；**instruction-semantic failure（如多步 function calling）仍然 CD-resistant**。
- XGrammar 开销小（4–8 ms 编译，1.6–3.7% 吞吐损失）；Outlines 在小模型上可能 2–4.9 s/ schema、9–13% 吞吐税。

**含义**：约束解码应作为**结构门禁**，不能当作正确性保证。BountyGrid 仍需要 evidence grounding、schema 语义校验、policy/verifier 兜底。

### 2.4 把失败调用原样写回上下文，会让小模型更想重复它

arXiv:2608.23651 测试 6 个 135M–1.7B 模型：

- “corrective gain” 全为负，约 **-1.03 nats/action token**（每 token 约 2.8× 重复 odds）。
- 重复失败调用概率从 **0.06 → 0.54**；greedy 下 19% 的 item 会 token-for-token 复现失败调用（失败前 0%）。
- 83% 的伤害来自**失败调用的表面形式**；语义上的“它失败了”贡献很小且方向不稳定。
- **替换成 runtime 生成的失败描述**可消除 76% 的反转，且不增加 token。
- “不要重复”指令无效；**删除失败尝试、从干净上下文重试是最差的 harness**。

**含义**：BountyGrid 的工具失败反馈不要回显原始参数/原始调用。应输出结构化的 `FailureReport`（错误类、缺失字段、允许值、下一步），并配合 loop guard/decoder ban。

### 2.5 小模型的自验证不可靠

- SIEVE（ICML 2026）对 6 个 2B–31B 模型：naive self-check 对 small model **非正收益**；role-separated verification 和 external verifier 才有价值。
- Terminal agent 研究（arXiv:2609.38812）：10 个 agent 在 TerminalBench2.1 上，**61.43%** 的错误候选能被发现，但只有 **49.36%** 被成功修复；主要弱点在“检测和修复”，不是“是否启动验证”。
- Self-Critique Paradox（Snorkel）总结：critique-improve 在模型本来就会的任务上可能**变差**；在模型确实不会的任务上才有“Lazarus effect”。

**含义**：BountyGrid 不能把“模型自检/自我反思”当作 VERIFIED 或 ALLOW 的依据。自验证只能作为**提议**；必须由 external verifier（规则、证据、测试、policy）判定。

### 2.6 上下文不是“越大越好”

- Chroma “Context Rot”：18 个模型在输入变长时性能持续下降；单干扰项就有影响，4 个会叠加；**shuffled haystack 反而优于连贯文本**；~300 token 的聚焦 prompt 可能优于 ~113K token 全量 prompt。
- “Lost in the Middle”：关键信息在中间时下降 10–20%。
- 有效上下文往往只有名义窗口的 **10–20%**；多轮平均下降约 39%；instruction adherence 在 ~4K 输出后出现 cliff。
- JetBrains “The Complexity Trap”（arXiv:2508.21433）：**observation masking**（把旧工具输出替换成占位符）在 SWE-bench Verified 上匹配/超过 LLM summarization，成本约减半；Qwen3-Coder 480B 上 52% 更便宜、solve rate +2.6%。
- ContextCurator（arXiv:2604.11462）：7B 的 context curator 可匹配 GPT-4o 的上下文管理，WebArena +4.8 点、DeepSearch token 减 8×。

**含义**：BountyGrid 的模型上下文应是**策展的 evidence 视图**，不是原始页面/JSON 堆叠。优先 observation masking、稳定 evidence ID、re-anchor，而不是让模型读全量历史。

### 2.7 固定流水线在弱模型上常常打败自主 agent

- Agentless（arXiv:2407.01489）：不做自主决策，固定 **localization → repair → patch validation** 三阶段，SWE-bench Lite 32.00%、$0.70，超过许多复杂 agent；**reproduction test validation** 把 81 → 96 fixes。
- SWE-agent（arXiv:2405.15793）ACI 四原则：动作简单、动作紧凑高效、反馈简洁、**guardrail 防止错误传播**（lint、结果上限、窗口边界）。
- Harness 实证研究（arXiv:2609.20804）：planning 对弱模型是 accuracy scaffold，对强模型更多是 cost saver；predefined tools 对 bash 弱的模型有帮助，bash 强的模型用 bash-only 更便宜；context management 在预算收紧时价值最大，rule-based elision 先于 LLM summarization。
- CodeAct 在小模型上有条件：0.8B/2B 上 whole-program CodeAct 0/3，step-wise tool-calling 2/3；greedy 会 byte-identical 重复失败程序。小模型需要 step-wise + 外部反馈，而不是 one-shot 大程序。
- NVIDIA 立场论文（arXiv:2506.02153）：agent 负载大量是重复、窄任务；SLM 足够、更合适、更经济；未来是异构系统（SLM 默认，LLM 选择性调用）。

**含义**：BountyGrid 不应把本地小模型放进“长程自主 ReAct”。应优先 fixed pipeline / state machine；只有 14B+ 或经过验证的模型才开放有限 planning。

### 2.8 Harness 方差可能大于模型方差

- “Stop Comparing LLM Agents Without Disclosing the Harness”（arXiv:2605.23950）：长程任务中，harness 配置导致的方差可能超过模型选择，甚至造成模型排名反转；不披露 harness 的 leaderboard 不可比。
- Harness 综述（arXiv:2606.20683）把 harness 拆成六个运行时职责：**observation、context、control、action、state、verification**，并强调 model–harness co-evolution。
- 另一份 harness 综述提出 ETCLOVG 七层：Execution/Sandbox、Tooling、Context、Lifecycle/Orchestration、Observability、Verification、Governance。

**含义**：BountyGrid 的小模型评测必须报告 harness 配置；优化 harness 的收益可能比换模型更大。

### 2.9 安全边界不能交给小模型

- CaMeL（arXiv:2503.18813）：从可信 query 提取 control/data flow，让 untrusted retrieved data 不能改变程序流；用 capability 限制工具调用。AgentDojo 上 77% 任务可证明安全（无防御 84%）。
- Progent/AgentDojo 一类工作：动态权限约束；但 static benchmark 上的防御可能被 adaptive attack 绕过。
- 小模型更容易被 indirect prompt injection 影响；本地模型也不能信任。

**含义**：BountyGrid 的 authority 必须留在 Go 控制层：平台授权是 ceiling，Policy/Gateway/ExecutionGrant 是执行边界；模型只能提议。平台页面/Evidence 是 **data**，不是 instruction。

### 2.10 DSH/Codex/Claude Code 的可借鉴机制

**Codex**（已借鉴）：
- 声明式 execpolicy + match/not_match、最严合并、justification。
- 审批分级、sandbox-first、per-request network proxy、Guardian、rollout trace。
- 这些是 enforcement mechanism，不是 authority model；BountyGrid 不能照搬“用户可授权 Agent 更多权限”。

**DeepSeek Harness（dsh）**：
- 一切皆插件（Cordis）：模型适配器、工具、会话日志、agent loop 都是可替换插件；profile/bundle 组合。
- 具体小模型友好组件：
  - `repeat-tool-reminder`：相同 tool + 相同参数的重复调用，在 3/5/8 次时提醒；只建议、不阻塞。
  - `timeout-policy`：每个工具声明自己的 timeout，超时返回结构化 `TOOL_TIMEOUT`。
  - `compaction-tool-result-pruner`：超过 8,192 字符的工具结果裁成 4,096 head + 1,024 tail；原文保留在 session log。
  - `llm-retry`：在 durable agent-step 边界重试；默认 5 次、指数退避；重试事件先落日志再等待。
  - `plan-mode`：先探索/设计，`exit_plan_mode` 提交计划给用户 review；计划模式本身不限制工具，需配合 sandbox/approval。
- 这些正好对应小模型的 loop、timeout、context、retry、planning 缺点。

**Claude Code**：
- plan/todo/subagents/hooks/permissions/context compaction 是成熟的人机协作模式。
- 但它面向强模型；小模型直接套用会把“自主决策”放大成失败。应借用其 **显式状态/审批/压缩**，不借用其默认自主度。

## 3. 小模型缺点 → Harness 特化映射

| 小模型缺点 | 证据 | Harness 特化 | BountyGrid 落点 |
|---|---|---|---|
| tool 参数形状错误、字段放错 | 2609.21341 transport 36.2% | 严格 JSON schema、服务端参数校验、字段级修复提示、拒绝时回显允许字段 | `internal/harness` 工具契约 / ACI |
| 多步 function calling 语义错误 | 2609.23742 semantic gap | 约束解码只做结构门禁；语义校验/工具编排由代码做 | 模型适配器 + validator |
| 重复失败调用 | 2608.23651 | 用 runtime 生成的 FailureReport 替代 verbatim call；repeat guard；decoder ban | 工具错误反馈层 |
| 自验证不可靠 | SIEVE / 2609.38812 | external verifier；模型自检只作提议 | `verify.RuleVerifier` / Policy |
| 长上下文退化 | context rot / lost-in-middle | evidence 视图、observation masking、re-anchor、context cap | context assembler |
| 长程规划弱 | 2609.20804 / Agentless | fixed pipeline / state machine；plan 作为数据；有限 ReAct | ResearchTask 状态机 |
| 代码动作 one-shot 不可靠 | CodeAct small-model caveat | step-wise typed tools，而不是 whole-program code | 工具设计 |
| 量化/引擎差异 | 2609.26693 / Strata community | 记录 serving metadata；按模型/引擎/量化评测 | `model_calls` 扩展 |
| 过度自信/不 abstain | 小模型校准研究 | 强制 confidence/abstain；确定性 gate | 输出 schema |
| prompt injection | CaMeL / AgentDojo | 数据/指令分离；capability；policy 权威 | Evidence/Policy 边界 |

## 4. 建议的 Harness 架构（面向 BountyGrid）

### 4.1 核心原则

```text
Model proposes -> Harness validates -> Policy/Verifier decides -> Human approves -> Action
```

- 模型永远不能直接产生 `ActionDecision`、`VERIFIED`、`SUBMITTED`、`PAID`。
- 模型输出是 **untrusted proposal**；所有事实必须绑定 Evidence。
- 本地模型只负责它擅长的：抽取、分类、总结、草稿、候选假设、代码/规则解释。
- 控制层负责：状态、权限、预算、scope、freshness、验证、提交、账本。

### 4.2 三层任务 profile

| Profile | 适用 | 模型行为 | Harness 行为 |
|---|---|---|---|
| **Extraction** | 页面/规则/证据 → Fact | 单轮、结构化 JSON、引用 evidence ID | 约束解码、schema 校验、evidence grounding、自一致性投票 |
| **Analysis** | 证据归纳、候选漏洞、报告草稿 | 少量步骤、有限工具菜单 | fixed pipeline、state machine、external verifier、human review |
| **Planning** | 14B+/强模型才开放 | 生成 plan 数据（步骤/依赖/验收） | plan 校验、reviewed exit、step budget、不允许自由长程执行 |

### 4.3 工具 ACI 设计

每个工具返回结构化 observation，而不是自由文本：

```json
{
  "status": "ok|error",
  "code": "TOOL_TIMEOUT|INVALID_ARGUMENT|NOT_FOUND|POLICY_DENIED|...",
  "message": "short, model-facing explanation",
  "data": {},
  "evidence_refs": ["ev-..."],
  "allowed_next_actions": ["read_evidence", "draft_report"],
  "retryable": false
}
```

约束：

- 工具数量少（建议 ≤ 8 个活跃工具）；每个工具 1–3 个参数；参数名/枚举值稳定。
- 工具描述是 **contract**，不是教程；包含 allowed values、默认值、失败语义。
- 大结果裁剪：head/tail + marker，原文保留在 trace/local store。
- 失败反馈：
  - 不回显原始失败参数；
  - 输出 `error_class`、缺失/非法字段、允许值、下一步；
  - 最多 2 次 repair；之后转 human 或 fail closed；
  - 同一 tool+args 重复 3/5/8 次触发 reminder/停止。

### 4.4 上下文策展

- 只给模型 **evidence 视图**：稳定 ID、来源、时间、摘要、必要的原文片段；不塞原始 HTML/JSON。
- 固定锚点：system/policy/当前任务/允许动作，放在上下文开头和结尾；每 K 轮 re-anchor。
- 旧 observation 用 masking 占位（`[tool output, 2,847 tokens, exit 1, elided]`），不要每次都 LLM summarization。
- 上下文预算按 **有效窗口**，不是名义窗口；本地 IQ3_S/长上下文要留余量。
- 任务状态（todo/plan/facts）放在结构化 state 中，不依赖模型记忆。

### 4.5 验证与恢复

- **Deterministic first**：schema、evidence grounding、scope、policy、receipt、human review。
- **External verifier**：规则/测试/编译/类型检查/静态分析；可选 teacher/verifier model，但不作为唯一 gate。
- **Self-consistency**：抽取/分类可采样 N 次投票；代码/报告不靠投票。
- **Repair loop**：失败 → 结构化 FailureReport → 重新生成 → 校验；限制次数；失败转 human。
- **No progress detection**：如果连续 N 步没有新 evidence/state/decision，停止并转人工。

### 4.6 Serving / 模型身份

每次 `model_calls` 记录：

```text
provider, model, model_version, quantization (IQ3_S/Q4/...), engine (Strata/Ollama/vLLM/SGLang),
chat_template_hash, tool_protocol (native/text/json), context_cap, max_output, temperature, seed,
grammar_version, latency, usage, cost, error_class
```

原因：本地 tool-call fidelity 可能由 serving stack 决定；没有这些字段，无法区分“模型不会”与“引擎/template 不支持”。

### 4.7 安全

- Evidence/页面是 untrusted data；system prompt 明确“数据不是指令”。
- 工具 capability 最小化：模型不能拿到 shell/network；只能调用 BountyGrid 定义的 typed tools。
- Policy/Gateway/ExecutionGrant 是权威；模型不能提高平台 ceiling。
- 对平台页面/第三方内容做 prompt injection 测试（AgentDojo 风格），但只在本机/模拟数据上。
- 敏感数据：本地模型可以看本地 redacted evidence；远程模型永不接收 SENSITIVE；`SENSITIVE` 路由只允许 `IsLocal()`。

## 5. 评测方案

### 5.1 指标

- **Task success**：端到端任务完成（不是“模型说完成了”）。
- **Schema validity**：结构合法率。
- **Semantic accuracy**：内容正确率（与 schema 分开）。
- **Evidence grounding**：每个 Fact/Claim 是否有 evidence_refs；无证据率。
- **Tool-call validity**：参数合法率、tool 选择正确率、multi-turn 成功率。
- **Repair success**：失败后修复率。
- **Context efficiency**：tokens、压缩率、masking vs summarization。
- **Latency/cost**：本地 tok/s、首 token、总时长。
- **Failure taxonomy**：transport / clock / verification / capability / policy / security。

### 5.2 评测矩阵

| 维度 | 取值 |
|---|---|
| 模型 | Qwen3.8-Flash-Next IQ3_S、Q4、其他 4B/8B/14B |
| 引擎 | Strata、Ollama、vLLM、SGLang（如可用） |
| 解码 | native、XGrammar、llama.cpp GBNF |
| 上下文 | 8K/16K/32K/64K cap；masking vs summarization |
| 反馈 | verbatim failure vs synthesized FailureReport |
| 编排 | fixed pipeline vs ReAct vs plan-mode |
| 工具 | 3/5/8/12 个工具；typed vs bash-only |

### 5.3 消融优先级

1. verbatim vs synthesized failure（预期：synthesized 明显更好）。
2. observation masking vs summarization（预期：masking 更便宜且不差）。
3. constrained decoding on/off（预期：结构修复，语义不变）。
4. fixed pipeline vs ReAct（预期：小模型 fixed 更好）。
5. tool menu size（预期：小菜单更好）。
6. context cap / re-anchor（预期：有效窗口比名义窗口重要）。

## 6. 优先级建议

### P0（接本地模型前必须）
1. **Failure taxonomy + ServingMetadata**：把 transport/clock/verification/capability 和模型/量化/引擎/template 记录下来。
2. **Typed tool ACI + 参数校验 + 结构化 FailureReport**：不回显 verbatim 失败调用。
3. **External verifier**：schema/evidence/policy/verifier；模型自检不作 gate。
4. **Context assembler**：evidence ID、masking、re-anchor、context cap；不塞原始页面。
5. **Loop guards**：repeat 3/5/8、timeout per tool、step budget、no-progress stop。

### P1
6. **Constrained decoding adapter**：XGrammar/llama.cpp grammar，按 schema 生成；语义校验单独做。
7. **Self-consistency**：抽取/分类 N 采样投票；代码/报告不投票。
8. **Plan-as-data**：todo/plan 状态机；小模型只从有限动作菜单选择。
9. **Per-model eval suite**：BountyGrid 任务集 + 失败分类 + harness ablation。

### P2
10. **Teacher/verifier model**：大模型/更强本地模型做关键步骤复核，不作为唯一 gate。
11. **Distillation/fine-tuning**：用 trace/成功轨迹 SFT/DPO 小模型；先有 eval 再做。
12. **Limited planning**：只对 14B+/验证过的模型开放 ReAct/plan；小模型保持 fixed pipeline。

## 7. 不建议的做法

- 不要把 Codex/Claude Code 的“自主长程 agent”默认套给 2B–14B 本地模型。
- 不要让模型自己判 VERIFIED/ALLOW/提交；不要用 self-critique 作为唯一验证。
- 不要把失败调用原样写回上下文；不要用“不要重复”指令代替结构性修复。
- 不要给模型 20+ 工具或自由 shell/network；不要塞原始 HTML/JSON 全量页面。
- 不要把名义 256K 窗口当作可用上下文；不要忽略量化/引擎/template 差异。
- 不要在不同 harness/量化/引擎之间直接比较模型分数；不要 pooled 统计掩盖 per-instance 失败。

## 8. 对 BountyGrid 的具体判断

BountyGrid 的控制层已经比一般 agent 项目严谨：Evidence/Fact/State、Policy/Gateway、ExecutionGrant、Verifier、Submission、Ledger、Trace 都在。**小模型特化不需要重写控制层**，而是补一个“model-facing runtime”：

```text
ResearchTask state machine
  -> ContextAssembler (evidence IDs, masking, re-anchor, cap)
  -> LocalModelAdapter (Strata/OpenAI-compatible, constrained decoding, serving metadata)
  -> OutputValidator (schema, grounding, confidence/abstain)
  -> TypedToolExecutor (small menu, structured observations, synthesized failures)
  -> ExternalVerifier (RuleVerifier/Policy/Gateway/Evidence)
  -> Trace/Eval (failure taxonomy, per-model/per-engine)
```

最重要的三条：

1. **模型只提议，harness 决定。**
2. **失败反馈是 runtime 生成的描述，不是原始失败调用。**
3. **上下文是策展的 evidence 视图，不是全量历史。**

这三条比“再换一个更大的本地模型”更可能提升 BountyGrid 的实际完成率。

## 9. 参考来源

### Harness / agent systems
- [From Question Answering to Task Completion: A Survey on Agent System and Harness Design (arXiv:2606.20683)](https://arxiv.org/abs/2606.20683)
- [Stop Comparing LLM Agents Without Disclosing the Harness (arXiv:2605.23950)](https://arxiv.org/abs/2605.23950)
- [Agent Harness Engineering survey (preprints.org 202606.2203)](https://www.preprints.org/manuscript/202606.2203)
- [Agent Systems with Harness Engineering: A Systematic Survey (Frontiers of Computer Science)](https://journal.hep.com.cn/fcs/EN/10.1007/s11704-026-61123-6)
- [SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering (arXiv:2405.15793)](https://arxiv.org/abs/2405.15793)
- [Agentless: Demystifying LLM-based Software Engineering Agents (arXiv:2407.01489)](https://arxiv.org/abs/2407.01489)
- [An Empirical Study of Harness Design for Coding Agents (arXiv:2609.20804)](https://arxiv.org/abs/2609.20804)

### Small models / tool use / serving
- [Small Language Models are the Future of Agentic AI (arXiv:2506.02153)](https://arxiv.org/abs/2506.02153)
- [What Stops a Small Language Model From Driving a Database Agent (arXiv:2609.21341)](https://arxiv.org/abs/2609.21341)
- [Measuring the Serving Stack Instead of the Model (arXiv:2609.26693)](https://arxiv.org/abs/2609.26693)
- [Constrained Decoding Eliminates Structural Failures in Small LLMs but Reveals a Scale-Dependent Semantic Gap (arXiv:2609.23742)](https://arxiv.org/abs/2609.23742)
- [Feedback That Backfires: Why Small Language Model Agents Repeat the Call They Just Watched Fail (arXiv:2608.23651)](https://arxiv.org/abs/2608.23651)
- [TinyLLM: Evaluation and Optimization of Small Language Models for Agentic Tasks on Edge Devices (arXiv:2511.22138)](https://arxiv.org/abs/2511.22138)
- [The Calls are Coming from Inside the Model: Probing Tool-Calling Errors (arXiv:2608.27750)](https://arxiv.org/abs/2608.27750)
- [CodeAct: Executable Code Actions Elicit Better LLM Agents](https://arxiv.org/abs/2402.01030)
- [CodeAct vs tool-calling on phone-scale models](https://github.com/john-rocky/edge-agent-lab/blob/main/docs/codeact-vs-tool-calling.md)
- [Berkeley Function Calling Leaderboard](https://gorilla.cs.berkeley.edu/leaderboard.html)

### Context / verification
- [Context Rot (Chroma research summary)](https://www.morphllm.com/context-rot)
- [Lost in the Middle (arXiv:2307.03172)](https://arxiv.org/abs/2307.03172)
- [The Complexity Trap: Simple Observation Masking Is as Efficient as LLM Summarization (arXiv:2508.21433)](https://arxiv.org/abs/2508.21433)
- [Escaping the Context Bottleneck: Active Context Curation (arXiv:2604.11462)](https://arxiv.org/abs/2604.11462)
- [SIEVE: Limits of Inference-Only Self-Verification (ICML 2026)](https://icml.cc/virtual/2026/77914)
- [Can Terminal Agents Trust Their Own Verification? (arXiv:2609.38812)](https://arxiv.org/abs/2609.38812)
- [The Self-Critique Paradox (Snorkel)](https://snorkel.ai/blog/the-self-critique-paradox-why-ai-verification-fails-where-its-needed-most/)

### Security
- [Defeating Prompt Injections by Design (CaMeL, arXiv:2503.18813)](https://arxiv.org/abs/2503.18813)
- [Adaptive Evaluation of Out-of-Band Defenses Against Prompt Injection (arXiv:2606.26479)](https://arxiv.org/abs/2606.26479)

### Harness implementations
- [OpenAI Codex](https://github.com/openai/codex)
- [DeepSeek Harness (dsh)](https://github.com/deepseek-ai/deepseek-harness)
- [dsh repeat-tool-reminder](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/guard/repeat-tool-reminder)
- [dsh timeout-policy](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/guard/timeout-policy)
- [dsh compaction-tool-result-pruner](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/compaction/compaction-tool-result-pruner)
- [dsh llm-retry](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/llm/llm-retry)
- [dsh plan-mode](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/plan/plan-mode)

### Local model / engine (community claims, verify locally)
- [Strata (Qwen3.8-Flash-Next local engine)](https://github.com/BlockYugaLabs/Strata)
- [Qwen3.8-Flash-Next GSQ-RCO Strata GGUF (community)](https://huggingface.co/alesha-pro/Qwen3.8-Flash-Next-abliterated-GSQ-RCO-Strata-GGUF)
