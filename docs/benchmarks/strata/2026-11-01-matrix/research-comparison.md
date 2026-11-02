# 我们的实测 vs 社区/官方数据（RTX 4090 · Qwen3.8-Flash-Next IQ3_S）

- 日期：2026-11-02
- 我们的环境：RTX 4090 24 GB、96 GB RAM、Ryzen 9 9950X、Strata 0.2.9 / engine
  0.1.55 CUDA 13、IQ3_S、128K context、Windows 原生（非 WSL）
- 我们的原始数据：`summary.md`、`raw/matrix.jsonl`、`raw/thinking.jsonl`、
  `raw/agent_pilot.jsonl`

## 1. 一句话结论

**吞吐与社区中位数基本一致，没有异常。** 我们的持续 decode 为 88–121 tok/s
（结构化 thinking-off 最高 174），社区 24 GB 4090 的中位约 108 tok/s；我们的
prefill 3.3K tok/s @16–64K，社区约 2.8–3.6K。差距主要来自显存容量（24 GB vs
48 GB 改装卡）、提示缓存命中率、输出内容可预测性，而不是环境配置错误。

## 2. 逐项对照

| 指标 | 我们的实测 | 社区/官方 | 判断 |
|---|---|---|---|
| decode（散文/代码） | 87.6–121.2 tok/s | 99–128，中位 **108**（Level1Techs 24 GB 投稿） | 一致，略低于中位 |
| decode（结构化） | 108（thinking on）、**174**（thinking off） | 143（4090 单卡，MTP）、164（0.1.42 宣称）、168（48 GB 全专家常驻） | 我们接近；168 需要 48 GB |
| decode 随输出长度 | 64→121、256→112、1024→106、2048→**98** | 未见按长度分档的公开数据 | 新数据：越长越慢 |
| prefill @32K | **3,301 tok/s** | 2,456（4090 实测）、~2,800（中位）、3,314（双卡） | 我们高于 24 GB 中位 |
| prefill @16K | 3,263 | 5.2K（0.1.42 宣称）、3.1K（0.1.40 社区） | 在社区区间内 |
| prefill @64K | 3,295 | 官方 5070 12 GB 表：1,640（IQ3_S） | 不同硬件，不可直比 |
| TTFT @16K / 32K / 64K | 4.9 / 9.7 / **19.5 s** | 32K 约 10.5 s、128K 约 41.3 s（0.1.40 社区） | 线性扩展一致 |
| VRAM | 23.5 / 24.5 GB（剩 ~1 GB） | 社区警告默认 700 MiB reserve 可能触发 10× 崩溃；**1500 起稳定** | 我们在临界区，需关注 |
| 功耗/温度 | 330 W / **79 °C**（64K） | 450 W cap 社区设置 | 正常 |
| 严格 JSON（thinking on） | 4/5（80%） | HF 卡片：v1 失败多为 CoT 吃满预算，控制后 23/25 | 与社区现象一致 |
| 严格 JSON（thinking off） | **10/10（100%）** | 同上，社区也建议控制 thinking | 一致 |
| 检索 16K/32K | **30/30（100%）** | 5090 上 999K 3-needle 测试通过 | 同量级能力 |
| coding agent（玩具任务） | **6/8**，t04 测试通过但改越界文件 | 无公开同协议数据；官方 SWE-bench Pro 62.5、T-Bench 2.1 86.1、Toolathlon 73.5 | 不可直接比较 |
| IQ3_S 质量 | 与公开结论一致 | IQ3_S 基本追平 BF16；IQ3_XXS 约 99% 零样本但 agentic/coding 明显更差 | 选 IQ3_S 正确 |

## 3. 为什么没到 143–168 tok/s

1. **显存容量**：我们是 24 GB，专家缓存 6,163 个（11.7 GB），其余在 RAM；
   168 tok/s 的报告是 48 GB 改装卡、全部专家常驻显存。
2. **提示缓存**：社区报告 ~90% 命中，prefill 和后续轮次会快很多；我们的公平测试
   故意用 nonce 让 `cached_tokens=0`，这是最低速度。
3. **输出内容**：MTP 接受率 prose ≈ 0.55–0.59、code ≈ 0.73–0.88、structured ≈
   0.87–1.00；我们的 88–121 与 108–174 的差异主要由内容类型解释。
4. **配置**：社区建议 `--pool-workers 15`、`--pool-affinity auto`、
   `--vram-reserve-mib 1500+`；我们使用 setup 默认值，未做这三项调优。
5. **版本**：社区峰值多来自 0.1.42 / 0.1.38；0.2.9 的 prefill 提升主要在
   AMD/Linux，4090 没有专门的官方基准。

## 4. 风险与建议

1. **VRAM 余量是当前最大风险**：只剩 ~1 GB，社区在 700 MiB reserve 下出现过
   13.5 tok/s（约 10× 崩溃）。建议重跑 setup 加
   `--vram-reserve-mib 1500`，或改用 `auto`；我们尚未观察到崩溃，但已在临界区。
2. **尝试社区调优**：`--pool-workers 15 --pool-affinity auto`（9950X 为 16 核，
   默认可能是 15；需要重启验证）。预计 +8–12% decode。
3. **量化 warm-cache 场景**：去掉 nonce 重复同一 prompt，测 ~90% 缓存命中下的
   prefill 和端到端 agent 延迟。
4. **thinking 策略要分组**：结构化/JSON/检索默认关闭 thinking；复杂代码与多轮
   agent 保留 thinking（社区报告关闭后会降低工具调用可靠性、增加重试）。
5. **不要并发大型 CUDA 负载**：1 GB 余量不足以再跑 ArtFlow-Plus 之类的任务。

## 5. 社区数据口径提醒

- 绝大多数 4090 数字是**自报**，不是第三方复测；48 GB 改装卡、WSL2 内存分配、
  提示缓存命中率、上下文长度都会显著改变结果。
- 官方 agentic 分数（SWE-bench Pro 62.5、Terminal-Bench 2.1 86.1、
  Toolathlon 73.5、DeepSWE 58.7、LiveCodeBench 91.9）是厂商报告，部分尚未在
  公开榜单独立复核。
- 我们的 coding-agent 只有 4 个玩具任务 ×2 次，不能当作真实项目成功率。

## 6. 来源

- Level1Techs Local LLM Matrix（4090 24 GB IQ3_S 社区投稿，decode 中位 108、
  prefill 中位 ~2800）：
  https://forum.level1techs.com/t/the-local-llm-matrix-best-models-quants-by-vram-tier-16gb-256gb/256443/28
- ModelDock 证据账本（IQ3_S 4090 中位 ~109.3 tok/s）：
  https://modeldock.net/evidence/?model=qwen38_flash_next&rig=rtx4090
- kanaf.info / Qiita 4090 对比（Strata 143 tok/s、prefill 2,456；llama.cpp 41 /
  423；Ollama 40 / 703）：
  https://kanaf.info/posts/tips/local-llm-moe-2gpu/
- Strata 社区 RTX 4090 opt-ins 基准（KV_PREFETCH、TTFT 10.5 s @32K、41.3 s @128K）：
  https://github.com/niko1221/strata/blob/477ac13cbe8e582c20484871ce459d20d82df09d/bench/results/2026-10-06-community-rtx4090-opt-ins/README.md
- lcz.me 4090 48 GB 调优（vram-reserve 700→13.5 tok/s，1500→138.4；pool-workers 15）：
  https://lcz.me/topic/2089
- Strata 0.2.9 release notes（prefill 提升集中在 AMD/Linux）：
  https://github.com/Niko1221/Strata/releases/tag/v0.2.9
- ISTA-DASLab GGUF 卡片（IQ3_S 3.50 bpw，基本追平 BF16）：
  https://huggingface.co/ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-GGUF
- RTX 5090 IQ3_XXS vs IQ3_S 实测（262K、~106.8 tok/s）：
  https://ai-coding.wiselychen.com/strata-qwen38-flash-next-rtx5090-1m-context/
- Qwen3.8-Flash-Next 官方博文与 agentic 分数：
  https://qwen.ai/blog?id=qwen3.8-flash-next
- thinking 控制与 HF 复现（CoT 吃满预算导致空正文）：
  https://huggingface.co/groxaxo/Qwen3.8-Flash-Next-exl3-unsloth-shaped-h8-mtp5/commit/96fda2e537fa3ad0201bab61a8cdcd87c45251db
- club-3090 对 thinking-off 的实测提醒（工具可靠性下降）：
  https://github.com/noonghunna/club-3090/pull/1116
