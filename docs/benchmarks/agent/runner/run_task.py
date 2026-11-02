# -*- coding: utf-8 -*-
"""Benchmark task runner: isolated worktree -> optional agent -> acceptance -> run record.

Usage examples
  python run_task.py --list
  python run_task.py --all --arm mock --repeat 1
  python run_task.py --all --arm oracle --repeat 2
  python run_task.py --tasks t01_calc_fix --arm external \
      --agent-cmd "claude -p {prompt} --dangerously-skip-permissions" --repeat 2

Every run happens in a fresh temp copy of the fixture; the fixture itself is never modified.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

from sandbox import (  # noqa: E402
    DockerSandbox,
    build_probe_input,
    compare_hidden,
    docker_available,
    is_docker_error,
    sweep_stale,
)

ROOT = HERE.parent                      # experiments/
BENCH = ROOT / "benchmark"
FIXTURES = BENCH / "fixtures"
TASKS = BENCH / "tasks"
SOLUTIONS = BENCH / "solutions"
DEFAULT_OUT = ROOT / "runs" / "run.jsonl"
PROBE_PATH = HERE / "sandbox" / "probe.py"
SECRET_RE = re.compile(r"(sk-[A-Za-z0-9]{12,}|ANTHROPIC_API_KEY\s*=|OPENAI_API_KEY\s*=|password\s*=\s*['\"]?[^\s'\"]+)", re.I)


def load_tasks(ids=None):
    out = []
    for p in sorted(TASKS.glob("*.json")):
        spec = json.loads(p.read_text(encoding="utf-8"))
        if ids and spec["id"] not in ids:
            continue
        out.append(spec)
    return out


def run_cmd(cmd, cwd, timeout):
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    started = time.time()
    try:
        p = subprocess.run(cmd, cwd=str(cwd), shell=True, capture_output=True, text=True,
                           encoding="utf-8", errors="replace", timeout=timeout, env=env)
        return {"cmd": cmd, "exit_code": p.returncode, "stdout": p.stdout[-4000:],
                "stderr": p.stderr[-4000:], "timed_out": False, "duration_s": round(time.time() - started, 2)}
    except subprocess.TimeoutExpired as e:
        return {"cmd": cmd, "exit_code": None, "stdout": (e.stdout or "")[-4000:] if isinstance(e.stdout, str) else "",
                "stderr": "timeout", "timed_out": True, "duration_s": round(time.time() - started, 2)}


def parse_unittest(text):
    ran = 0
    m = re.search(r"Ran (\d+) tests?", text)
    if m:
        ran = int(m.group(1))
    failed = 0
    m = re.search(r"FAILED \((.*?)\)", text)
    if m:
        for part in m.group(1).split(","):
            part = part.strip()
            if part.startswith("failures="):
                failed += int(part.split("=")[1])
            elif part.startswith("errors="):
                failed += int(part.split("=")[1])
    passed = max(0, ran - failed)
    return {"tests_run": ran, "tests_failed": failed, "tests_passed": passed, "parsed_ok": "OK" in text or "FAILED" in text}


def git(cwd, args, timeout=60):
    return run_cmd(
        "git -c user.email=bench@example.com -c user.name=bench "
        "-c core.autocrlf=false -c safe.directory=* " + args,
        cwd, timeout,
    )


def init_repo(work):
    git(work, "init -q")
    git(work, "add -A")
    git(work, "commit -qm baseline")


def changed_files(work):
    st = git(work, "status --porcelain")
    files = []
    for line in (st["stdout"] or "").splitlines():
        line = line.rstrip()
        if len(line) < 4:
            continue
        path = line[3:].strip()
        if " -> " in path:
            path = path.split(" -> ")[-1]
        path = path.replace("\\", "/")
        if ("__pycache__/" in path or path.endswith(".pyc") or ".pytest_cache/" in path
                or path == "agent_usage.json"):
            continue
        files.append(path)
    return files


def diff_stats(work):
    d = git(work, "diff --numstat HEAD")
    ins = dele = 0
    for line in (d["stdout"] or "").splitlines():
        parts = line.split("\t")
        if len(parts) >= 3:
            try:
                ins += int(parts[0])
                dele += int(parts[1])
            except ValueError:
                pass
    return ins, dele


def scan_secrets(work, files):
    hits = []
    diff = git(work, "diff HEAD")
    chunks = [diff["stdout"] or ""]
    for f in files:
        p = work / f
        if p.is_file() and p.stat().st_size < 200_000:
            try:
                chunks.append(p.read_text(encoding="utf-8", errors="replace"))
            except Exception:
                pass
    for c in chunks:
        for m in SECRET_RE.finditer(c):
            hits.append(m.group(1)[:40])
    return hits[:10]


def refresh_copied_files(src, work):
    """Make a copied patch visible to git and Python.

    ``copytree`` preserves the source mtime. When the patch and the fixture
    file have the same size and the same second-resolution mtime, git's stat
    cache reports no change and Python reuses the baseline's ``.pyc``. Touch
    the targets and drop caches generated before the patch was applied.
    """
    for p in src.rglob("*"):
        if p.is_file():
            target = work / p.relative_to(src)
            if target.is_file():
                os.utime(target, None)
    for cache in list(work.rglob("__pycache__")):
        shutil.rmtree(cache, ignore_errors=True)
    for pyc in work.rglob("*.pyc"):
        pyc.unlink(missing_ok=True)


def apply_oracle(work, spec):
    src = SOLUTIONS / spec["id"].split("_")[0]
    if not src.exists():
        raise FileNotFoundError(f"solution not found: {src}")
    shutil.copytree(src, work, dirs_exist_ok=True)
    refresh_copied_files(src, work)


def apply_shallow(work, spec):
    src = SOLUTIONS / (spec["id"].split("_")[0] + "_shallow")
    if not src.exists():
        raise FileNotFoundError(f"shallow solution not found: {src}")
    shutil.copytree(src, work, dirs_exist_ok=True)
    refresh_copied_files(src, work)


def run_arm(spec, arm, agent_cmd, work, sandbox_container=None):
    info = {"agent_exit": None, "agent_stdout": "", "agent_stderr": "", "agent_timed_out": False}
    started = time.time()
    prompt = spec["prompt"]
    if arm == "mock":
        info["agent_exit"] = 0
    elif arm == "oracle":
        apply_oracle(work, spec)
        info["agent_exit"] = 0
    elif arm == "shallow":
        apply_shallow(work, spec)
        info["agent_exit"] = 0
    elif arm == "external":
        if not agent_cmd:
            raise SystemExit("--agent-cmd is required for arm=external")
        cmd = agent_cmd.format(workdir=str(work), prompt=prompt.replace('"', "'"))
        env = os.environ.copy()
        env["PYTHONIOENCODING"] = "utf-8"
        env["BENCH_PROMPT"] = prompt
        env["BENCH_SANDBOX_CONTAINER"] = sandbox_container or ""
        try:
            p = subprocess.run(cmd, cwd=str(work), shell=True, capture_output=True, text=True,
                               encoding="utf-8", errors="replace", timeout=spec.get("timeout_s", 600) * 6,
                               env=env)
            info.update(agent_exit=p.returncode, agent_stdout=p.stdout[-4000:], agent_stderr=p.stderr[-4000:])
        except subprocess.TimeoutExpired:
            info.update(agent_exit=None, agent_timed_out=True)
    info["agent_duration_s"] = round(time.time() - started, 2)
    usage_path = work / "agent_usage.json"
    if usage_path.exists():
        try:
            info["usage"] = json.loads(usage_path.read_text(encoding="utf-8"))
        except Exception:
            info["usage"] = {"parse_error": True}
    return info


def sandbox_error_record(spec, arm, repeat, run_id, sandbox_info, error):
    return {
        "run_id": run_id,
        "task_id": spec["id"],
        "category": spec.get("category"),
        "context_band": spec.get("context_band"),
        "arm": arm,
        "repeat": repeat,
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "baseline": {"ok": False, "exit_codes": [], "output_tail": ""},
        "result": {"ok": False, "exit_codes": [], "output_tail": ""},
        "agent": {},
        "diff": {"files": [], "files_outside_allowed": [],
                 "insertions": 0, "deletions": 0, "secrets_hit": []},
        "sandbox": sandbox_info,
        "sandbox_error": error,
        "hidden": {"declared": bool(spec.get("hidden_cases")),
                   "skipped": False, "baseline_matched": None,
                   "baseline_error": None, "control_valid": None,
                   "pass": None, "detail": "sandbox_error", "error": None},
        "functional_pass": False,
        "scope_pass": False,
        "agent_completed": False,
        "agent_unsandboxed": False,
        "overall_success": False,
        "success": False,
    }


def run_one(spec, arm, agent_cmd, repeat, agent_max_steps=0, sandbox_mode="none"):
    fixture = FIXTURES / spec["fixture"]
    if not fixture.exists():
        raise FileNotFoundError(fixture)
    tmp = Path(tempfile.mkdtemp(prefix=f"bench_{spec['id']}_{arm}_"))
    work = tmp / "work"
    shutil.copytree(fixture, work)
    init_repo(work)
    run_id = uuid.uuid4().hex[:12]
    hidden_cases = spec.get("hidden_cases") or []
    probe_input = build_probe_input(hidden_cases) if hidden_cases else None
    sandbox = None
    sandbox_info = {"mode": sandbox_mode}
    sandbox_error = None
    agent_unsandboxed = False
    started = False
    try:
        if sandbox_mode == "docker":
            image = spec.get("sandbox_image")
            if not image:
                sandbox_error = "missing sandbox_image"
            elif not docker_available():
                sandbox_error = "docker unavailable"
            else:
                sandbox = DockerSandbox(work, image, run_id)
                sandbox_info.update({
                    "backend": "docker", "enforcement": "full",
                    "image": image, "image_digest": sandbox.image_digest(),
                    "agent_container": sandbox.name, "user": sandbox.user,
                })
        if sandbox_error:
            return sandbox_error_record(spec, arm, repeat, run_id,
                                        sandbox_info, sandbox_error)

        probe_arg = PROBE_PATH if probe_input is not None else None
        if sandbox is None:
            baseline = [run_cmd(c, work, spec.get("timeout_s", 120))
                        for c in spec["acceptance"]]
            baseline_probe = None
        else:
            ar = sandbox.run_acceptance(spec["acceptance"], probe_arg, probe_input,
                                        timeout_s=spec.get("timeout_s", 120))
            if any(is_docker_error(r) for r in ar.commands) or (
                    ar.probe is not None and is_docker_error(ar.probe)):
                return sandbox_error_record(spec, arm, repeat, run_id,
                                            sandbox_info, "acceptance docker error")
            baseline = [{"cmd": c, "exit_code": r.exit_code, "stdout": r.stdout,
                         "stderr": r.stderr, "timed_out": r.timed_out, "duration_s": 0.0}
                        for c, r in zip(spec["acceptance"], ar.commands)]
            baseline_probe = ar.probe
        baseline_ok = all(r["exit_code"] == 0 for r in baseline)
        baseline_parse = parse_unittest("\n".join(r["stdout"] + r["stderr"] for r in baseline))

        try:
            if sandbox is not None and arm == "external":
                try:
                    sandbox.start_agent()
                    started = True
                except Exception as exc:
                    sandbox_error = f"start_agent: {exc}"
            if not sandbox_error:
                try:
                    agent = run_arm(spec, arm, agent_cmd, work,
                                    sandbox_container=sandbox.name if started else None)
                except Exception as exc:
                    sandbox_error = f"agent: {exc}"
        finally:
            if sandbox is not None and started:
                stop = sandbox.stop()
                if stop.exit_code != 0:
                    sandbox_error = "stop failed"
        if sandbox_error:
            return sandbox_error_record(spec, arm, repeat, run_id,
                                        sandbox_info, sandbox_error)

        if sandbox is None:
            after = [run_cmd(c, work, spec.get("timeout_s", 120))
                     for c in spec["acceptance"]]
            after_probe = None
        else:
            ar = sandbox.run_acceptance(spec["acceptance"], probe_arg, probe_input,
                                        timeout_s=spec.get("timeout_s", 120))
            if any(is_docker_error(r) for r in ar.commands) or (
                    ar.probe is not None and is_docker_error(ar.probe)):
                return sandbox_error_record(spec, arm, repeat, run_id,
                                            sandbox_info, "acceptance docker error")
            after = [{"cmd": c, "exit_code": r.exit_code, "stdout": r.stdout,
                      "stderr": r.stderr, "timed_out": r.timed_out, "duration_s": 0.0}
                     for c, r in zip(spec["acceptance"], ar.commands)]
            after_probe = ar.probe
        after_ok = all(r["exit_code"] == 0 for r in after)
        after_parse = parse_unittest("\n".join(r["stdout"] + r["stderr"] for r in after))

        if sandbox is not None and arm == "external":
            usage = agent.get("usage") or {}
            if usage.get("sandbox_error"):
                sandbox_error = f"agent: {usage['sandbox_error']}"
            elif usage.get("sandbox_container") != sandbox.name:
                agent_unsandboxed = True
        if sandbox_error:
            return sandbox_error_record(spec, arm, repeat, run_id,
                                        sandbox_info, sandbox_error)

        files = changed_files(work)
        ins, dele = diff_stats(work)
        outside = [f for f in files if f not in spec.get("allowed_files", [])]
        secrets = scan_secrets(work, files)

        usage = agent.get("usage") or {}
        max_steps = 0
        if agent_cmd:
            match = re.search(r"--max-steps\s+(\d+)", agent_cmd)
            if match:
                max_steps = int(match.group(1))
        max_steps = int(spec.get("max_steps") or 0) or max_steps or int(agent_max_steps or 0)
        step_cap_hit = False
        step_cap_known = False
        if arm in ("oracle", "mock", "shallow"):
            agent_completed = True
        else:
            if usage.get("step_cap_hit") is not None:
                step_cap_hit = bool(usage.get("step_cap_hit"))
                step_cap_known = True
            if max_steps:
                step_cap_known = True
                step_cap_hit = step_cap_hit or (
                    int(usage.get("steps") or 0) >= max_steps and not usage.get("completed")
                )
            exit_ok = agent.get("agent_exit") == 0 and not agent.get("agent_timed_out")
            completed_flag = usage.get("completed")
            if completed_flag is None:
                # A non-adapter CLI (cloud arm) has no completion flag; its exit
                # status is the only completion signal available.
                agent_completed = bool(exit_ok and not step_cap_hit)
            else:
                agent_completed = bool(exit_ok and completed_flag is True and not step_cap_hit)

        functional_pass = bool(after_ok)
        scope_pass = bool(not outside and not secrets and not agent.get("agent_timed_out"))

        if sandbox is None:
            hidden = {
                "declared": bool(hidden_cases), "skipped": bool(hidden_cases),
                "baseline_matched": None, "baseline_error": None,
                "control_valid": None, "pass": None,
                "detail": "skipped (sandbox none)", "error": None,
            }
        elif hidden_cases:
            base = compare_hidden(baseline_probe, hidden_cases)
            final = compare_hidden(after_probe, hidden_cases)
            baseline_matched = base["pass"] is True
            baseline_error = base["detail"] == "probe_error"
            control_valid = not baseline_matched and not baseline_error
            hidden = {
                "declared": True, "skipped": False,
                "baseline_matched": baseline_matched,
                "baseline_error": baseline_error,
                "control_valid": control_valid,
                "pass": bool(control_valid and final["pass"]),
                "detail": final["detail"],
                "error": None if control_valid else "broken_hidden_control",
            }
        else:
            hidden = {
                "declared": False, "skipped": False,
                "baseline_matched": None, "baseline_error": None,
                "control_valid": None, "pass": None,
                "detail": "no cases", "error": None,
            }

        overall_success = (
            functional_pass and scope_pass and agent_completed
            and hidden["pass"] is not False
            and not agent_unsandboxed
        )
        rec = {
            "run_id": run_id,
            "task_id": spec["id"],
            "category": spec.get("category"),
            "context_band": spec.get("context_band"),
            "arm": arm,
            "repeat": repeat,
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
            "baseline": {"ok": baseline_ok, **baseline_parse,
                         "exit_codes": [r["exit_code"] for r in baseline],
                         "output_tail": "\n".join((r["stdout"] + r["stderr"])[-1200:] for r in baseline)},
            "result": {"ok": after_ok, **after_parse,
                       "exit_codes": [r["exit_code"] for r in after],
                       "output_tail": "\n".join((r["stdout"] + r["stderr"])[-1200:] for r in after)},
            "agent": agent,
            "diff": {"files": files, "files_outside_allowed": outside,
                     "insertions": ins, "deletions": dele, "secrets_hit": secrets},
            "sandbox": sandbox_info,
            "sandbox_error": None,
            "hidden": hidden,
            "agent_unsandboxed": agent_unsandboxed,
            "functional_pass": functional_pass,
            "scope_pass": scope_pass,
            "agent_completed": agent_completed,
            "agent_max_steps": max_steps,
            "step_cap_hit": step_cap_hit,
            "step_cap_known": step_cap_known,
            "overall_success": overall_success,
            "success": overall_success,
            "wall_clock_s": round(sum(r["duration_s"] for r in baseline) +
                                  sum(r["duration_s"] for r in after) +
                                  agent.get("agent_duration_s", 0), 2),
        }
        return rec
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", nargs="*", help="task ids; default all")
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--arm", default="mock",
                    choices=["mock", "oracle", "shallow", "external"])
    ap.add_argument("--agent-cmd", default="")
    ap.add_argument("--repeat", type=int, default=1)
    ap.add_argument("--agent-max-steps", type=int, default=0,
                    help="known step budget for a non-adapter agent command")
    ap.add_argument("--out", default=str(DEFAULT_OUT))
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--sandbox", choices=["docker", "none"], default="docker")
    args = ap.parse_args()

    if args.sandbox == "docker":
        sweep_stale()

    tasks = load_tasks(None if args.all or not args.tasks else set(args.tasks))
    if args.list:
        for t in tasks:
            print(f"{t['id']:28s} {t['category']:10s} {t['context_band']:8s} {t['fixture']}")
        return
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    ok = 0
    for spec in tasks:
        for rep in range(1, args.repeat + 1):
            rec = run_one(spec, args.arm, args.agent_cmd, rep,
                          args.agent_max_steps, sandbox_mode=args.sandbox)
            with out.open("a", encoding="utf-8") as f:
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")
            ok += rec["success"]
            print(f"{rec['task_id']:28s} arm={rec['arm']:8s} rep={rep} "
                  f"baseline={'ok' if rec['baseline']['ok'] else 'fail':4s} "
                  f"result={'PASS' if rec['result']['ok'] else 'FAIL':4s} "
                  f"success={rec['success']} files={len(rec['diff']['files'])}")
    print(f"\n{ok}/{len(tasks) * args.repeat} runs counted as success -> {out}")


if __name__ == "__main__":
    main()
