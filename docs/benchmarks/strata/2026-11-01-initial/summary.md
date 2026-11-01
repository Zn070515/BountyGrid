# Strata / Qwen3.8-Flash-Next IQ3_S — initial benchmark

- Date: 2026-11-01
- Deployment: Strata 0.2.9 (control 0.1.55), engine 0.1.55 CUDA 13, model
  Qwen3.8-Flash-Next IQ3_S (ISTA-DASLab), context 128K, serving on
  `127.0.0.1:8080`
- Hardware: RTX 4090 24 GB (driver 610.62), 96 GB RAM, Ryzen 9 9950X
- Method: direct HTTP API (`/v1/chat/completions`, non-streaming), one call per
  task, resource sampler every 0.5 s (`nvidia-smi` + `psutil`)
- Scripts: `scripts/run_test.py`, `scripts/run_long32k.py`
- Raw: `raw/2026-11-01-iq3s.jsonl`

## Results

| Test | Prompt tokens | Output tokens | Wall | End-to-end tok/s | Finish |
|---|---:|---:|---:|---:|---|
| short-generation | 118 | 2048 | 25.63 s | 79.9 | `length` |
| code-review | 715 | 2048 | 23.94 s | 85.6 | `length` |
| long-context 12.6K | 12,577 | 82 | 5.64 s | 14.5 | `stop` |
| long-context 31.5K | 31,477 | 104 | 11.19 s | 9.3 | `stop` |

The long-context end-to-end rate includes prefill. Estimated prefill:
~2.7K tok/s at 12.6K, ~3.2K tok/s at 31.5K (decode ~80 tok/s assumed).

## Resource peaks

| Metric | short | code-review | 12.6K | 31.5K |
|---|---:|---:|---:|---:|
| GPU util max / avg | 100 / 95.8% | 100 / 98.8% | 100 / 98.9% | 100 / 93.9% |
| VRAM max | 23,393 MiB | 23,360 MiB | 23,368 MiB | 23,359 MiB |
| Power max | 264 W | 281 W | 310 W | 311 W |
| GPU temp max | 53 °C | 59 °C | 63 °C | 62 °C |
| CPU max / avg | 60.9 / 48.8% | 57.2 / 49.1% | 52.1 / 25.0% | 52.3 / 24.2% |
| RAM max | 74.3% | 74.3% | 74.3% | 74.4% |

VRAM stayed at 23.4 / 24.5 GB (about 1.2 GB free) with no paging or OOM.

## Quality observations

- Long-context retrieval was exact at both 12.6K and 31.5K: the model returned
  `ZX-4471 @ section 137`.
- Code review correctly identified the eviction goroutine leak (never selects
  `stop`/`ctx.Done()`) and the unused `ctx`/`ttl` parameters. The answer was
  truncated by the token budget before the full list.
- The short generation produced a coherent HTTP/2/QUIC explanation but stopped
  at about 560 words because `max_tokens` was consumed by reasoning.

## Findings

> The [fair benchmark](../2026-11-01-fair/summary.md) supersedes this run for
> controlled measurements: decode is workload-dependent (roughly 80–106 tok/s
> for prose/code, 118–149 tok/s for highly predictable output).

1. **Sustained decode is ~80–86 tok/s** in this run, below the 138–146 tok/s reported by
   Strata's auto-tune. The tuning number is likely a short-window peak; these
   runs generated 2048 tokens continuously.
2. **`reasoning_content` dominates the budget.** The two 2048-token runs produced
   roughly 6.8K and 7.3K characters of reasoning; the visible answer was 1.7–2.2K
   characters. Coding-agent use must budget reasoning plus answer.
3. **Only ~1.2 GB of VRAM is free.** Another large CUDA workload on this card is
   risky. RAM has ~24.6 GB free.
4. Long context remains correct but end-to-end throughput falls because prefill
   dominates.

## Limitations

- Both generation tests hit `max_tokens` (`finish_reason=length`); their
  throughput is valid for sustained decode, but the answers are incomplete.
- Non-streaming calls do not separate time-to-first-token from decode; prefill
  rates above are estimates.
- One run per task; no variance measurement.
- The 12.6K run was originally intended as 16K; the document produced fewer
  tokens than estimated. The 31.5K run covers the upper target.

## Reproduce

```powershell
$env:PYTHONIOENCODING='utf-8'
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-initial\scripts\run_test.py
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-initial\scripts\run_long32k.py
```

Both scripts write to `raw/2026-11-01-iq3s.jsonl` relative to this benchmark
folder. Set `STRATA_BENCH_OUT` to write elsewhere. `run_test.py` truncates the
file at start; `run_long32k.py` appends to it, so run them in that order.
