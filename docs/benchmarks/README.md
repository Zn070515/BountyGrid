# Benchmarks

Model and inference benchmark records for the local deployments used with
BountyGrid. These files are evidence of measured behavior, not product code and
not a quality guarantee.

## Layout

```text
benchmarks/
  <target>/                         e.g. strata
    <YYYY-MM-DD>-<label>/
      summary.md                    what ran, results, findings, limits
      raw/*.jsonl                   per-call measurements (usage, timing, sampler peaks)
      scripts/*.py                  the exact script that produced raw/
```

## Rules

- Never store prompts with account data, cookies, tokens, or personal values.
- Keep the raw measurements and the exact script together; a summary without
  its raw file is not reproducible.
- Record the deployment (engine version, model, quant, context, GPU, driver)
  before the numbers.
- A single run is not a verdict. Record the task, `max_tokens`, and
  `finish_reason` so truncation is visible.

## Current records

| Date | Target | Deployment | Summary |
|---|---|---|---|
| 2026-11-01 | Strata | Qwen3.8-Flash-Next IQ3_S, engine 0.1.55 CUDA, RTX 4090 | [initial](strata/2026-11-01-initial/summary.md) · [fair](strata/2026-11-01-fair/summary.md) · [matrix + agent](strata/2026-11-01-matrix/summary.md) |
