# Long-horizon tasks: single prompt vs multi-turn vs staged handoff

- Date: 2026-11-02
- Task: `t24_longlib_full` — implement four Python modules (text, stats, store,
  CLI) against 27 tests. Baseline: all 27 tests fail. Oracle: `solutions/t24`.
- Budgets: single/multi-turn `max_steps=40`; staged `max_steps=20` per stage.
- Modes: thinking on/off; 1 run per cell (preliminary).
- Raw: `runs/paired_long_single.jsonl`, `runs/multiturn_on.jsonl`,
  `runs/multiturn_off.jsonl`, `runs/staged_fixed_on.jsonl`,
  `runs/staged_fixed_off.jsonl`

## 1. Three ways to carry state

| Mode | How the next stage knows what happened |
|---|---|
| Single prompt | the model sees the whole task and its own tool history |
| Multi-turn, same session | four user prompts in one conversation; tool history retained |
| Staged, fresh process | repository + deterministic `HANDOFF.md` (completed stages, changed files, last full-suite status, next instruction); no conversation memory |

## 2. Results

| Mode | Thinking | Success | Wall | Steps | Input tokens | Output tokens | Denied writes |
|---|---|---:|---:|---:|---:|---:|---:|
| Single prompt | off | ✅ | **30.4 s** | 10 | 30,338 | 2,312 | 0 |
| Single prompt | on | ✅ | 78.6 s | 17 | 94,133 | 6,329 | 1 |
| Multi-turn, same session | off | ✅ | 35.8 s | 18 | 59,690 | 2,235 | 0 |
| Multi-turn, same session | on | ✅ | **64.8 s** | 22 | 81,961 | 3,972 | 0 |
| Staged handoff | off | ✅ | 111.3 s | 57 | 157,505 | 7,536 | 10 |
| Staged handoff | on | ✅ | 179.7 s | 51 | 172,840 | 12,565 | 6 |

All six runs finished with all 27 tests passing. The task is long in steps but
not hard enough to separate success rates.

Per-stage (staged off): text 20 steps (hit the 20-step cap), stats 18, store 8,
CLI 11. Staged on: 10 / 10 / 15 / 16.

## 3. Findings

1. **Same-session multi-turn is the best cost/robustness trade-off here.**
   With thinking on it costs 64.8 s / 81,961 input tokens, versus 78.6 s /
   94,133 for one monolithic prompt and 179.7 s / 172,840 for staged handoff.
   With thinking off, single prompt is cheapest (30.4 s), multi-turn is close
   (35.8 s), staged is 3× more expensive (111.3 s).
2. **Staged fresh sessions pay a large re-reading tax.** Four processes re-read
   the repository and rebuild context each time; input tokens are 2–3× the
   same-session modes. The handoff itself is tiny (364–485 chars) and was enough
   for correctness, but not for cost.
3. **Staged mode loops more and hits scope walls.** The model attempted 6–10
   writes outside the per-stage allowlist and the text stage used all 20 steps.
   The allowlist prevented side effects; a union allowlist in multi-turn had 0
   denials but also no scope protection.
4. **Context grows without compaction.** Multi-turn per-turn input tokens:
   on 7,736 → 10,674 → 31,394 → 32,157; off 6,159 → 10,115 → 15,806 → 27,610.
   The jump at the store stage comes from reading several files. There is no
   masking, summarization or re-anchor in the current adapter.
5. **Thinking off is consistently cheaper** (1.6–2× less wall, roughly half the
   tokens) and lost nothing on this task. This does not contradict the earlier
   t16 failure; that task was numerically exacting, this one is ordinary code.
6. **A 40-step budget is sufficient for four modules; 20 steps per stage is
   tight** (the text stage hit the cap). Larger modules need a bigger stage cap
   or a stall detector/retry rather than a hard 20.

## 4. Harness implications

- Default to **same-session multi-turn** for a task that fits the context; use
  staged handoff only for crash recovery, context overflow, or very large
  repositories.
- Before tasks longer than ~30K tokens per call, add a **context assembler**
  (evidence view, masking of old tool results, re-anchor of system/plan, cap).
- Handoff must carry more than test status for harder tasks: open subgoals,
  known failing assertions, changed-file semantics, and the next acceptance
  command. The current deterministic handoff was sufficient but minimal.
- Keep the **pre-execution write allowlist**, but make it plan-aware: a union
  allowlist avoids rework but removes discipline; a per-stage allowlist causes
  denied-write loops. The harness should widen the allowlist only when the plan
  explicitly advances.
- Pair the step budget with **loop/no-progress detection**; a hard 20-step cap
  stopped a legitimate stage (text 20/20) and would fail a larger module.

## 5. Limitations

- One task, one run per cell; not enough to claim a success-rate difference.
- The task is synthetic and small in code size (~250 lines of tests and oracle);
  it exercises long tool sequences, not a large repository.
- Multi-turn uses the same four stage prompts as the staged experiment; order
  and wording are shared, but the handoff text differs by design.
- The staged runner's earlier accounting bug was fixed before these numbers; the
  raw file is `staged_fixed_*.jsonl`.
