# -*- coding: utf-8 -*-
"""Paired local-model evaluation: thinking on/off and scope enforcement A/B.

Fairness controls:
- same fixtures, prompts, acceptance commands, adapter and temperature 0;
- the only changed variable between a pair is `--no-thinking`;
- order alternates by task index and repetition (counterbalanced);
- write allowlists are enforced before a write (reviewer P0);
- 2 repetitions per cell; per-task results plus a bootstrap over tasks.

Writes runs/paired_2026-11-02.jsonl and runs/paired-summary.json.
"""

import importlib.util
import json
import os
import random
import statistics
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("run_task", HERE / "run_task.py")
rt = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(rt)

ADAPTER = str(HERE / "agent_openai_tools.py")
BASE = "http://127.0.0.1:8080/v1"
MODEL = "qwen3.8-flash-next-iq3_s"
RUNS = HERE.parent / "runs"
OUT = Path(os.environ.get("BENCH_OUT") or (RUNS / "paired_2026-11-02.jsonl"))
SUMMARY = Path(os.environ.get("BENCH_SUMMARY") or (RUNS / "paired-summary.json"))
REPS = int(os.environ.get("BENCH_REPS", "2"))
SCOPE_TASKS = {"t04_inventory_refactor", "t10_reporting_refactor"}


def agent_cmd(thinking, enforce, allowed, max_steps=20):
    parts = [
        "python", "-I", "-X", "utf8", f'"{ADAPTER}"',
        "--workdir", "{workdir}",
        "--prompt-env", "BENCH_PROMPT",
        "--base-url", BASE,
        "--model", MODEL,
        "--max-steps", str(max_steps),
    ]
    if thinking == "off":
        parts.append("--no-thinking")
    if enforce and allowed:
        parts += ["--allowed-files", '"' + ",".join(allowed) + '"']
    return " ".join(parts)


def run_cell(spec, thinking, enforce, rep, phase):
    rec = rt.run_one(
        spec,
        "external",
        agent_cmd(thinking, enforce, spec.get("allowed_files", []), spec.get("max_steps", 20)),
        rep,
        spec.get("max_steps", 20),
        sandbox_mode="none",
        # Paired tasks do not declare sandbox_image yet; keep the host path
        # explicit so the record never claims isolation.
    )
    rec["thinking"] = thinking
    rec["enforce_scope"] = bool(enforce)
    rec["phase"] = phase
    with OUT.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
    usage = rec.get("agent", {}).get("usage") or {}
    print(
        f"{phase:10s} {rec['task_id']:28s} think={thinking:3s} scope={str(enforce):5s} "
        f"rep={rep} result={str(rec['result']['ok']):5s} success={rec['success']} "
        f"wall={rec['wall_clock_s']:6.1f}s steps={usage.get('steps')} "
        f"denied={usage.get('denied_writes')} outside={len(rec['diff']['files_outside_allowed'])}",
        flush=True,
    )
    return rec


def rate(rows):
    return (sum(1 for r in rows if r["success"]) / len(rows)) if rows else None


def median(rows, key):
    vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
    return round(statistics.median(vals), 1) if vals else None


def bootstrap_diff(tasks, records, iterations=2000):
    """Bootstrap the paired per-task success-rate difference (on - off)."""
    diffs = []
    for spec in tasks:
        tid = spec["id"]
        on = [r for r in records if r["phase"] == "paired" and r["task_id"] == tid and r["thinking"] == "on"]
        off = [r for r in records if r["phase"] == "paired" and r["task_id"] == tid and r["thinking"] == "off"]
        if on and off:
            diffs.append((rate(on), rate(off)))
    if not diffs:
        return None
    observed = statistics.mean(a - b for a, b in diffs)
    rng = random.Random(20261102)
    samples = []
    for _ in range(iterations):
        pick = [diffs[rng.randrange(len(diffs))] for _ in range(len(diffs))]
        samples.append(statistics.mean(a - b for a, b in pick))
    samples.sort()
    lo = samples[int(0.025 * len(samples))]
    hi = samples[int(0.975 * len(samples)) - 1]
    return {"observed": round(observed, 3), "ci95": [round(lo, 3), round(hi, 3)], "tasks": len(diffs)}


def main():
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("", encoding="utf-8")
    tasks = rt.load_tasks(None)
    only = [x.strip() for x in os.environ.get("BENCH_TASKS", "").split(",") if x.strip()]
    if only:
        tasks = [t for t in tasks if t["id"] in only]
    records = []

    for rep in range(1, REPS + 1):
        for idx, spec in enumerate(tasks):
            order = ["on", "off"] if (idx + rep) % 2 == 0 else ["off", "on"]
            for thinking in order:
                records.append(run_cell(spec, thinking, True, rep, "paired"))

    # Scope enforcement A/B on the two refactor tasks (thinking on).
    scope_specs = [s for s in tasks if s["id"] in SCOPE_TASKS]
    for rep in range(1, REPS + 1):
        for spec in scope_specs:
            for enforce in (True, False):
                records.append(run_cell(spec, "on", enforce, rep, "scope-ab"))

    paired = [r for r in records if r["phase"] == "paired"]
    scope = [r for r in records if r["phase"] == "scope-ab"]
    summary = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "model": MODEL,
        "reps": REPS,
        "tasks": len(tasks),
        "paired": {
            "overall": {
                t: {
                    "runs": len([r for r in paired if r["thinking"] == t]),
                    "success_rate": rate([r for r in paired if r["thinking"] == t]),
                    "median_wall_s": median([r for r in paired if r["thinking"] == t], "wall_clock_s"),
                    "median_input_tokens": median(
                        [r["agent"]["usage"] for r in paired if r["thinking"] == t], "input_tokens"
                    ),
                    "median_output_tokens": median(
                        [r["agent"]["usage"] for r in paired if r["thinking"] == t], "output_tokens"
                    ),
                    "median_steps": median([r["agent"]["usage"] for r in paired if r["thinking"] == t], "steps"),
                }
                for t in ("on", "off")
            },
            "bootstrap_success_diff_on_minus_off": bootstrap_diff(tasks, records),
            "per_task": {
                spec["id"]: {
                    t: {
                        "success": sum(
                            1 for r in paired
                            if r["task_id"] == spec["id"] and r["thinking"] == t and r["success"]
                        ),
                        "runs": len([
                            r for r in paired
                            if r["task_id"] == spec["id"] and r["thinking"] == t
                        ]),
                    }
                    for t in ("on", "off")
                }
                for spec in tasks
            },
        },
        "scope_ab": {
            spec["id"]: {
                str(enforce): {
                    "success": sum(
                        1 for r in scope
                        if r["task_id"] == spec["id"] and r["enforce_scope"] == enforce and r["success"]
                    ),
                    "runs": len([
                        r for r in scope
                        if r["task_id"] == spec["id"] and r["enforce_scope"] == enforce
                    ]),
                    "outside_files": sum(
                        len(r["diff"]["files_outside_allowed"])
                        for r in scope
                        if r["task_id"] == spec["id"] and r["enforce_scope"] == enforce
                    ),
                    "denied_writes": sum(
                        (r["agent"]["usage"] or {}).get("denied_writes") or 0
                        for r in scope
                        if r["task_id"] == spec["id"] and r["enforce_scope"] == enforce
                    ),
                }
                for enforce in (True, False)
            }
            for spec in scope_specs
        },
    }
    SUMMARY.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
