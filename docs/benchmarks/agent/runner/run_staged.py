# -*- coding: utf-8 -*-
"""Long-horizon staged task with cross-session handoff.

The agent runs in a fresh process per stage. Between stages the harness writes a
deterministic HANDOFF.md (completed stages, changed files, last test status,
next instruction); the model must resume from the repository plus that handoff,
not from conversation memory.

Usage: python run_staged.py [--no-thinking] [--reps 1]
Writes runs/staged_longlib.jsonl and runs/staged-summary.json.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
FIXTURE = ROOT / "benchmark" / "fixtures" / "longlib"
RUNS = ROOT / "runs"
ADAPTER = HERE / "agent_openai_tools.py"
BASE = "http://127.0.0.1:8080/v1"
MODEL = "qwen3.8-flash-next-iq3_s"
PY = sys.executable

STAGES = [
    {
        "name": "text",
        "prompt": "Stage 1/4: implement slugify and truncate in longlib/text.py so tests/test_text.py passes. Do not modify the tests.",
        "tests": "test_text.py",
        "allowed": ["longlib/text.py", "longlib/__init__.py"],
    },
    {
        "name": "stats",
        "prompt": "Stage 2/4: implement mean, median and percentile in longlib/stats.py so tests/test_stats.py passes. Do not modify the tests.",
        "tests": "test_stats.py",
        "allowed": ["longlib/stats.py", "longlib/__init__.py"],
    },
    {
        "name": "store",
        "prompt": "Stage 3/4: implement JsonStore in longlib/store.py so tests/test_store.py passes. Do not modify the tests.",
        "tests": "test_store.py",
        "allowed": ["longlib/store.py", "longlib/__init__.py"],
    },
    {
        "name": "cli",
        "prompt": "Stage 4/4: implement the CLI in longlib/cli.py so tests/test_cli.py passes and the full suite stays green. Do not modify the tests.",
        "tests": "test_cli.py",
        "allowed": ["longlib/cli.py", "longlib/__init__.py"],
    },
]

FULL = [PY, "-m", "unittest", "discover", "-s", "tests", "-v"]


def run(cmd, cwd, env, timeout):
    started = time.perf_counter()
    try:
        proc = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True,
                              encoding="utf-8", errors="replace", timeout=timeout, env=env)
        return {
            "exit": proc.returncode,
            "stdout": proc.stdout[-4000:],
            "stderr": proc.stderr[-2000:],
            "timed_out": False,
            "duration_s": round(time.perf_counter() - started, 2),
        }
    except subprocess.TimeoutExpired as exc:
        return {"exit": None, "stdout": (exc.stdout or "")[-4000:] if isinstance(exc.stdout, str) else "",
                "stderr": "timeout", "timed_out": True,
                "duration_s": round(time.perf_counter() - started, 2)}


def stage_tests(pattern):
    return [PY, "-m", "unittest", "discover", "-s", "tests", "-p", pattern, "-v"]


def changed_files(work):
    proc = subprocess.run(["git", "status", "--porcelain"], cwd=str(work), capture_output=True,
                          text=True, encoding="utf-8", errors="replace")
    files = []
    for line in (proc.stdout or "").splitlines():
        if len(line) >= 4:
            path = line[3:].strip().replace("\\", "/")
            if " -> " in path:
                path = path.split(" -> ")[-1]
            if path and path != "agent_usage.json" and not path.startswith("__pycache__"):
                files.append(path)
    return sorted(files)


def handoff_text(work, completed, last_full, next_stage):
    full_state = "PASS" if last_full and last_full["exit"] == 0 else "FAIL"
    return (
        "# Handoff\n\n"
        f"Completed stages: {', '.join(completed) or '(none)'}\n"
        f"Changed files: {', '.join(changed_files(work)) or '(none)'}\n"
        f"Last full-suite run: {full_state}\n"
        f"Next stage: {next_stage['name']} — {next_stage['prompt']}\n"
        "Read the repository and HANDOFF.md, then continue. Do not modify tests.\n"
    )


def one_run(no_thinking, rep, out_path):
    tmp = tempfile.mkdtemp(prefix="staged_longlib_")
    work = Path(tmp) / "work"
    shutil.copytree(FIXTURE, work)
    subprocess.run(["git", "init", "-q"], cwd=str(work))
    subprocess.run(["git", "add", "-A"], cwd=str(work))
    subprocess.run(["git", "-c", "user.email=bench@example.com", "-c", "user.name=bench",
                    "commit", "-qm", "baseline"], cwd=str(work))
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    env["NO_PROXY"] = "127.0.0.1,localhost"
    records = []
    completed = []
    last_full = None
    total_input = total_output = total_wall = 0
    try:
        for index, stage in enumerate(STAGES, start=1):
            handoff = work / "HANDOFF.md"
            handoff.write_text(handoff_text(work, completed, last_full, stage), encoding="utf-8")
            usage_path = work / "agent_usage.json"
            usage_path.unlink(missing_ok=True)
            prompt = (
                "You are continuing a multi-stage coding task in this repository. "
                "Read HANDOFF.md first. "
                + stage["prompt"]
                + f" Run the stage tests with: {PY} -m unittest discover -s tests -p {stage['tests']} -v."
            )
            cmd = [PY, "-I", "-X", "utf8", str(ADAPTER), "--workdir", str(work),
                   "--prompt", prompt, "--base-url", BASE, "--model", MODEL, "--max-steps", "20",
                   "--allowed-files", ",".join(stage["allowed"])]
            if no_thinking:
                cmd.append("--no-thinking")
            agent = run(cmd, work, env, timeout=900)
            usage = {}
            if usage_path.exists():
                try:
                    usage = json.loads(usage_path.read_text(encoding="utf-8"))
                except json.JSONDecodeError:
                    usage = {"parse_error": True}
            stage_tests_result = run(stage_tests(stage["tests"]), work, env, timeout=300)
            full = run(FULL, work, env, timeout=300)
            last_full = full
            # A stage is complete when its own tests pass. The full suite is
            # expected to fail until the later stages exist; only the final
            # suite gates overall success.
            ok = stage_tests_result["exit"] == 0
            if ok:
                completed.append(stage["name"])
            total_input += int(usage.get("input_tokens") or 0)
            total_output += int(usage.get("output_tokens") or 0)
            total_wall += agent["duration_s"]
            record = {
                "run": rep, "thinking": "off" if no_thinking else "on",
                "stage": stage["name"], "stage_index": index,
                "agent_exit": agent["exit"], "agent_timed_out": agent["timed_out"],
                "stage_ok": stage_tests_result["exit"] == 0,
                "stage_exit": stage_tests_result["exit"],
                "full_ok": full["exit"] == 0,
                "full_exit": full["exit"],
                "success": ok,
                "wall_s": agent["duration_s"],
                "usage": usage,
                "handoff_chars": len(handoff.read_text(encoding="utf-8")),
                "handoff": handoff.read_text(encoding="utf-8"),
                "changed_files": changed_files(work),
                "stage_output_tail": (stage_tests_result["stdout"] + stage_tests_result["stderr"])[-1200:],
                "full_output_tail": (full["stdout"] + full["stderr"])[-1200:],
            }
            records.append(record)
            with out_path.open("a", encoding="utf-8") as fh:
                fh.write(json.dumps(record, ensure_ascii=False) + "\n")
            print(f"run={rep} think={record['thinking']} stage={stage['name']:5s} "
                  f"stage_ok={record['stage_ok']} full_ok={record['full_ok']} "
                  f"wall={record['wall_s']}s steps={usage.get('steps')} "
                  f"changed={record['changed_files']}", flush=True)
        final = run(FULL, work, env, timeout=300)
        summary = {
            "run": rep, "thinking": "off" if no_thinking else "on",
            "stages": len(STAGES), "completed": completed,
            "all_stages_ok": len(completed) == len(STAGES),
            "final_full_ok": final["exit"] == 0,
            "success": len(completed) == len(STAGES) and final["exit"] == 0,
            "total_wall_s": round(total_wall, 1),
            "total_input_tokens": total_input,
            "total_output_tokens": total_output,
            "final_output_tail": final["stdout"][-1200:],
        }
        return summary
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-thinking", action="store_true")
    ap.add_argument("--reps", type=int, default=1)
    ap.add_argument("--out", default="")
    ap.add_argument("--summary", default="")
    args = ap.parse_args()
    RUNS.mkdir(parents=True, exist_ok=True)
    out = Path(args.out) if args.out else RUNS / "staged_longlib.jsonl"
    summary_path = Path(args.summary) if args.summary else RUNS / "staged-summary.json"
    out.write_text("", encoding="utf-8")
    summaries = []
    for rep in range(1, args.reps + 1):
        summaries.append(one_run(args.no_thinking, rep, out))
    print(json.dumps(summaries, ensure_ascii=False, indent=2))
    summary_path.write_text(
        json.dumps(summaries, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
