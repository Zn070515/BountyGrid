# Strata / Qwen3.8-Flash-Next IQ3_S — matrix + agent benchmark

- Date: 2026-11-01
- Deployment: Strata 0.2.9 (control 0.1.55), engine 0.1.55 CUDA 13, model
  Qwen3.8-Flash-Next IQ3_S (ISTA-DASLab), context 128K, `127.0.0.1:8080`
- Hardware: RTX 4090 24 GB (driver 610.62), 96 GB RAM, Ryzen 9 9950X
- This record has two passes:
  1. throughput/quality matrix with thinking on (`scripts/run_matrix.py`);
  2. thinking-on vs thinking-off pass with adequate output budgets
     (`scripts/run_thinking.py`), plus a coding-agent benchmark
     (`scripts/run_strata_agent.ps1` style command in the log).
- All prompts carry a unique nonce; `cached_tokens` was 0 in every measured run.
- Streaming is used; the server's `timings` block is authoritative for prefill
  and decode. 3 runs per point (2 for the thinking-on retrieval control, 5/10 for
  JSON validity).

## 1. Thinking on: throughput matrix

| Point | Prompt | Output | TTFT | Prefill tok/s | Decode tok/s | Draft |
|---|---:|---:|---:|---:|---:|---:|
| decode-64 | 85 | 64 | 0.6 s | 160.2 | 121.2 | 81.5% |
| decode-256 | 86 | 256 | 0.6 s | 155.0 | 112.0 | 79.0% |
| decode-1024 | 88 | 1024 | 0.7 s | 109.7 | 106.3 | 83.3% |
| decode-2048 | 88 | 2048 | 0.7 s | 109.0 | 98.4 | 77.3% |
| content-structured | 87 | 1024 | 0.7 s | 55.1 | 108.1 | 86.8% |
| content-prose | 93 | 1024 | 0.7 s | 124.9 | 87.7 | 54.8% |
| content-code | 95 | 942 | 0.7 s | 116.5 | 87.6 | 72.7% |
| prefill-1k | 1,081 | 87 | 1.7 s | 661.6 | 76.8 | 79.1% |
| prefill-4k | 4,056 | 128 | 1.9 s | 2,119.6 | 96.2 | 75.1% |
| prefill-8k | 8,052 | 114 | 2.6 s | 3,173.0 | 118.4 | 72.7% |
| prefill-16k | 15,990 | 116 | 4.9 s | 3,263.3 | 113.3 | 78.8% |
| prefill-32k | 31,989 | 126 | 9.7 s | 3,300.7 | 116.8 | 81.0% |
| prefill-64k | 64,014 | 117 | 19.5 s | 3,295.0 | 120.4 | 80.1% |
| json-validity | 119 | 455 | 0.7 s | 186.6 | 93.3 | 73.5% |

- Decode **falls with output length**: 121 tok/s at 64 tokens, 98 tok/s at 2048.
- Content type drives speed through MTP acceptance: structured 108 tok/s
  (draft 86.8%), prose 88 (54.8%), code 88 (72.7%).
- Prefill saturates at **~3.3K tok/s from 8K tokens**; 1K has ~1.6 s of fixed
  overhead. TTFT is linear above that: 16K 4.9 s, 32K 9.7 s, 64K 19.5 s.
- Strict JSON: **4/5 valid** (one run spent its budget on reasoning).
- Peak resources: VRAM 23.5 GB, power 330 W, GPU temperature **79 °C** at 64K.

## 2. First-pass retrieval was budget-limited, not a retrieval failure

The matrix used `max_tokens=128` for needle-in-a-haystack. Most "misses" had
empty content because `reasoning_content` consumed the budget; several
`answer_head` values were `ZX-447` or empty. Re-running with an adequate budget
(and with thinking off) gives the corrected numbers below.

## 3. Thinking on vs off (second pass)

Thinking is disabled with `chat_template_kwargs.enable_thinking=false`; the
top-level `enable_thinking` parameter is ignored by Strata.

### 3.1 Retrieval: 16K/32K contexts, needle at 10/50/90% depth

| Pass | Runs | Accuracy | Output tokens |
|---|---:|---:|---:|
| thinking off, max_tokens 64 | 18 | **100%** (18/18) | 12–13 |
| thinking on, max_tokens 512 | 12 | **100%** (12/12) | 76–181 |

TTFT is unchanged by thinking mode (prefill dominates): 4.9 s at 16K,
9.7 s at 32K. Prefill ~3.2–3.3K tok/s.

### 3.2 Strict JSON on OpenAI-style request (5 keys/shape checks)

| Pass | Runs | Valid | Output tokens |
|---|---:|---:|---:|
| thinking off, max_tokens 256 | 10 | **100%** (10/10) | median 82.5 |
| thinking on, max_tokens 512 | 5 | **80%** (4/5) | median 455 |

### 3.3 Content decode with thinking off

| Content | Decode tok/s | Draft accept |
|---|---:|---:|
| structured (numbers) | **173.9** | 100.0% |
| code | 108.6 | 80.0% |
| prose | 104.6 | 57.3% |

### 3.4 Thinking-off latency probe

| Variant | Output tokens | Wall | Result |
|---|---:|---:|---|
| baseline (thinking on) | 56 | 1.29 s | 127 reasoning chars |
| `chat_template_kwargs.enable_thinking=false` | 14 | 0.38 s | no reasoning |
| `reasoning_effort:"none"` | 14 | 0.34 s | no reasoning |
| `reasoning_effort:"low"` | 39 | 0.77 s | 67 reasoning chars |
| top-level `enable_thinking=false` | 66 | 1.01 s | ignored |

## 4. Coding-agent benchmark (arm B)

Protocol from `HistoryAnalysis/experiments`: same fixtures, prompts, acceptance
commands, and `agent_openai_tools.py` (temperature 0, max 20 steps), only the
endpoint changed to Strata. 4 tasks × 2 repetitions.

| Task | Reps | Strict success | Wall | Steps | Input tokens | Output tokens |
|---|---:|---:|---:|---:|---:|---:|
| t01 calc bugfix | 2 | 2/2 | 5.6–6.0 s | 5 | 4,816 | 303–304 |
| t02 textutil bugfix | 2 | 2/2 | 13.2–14.3 s | 6–8 | 8,792–12,570 | 681–832 |
| t03 inventory feature | 2 | 2/2 | 7.7–9.8 s | 5 | 5,749 | 420–424 |
| t04 multi-file refactor | 2 | **0/2** | 18.4–35.5 s | 8–14 | 12,351–28,106 | 1,134–2,445 |
| **Total** | 8 | **6/8 (75%)** | | | | |

`t04` is not a functional failure: both runs pass all 7 tests. It fails the O3
scope rule because the agent also edited `inventory/__init__.py`, which is not
in `allowed_files` (`inventory/store.py`, `inventory/summary.py`). The earlier
qwen3:8b dry-run failed `t04` functionally (AttributeError / NameError); the
strict 6/8 is the same, but the failure mode differs.

## 5. Findings

1. **Budget must cover reasoning plus answer.** With thinking on, max_tokens 128
   for a one-line retrieval answer is not enough; the answer can be empty while
   the call is billed for reasoning.
2. **Thinking off is a large win for structured work.** JSON validity 100% vs
   80%, 82 vs 455 output tokens, and decode 174 vs 108 tok/s for structured
   output. Use it for planning/JSON/tool-loop steps; keep thinking on for
   genuinely hard reasoning.
3. **Decode is workload-dependent.** Roughly 88–121 tok/s for prose/code and
   108–174 tok/s for highly predictable output; MTP acceptance explains the gap.
4. **Prefill is the long-context cost.** ~3.3K tok/s from 8K to 64K, linear
   TTFT (19.5 s at 64K), while decode stays near 100–120 tok/s.
5. **The agent completes toy coding tasks quickly** (6–35 s, 5–14 steps) but
   respects file scope poorly on a refactor task (modified a third file).
6. **VRAM headroom is ~1 GB**; 64K prefill reaches 79 °C and 330 W.

## 6. Implications for BountyGrid's harness

- Default structured/planning/JSON calls to `enable_thinking=false`; reserve
  thinking for analysis/code where quality matters.
- Always set `max_tokens` to reasoning + answer; validate `finish_reason` and
  treat empty content as a failure, not a passing empty answer.
- Enforce the allowed-file/allowed-action scope in the harness, not only in the
  prompt: Strata passes tests while editing outside the declared scope.
- Keep contexts ≤32K for interactive use; 64K costs ~20 s of prefill per call.
- Do not run another large CUDA workload concurrently (1 GB free VRAM).

## 7. Limitations

- Toy fixtures only; this is not the 10-task Pilot and not a cloud comparison.
- One model/quant/engine and one machine; no thermal soak or multi-hour run.
- 2–3 runs per throughput point; the think-on retrieval control has 2 runs.
- Decode numbers for short answers (retrieval, prefill configs) are noisy; the
  meaningful metrics there are prefill tok/s and TTFT.
- External community figures (4090, 24 GB) are roughly 100–125 tok/s decode and
  2.8–3.6K tok/s prefill, consistent with these results.

## 8. Reproduce

```powershell
$env:PYTHONIOENCODING='utf-8'
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-matrix\scripts\run_matrix.py
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-matrix\scripts\run_thinking.py
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-matrix\scripts\probe_thinking.py
```

Coding-agent command (from `HistoryAnalysis/experiments`):

```powershell
$runner = "C:\Users\16275\Desktop\HistoryAnalysis\experiments\runner"
python -I -X utf8 "$runner\run_task.py" --all --arm external `
  --agent-cmd "python -I -X utf8 $runner\agent_openai_tools.py --workdir {workdir} --prompt-env BENCH_PROMPT --base-url http://127.0.0.1:8080/v1 --model qwen3.8-flash-next-iq3_s --max-steps 20" `
  --repeat 2
```
