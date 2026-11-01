# Strata / Qwen3.8-Flash-Next IQ3_S — fair benchmark

- Date: 2026-11-01
- Deployment: Strata 0.2.9 (control 0.1.55), engine 0.1.55 CUDA 13, model
  Qwen3.8-Flash-Next IQ3_S (ISTA-DASLab), context 128K, serving on
  `127.0.0.1:8080`
- Hardware: RTX 4090 24 GB (driver 610.62), 96 GB RAM, Ryzen 9 9950X
- Method: streamed `/v1/chat/completions` (`stream_options.include_usage`), so
  the server's own `timings` block separates prefill (`prompt_per_second`) from
  decode (`predicted_per_second`); 3 repetitions per config; one warm-up run
  discarded; every prompt carries a unique nonce so `cached_tokens` stays 0.
- Script: `scripts/run_fair.py`
- Raw: `raw/fair.jsonl`, aggregate in `raw/fair-summary.json`

## Results (median of 3, min–max in parentheses)

| Config | Prompt tokens | Output tokens | TTFT | Prefill tok/s | Decode tok/s | Draft accept | Finish |
|---|---:|---:|---:|---:|---:|---:|---|
| decode-256 | 87 | 256 | 0.6 s | 155.6 | **103.5** (102.3–115.8) | 67.2% | length |
| decode-1024 | 85 | 1024 | 0.7 s | 103.7 | **117.9** (111.4–148.6) | 90.9% | length |
| prefill-1k | 1,081 | 66 | 1.7 s | 660.6 | 85.6 (70.8–94.1) | 81.9% | stop |
| prefill-4k | 4,056 | 111 | 1.9 s | 2,112.0 | 106.1 (98.0–119.8) | 81.5% | length/stop |
| prefill-16k | 15,989 | 119 | 4.9 s | 3,260.9 | 99.0 (95.9–117.8) | 68.6% | length/stop |
| prefill-32k | 31,991 | 105 | 9.7 s | 3,296.4 | 115.4 (107.8–127.5) | 78.4% | length/stop |

`cached_tokens` was 0 in every run. The decode rate for the prefill configs is
measured over 66–119 tokens, so it is noisier than the decode configs; the
prefill columns and TTFT are the meaningful results there.

## Resource peaks

| Config | GPU util max | VRAM max | Power max | Temp max |
|---|---:|---:|---:|---:|
| decode-256 | 100% | 23,485 MiB | 274 W | 50 °C |
| decode-1024 | 100% | 23,511 MiB | 307 W | 57 °C |
| prefill-1k | 100% | 23,467 MiB | 267 W | 58 °C |
| prefill-4k | 100% | 23,475 MiB | 255 W | 58 °C |
| prefill-16k | 100% | 23,467 MiB | 309 W | 67 °C |
| prefill-32k | 100% | 23,467 MiB | 315 W | 73 °C |

VRAM stayed within 23.5 of 24.5 GB (about 1.0 GB free). The 32K prefill reached
73 °C and 315 W.

## Findings

1. **Decode speed is workload-dependent, driven by MTP draft acceptance.**
   Highly predictable output (integers 1–500) accepted 67–91% of draft tokens and
   decoded at 103–149 tok/s; prose and code in the initial run accepted fewer and
   decoded at ~80–86 tok/s. The auto-tune's 138–146 tok/s is reachable
   (`decode-1024` max 148.6) but is not the sustained average for open-ended text.
2. **Prefill is ~3.3K tok/s from 16K to 32K tokens**, and TTFT scales linearly
   (4.9 s at 16K, 9.7 s at 32K). At 1K tokens a fixed ~1.6 s overhead dominates.
3. **The cost of long context is prefill, not decode.** Decode stays around
   95–127 tok/s after a 16K/32K prompt once generation starts.
4. **VRAM headroom is ~1.0 GB.** Sustained 32K prefill takes the GPU to 73 °C and
   315 W; another large CUDA workload would contend for the card.
5. The earlier initial run's "80–86 tok/s" figure should be read as
   **prose/code decode**, not the engine's ceiling.

## Limitations

- 3 repetitions per config; no long-duration thermal soak.
- `decode-256` and `decode-1024` hit `max_tokens` (`finish_reason=length`); that
  is intentional (fixed output budget) and does not invalidate decode timing.
- Prefill configs answer a needle-in-a-haystack question; their decode numbers
  come from short answers.
- Server `timings` are authoritative for prefill/decode; the client wall time
  also includes network and scheduling overhead (about 0.1–0.3 s).

## Reproduce

```powershell
$env:PYTHONIOENCODING='utf-8'
& 'D:\Dev\Strata\terminal\current\python\python.exe' .\docs\benchmarks\strata\2026-11-01-fair\scripts\run_fair.py
```

Writes `raw/fair.jsonl` and `raw/fair-summary.json`.
