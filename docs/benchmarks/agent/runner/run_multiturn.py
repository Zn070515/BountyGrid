# -*- coding: utf-8 -*-
"""Four-stage longlib task in ONE conversation (multi-turn, same session).

Compares with run_staged.py (fresh process + HANDOFF.md per stage) and with the
single monolithic prompt (t24). The agent receives the four stage prompts in the
same message history; no handoff file is written.

Usage: python run_multiturn.py [--no-thinking]
Writes runs/multiturn_longlib.jsonl and runs/multiturn-summary.json.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
FIXTURE = ROOT / "benchmark" / "fixtures" / "longlib"
RUNS = ROOT / "runs"
ADAPTER = HERE / "agent_openai_tools.py"
BASE = "http://127.0.0.1:8080/v1"
MODEL = "qwen3.8-flash-next-iq3_s"
PY = sys.executable

STAGE_PROMPTS = [
    "Stage 1/4: implement slugify and truncate in longlib/text.py so tests/test_text.py passes. "
    "Do not modify the tests and do not work on other modules. When this stage is done, reply "
    "with a one-line summary and no tool calls, then wait for the next stage.",
    "Stage 2/4: implement mean, median and percentile in longlib/stats.py so tests/test_stats.py passes. "
    "Do not modify the tests and do not work on other modules. When this stage is done, reply "
    "with a one-line summary and no tool calls, then wait for the next stage.",
    "Stage 3/4: implement JsonStore in longlib/store.py so tests/test_store.py passes. "
    "Do not modify the tests and do not work on other modules. When this stage is done, reply "
    "with a one-line summary and no tool calls, then wait for the next stage.",
    "Stage 4/4: implement the CLI in longlib/cli.py so tests/test_cli.py passes and the full suite stays green. "
    "Do not modify the tests. When the full suite passes, reply with a short summary and no tool calls.",
]

ALLOWED = "longlib/__init__.py,longlib/text.py,longlib/stats.py,longlib/store.py,longlib/cli.py"
FULL = [PY, "-m", "unittest", "discover", "-s", "tests", "-v"]


def run(cmd, cwd, env, timeout=600):
    started = time.perf_counter()
    try:
        proc = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True,
                              encoding="utf-8", errors="replace", timeout=timeout, env=env)
        return {"exit": proc.returncode, "stdout": proc.stdout[-4000:],
                "stderr": proc.stderr[-2000:], "timed_out": False,
                "duration_s": round(time.perf_counter() - started, 2)}
    except subprocess.TimeoutExpired as exc:
        return {"exit": None, "stdout": "", "stderr": "timeout", "timed_out": True,
                "duration_s": round(time.perf_counter() - started, 2)}


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


def one_run(no_thinking):
    tmp = tempfile.mkdtemp(prefix="multiturn_longlib_")
    work = Path(tmp) / "work"
    shutil.copytree(FIXTURE, work)
    subprocess.run(["git", "init", "-q"], cwd=str(work))
    subprocess.run(["git", "add", "-A"], cwd=str(work))
    subprocess.run(["git", "-c", "user.email=bench@example.com", "-c", "user.name=bench",
                    "commit", "-qm", "baseline"], cwd=str(work))
    prompts_file = work / "_prompts.json"
    prompts_file.write_text(json.dumps(STAGE_PROMPTS, ensure_ascii=False), encoding="utf-8")
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    env["NO_PROXY"] = "127.0.0.1,localhost"
    cmd = [PY, "-I", "-X", "utf8", str(ADAPTER), "--workdir", str(work),
           "--prompts-file", str(prompts_file), "--base-url", BASE, "--model", MODEL,
           "--max-steps", "40", "--allowed-files", ALLOWED]
    if no_thinking:
        cmd.append("--no-thinking")
    try:
        agent = run(cmd, work, env, timeout=1800)
        usage = {}
        usage_path = work / "agent_usage.json"
        if usage_path.exists():
            try:
                usage = json.loads(usage_path.read_text(encoding="utf-8"))
            except json.JSONDecodeError:
                usage = {"parse_error": True}
        full = run(FULL, work, env, timeout=300)
        record = {
            "thinking": "off" if no_thinking else "on",
            "agent_exit": agent["exit"], "agent_timed_out": agent["timed_out"],
            "success": full["exit"] == 0 and agent["exit"] == 0,
            "full_exit": full["exit"],
            "wall_s": agent["duration_s"],
            "usage": usage,
            "changed_files": changed_files(work),
            "full_output_tail": (full["stdout"] + full["stderr"])[-1500:],
        }
        with (RUNS / "multiturn_longlib.jsonl").open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, ensure_ascii=False) + "\n")
        return record
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-thinking", action="store_true")
    args = ap.parse_args()
    RUNS.mkdir(parents=True, exist_ok=True)
    (RUNS / "multiturn_longlib.jsonl").write_text("", encoding="utf-8")
    record = one_run(args.no_thinking)
    (RUNS / "multiturn-summary.json").write_text(
        json.dumps([record], ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(record, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
