# Benchmark Execution Sandbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run every agent command and acceptance suite inside a two-phase Docker sandbox, with hidden expectations kept on the host.

**Architecture:** The host keeps the model loop and the worktree; `run_task.py` starts a long-lived agent container, routes `docker exec` through the adapter, destroys it, then runs a fresh acceptance container. Hidden checks use a data-free probe in the acceptance container and compare its output to expectations held only on the host.

**Tech Stack:** Python 3.13 runner, Docker Desktop/WSL2 Linux containers, `unittest`, existing `runner/` harness.

**Spec:** [`docs/superpowers/specs/2026-11-02-benchmark-sandbox-design.md`](../specs/2026-11-02-benchmark-sandbox-design.md)

**Working directory for every command:** `docs/benchmarks/agent` unless a command starts with `git`.

---

## File Structure

| Path | Responsibility |
|---|---|
| `runner/sandbox/__init__.py` | Re-export the public sandbox API |
| `runner/sandbox/hidden.py` | Build probe input, parse marked output, compare expectations (host-only) |
| `runner/sandbox/probe.py` | Data-free call probe executed inside the acceptance container |
| `runner/sandbox/docker.py` | `DockerSandbox`, `docker_available`, `docker_exec`, Docker error classification |
| `runner/sandbox/Dockerfile.bench` | Pinned Python image; copies nothing from the build context |
| `runner/agent_openai_tools.py` | Accept an injected executor; record `sandbox_container` in usage |
| `runner/run_task.py` | `--sandbox`, two-phase orchestration, hidden comparison, run record |
| `runner/run_paired.py` | Pass `sandbox_mode="none"` explicitly (paired tasks have no images yet) |
| `runner/test_sandbox.py` | Unit tests: args, hidden comparison, error classification, no-Docker orchestration |
| `runner/test_sandbox_integration.py` | `skipUnless(docker_available())` end-to-end and isolation tests |
| `benchmark/tasks/t01_calc_fix.json` | `sandbox_image` + `hidden_cases` |
| `benchmark/solutions/t01_shallow/calc/ops.py` | Negative control that passes only the public tests |
| `docs/benchmarks/agent/README.md` | Sandbox build/run/limitations section |

---

## Task 1: Sandbox package and host-side hidden comparison

**Files:**
- Create: `runner/sandbox/__init__.py`
- Create: `runner/sandbox/hidden.py`
- Test: `runner/test_sandbox.py`

- [ ] **Step 1: Write the failing tests**

Create `runner/test_sandbox.py`:

```python
# -*- coding: utf-8 -*-
"""Unit tests for the Docker sandbox and host-side hidden comparison.

Run from docs/benchmarks/agent:
    python runner/test_sandbox.py
"""
import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from sandbox.hidden import build_probe_input, compare_hidden, parse_probe_output


class ProbeInputTests(unittest.TestCase):
    def test_expectations_are_never_serialized(self):
        cases = [{"module": "calc.ops", "func": "add", "args": [2, 3], "expect": 5}]
        self.assertEqual(json.loads(build_probe_input(cases)),
                         [{"module": "calc.ops", "func": "add", "args": [2, 3]}])

    def test_builds_a_list_for_multiple_cases(self):
        cases = [
            {"module": "m", "func": "f", "args": [1], "expect": 1},
            {"module": "m", "func": "g", "args": [], "expect": 2},
        ]
        self.assertEqual(len(json.loads(build_probe_input(cases))), 2)


class ProbeOutputTests(unittest.TestCase):
    def test_parses_the_marked_line_after_stray_prints(self):
        out = 'debug\n__PROBE_RESULT__[{"ok": true, "value": 5}]\ntrailing\n'
        self.assertEqual(parse_probe_output(out), [{"ok": True, "value": 5}])

    def test_missing_marker_returns_none(self):
        self.assertIsNone(parse_probe_output("no marker here"))

    def test_malformed_json_returns_none(self):
        self.assertIsNone(parse_probe_output("__PROBE_RESULT__{not json}"))

    def test_non_list_payload_returns_none(self):
        self.assertIsNone(parse_probe_output('__PROBE_RESULT__{"ok": true}'))


class CompareHiddenTests(unittest.TestCase):
    def probe(self, payload, exit_code=0):
        from sandbox.docker import ExecResult
        return ExecResult(exit_code, "__PROBE_RESULT__" + json.dumps(payload), "")

    def test_all_cases_match(self):
        cases = [
            {"module": "m", "func": "f", "args": [1], "expect": 10},
            {"module": "m", "func": "f", "args": [2], "expect": 20},
        ]
        result = compare_hidden(self.probe([{"ok": True, "value": 10},
                                            {"ok": True, "value": 20}]), cases)
        self.assertTrue(result["pass"])
        self.assertEqual(result["matched"], 2)
        self.assertEqual(result["detail"], "2/2 cases")

    def test_one_mismatch_fails(self):
        cases = [{"module": "m", "func": "f", "args": [1], "expect": 10}]
        result = compare_hidden(self.probe([{"ok": True, "value": 0}]), cases)
        self.assertFalse(result["pass"])
        self.assertEqual(result["matched"], 0)

    def test_probe_error_is_not_a_mismatch(self):
        cases = [{"module": "m", "func": "f", "args": [1], "expect": 10}]
        result = compare_hidden(self.probe({"ok": False, "error": "boom"}), cases)
        self.assertFalse(result["pass"])
        self.assertEqual(result["detail"], "probe_error")

    def test_no_cases_is_not_declared(self):
        result = compare_hidden(None, [])
        self.assertFalse(result["declared"])
        self.assertIsNone(result["pass"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `ModuleNotFoundError: No module named 'sandbox'`.

- [ ] **Step 3: Implement the package and comparison**

Create `runner/sandbox/__init__.py` (Task 3 extends it with the Docker API):

```python
# -*- coding: utf-8 -*-
"""Docker sandbox for the agent benchmark."""
from .hidden import build_probe_input, compare_hidden, parse_probe_output

__all__ = ["build_probe_input", "compare_hidden", "parse_probe_output"]
```

Create `runner/sandbox/hidden.py`:

```python
# -*- coding: utf-8 -*-
"""Host-side hidden checks: inputs cross into the container, expectations do not."""
import json

RESULT_PREFIX = "__PROBE_RESULT__"


def build_probe_input(cases):
    """Serialize only the call inputs; ``expect`` never leaves the host."""
    payload = [
        {"module": case["module"], "func": case["func"], "args": case["args"]}
        for case in cases
    ]
    return json.dumps(payload)


def parse_probe_output(stdout):
    """Return the marked result list, or None when it is missing or malformed."""
    for line in reversed((stdout or "").splitlines()):
        if line.startswith(RESULT_PREFIX):
            try:
                data = json.loads(line[len(RESULT_PREFIX):])
            except json.JSONDecodeError:
                return None
            return data if isinstance(data, list) else None
    return None


def compare_hidden(probe, cases):
    """Compare a probe result with host-side expectations.

    Returns ``{"declared", "pass", "matched", "total", "detail"}``.
    ``detail == "probe_error"`` means the probe crashed or emitted no marked
    line; callers must not treat that as a model mismatch.
    """
    declared = bool(cases)
    if not declared:
        return {"declared": False, "pass": None, "matched": 0, "total": 0,
                "detail": "no cases"}
    results = parse_probe_output(getattr(probe, "stdout", "") or "")
    if results is None or len(results) != len(cases):
        return {"declared": True, "pass": False, "matched": 0, "total": len(cases),
                "detail": "probe_error"}
    matched = 0
    for case, got in zip(cases, results):
        if isinstance(got, dict) and got.get("ok") is True and got.get("value") == case["expect"]:
            matched += 1
    return {
        "declared": True,
        "pass": matched == len(cases),
        "matched": matched,
        "total": len(cases),
        "detail": f"{matched}/{len(cases)} cases",
    }
```

Create a temporary `runner/sandbox/docker.py` with only `ExecResult` so the import works:

```python
# -*- coding: utf-8 -*-
import dataclasses


@dataclasses.dataclass
class ExecResult:
    exit_code: int | None
    stdout: str = ""
    stderr: str = ""
    timed_out: bool = False
```

The full module is written in Tasks 3 and 4.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 10 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/sandbox/__init__.py \
        docs/benchmarks/agent/runner/sandbox/hidden.py \
        docs/benchmarks/agent/runner/sandbox/docker.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): add host-side hidden comparison"
```

---

## Task 2: Data-free probe

**Files:**
- Create: `runner/sandbox/probe.py`
- Test: `runner/test_sandbox.py` (append `ProbeScriptTests`)

- [ ] **Step 1: Write the failing tests**

Append to `runner/test_sandbox.py`:

```python
import subprocess


class ProbeScriptTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)
        (self.work / "target.py").write_text(
            "def add(a, b):\n    return a + b\n\n"
            "def boom():\n    raise ValueError('nope')\n",
            encoding="utf-8",
        )

    def tearDown(self):
        self.tmp.cleanup()

    def run_probe(self, payload):
        env = {"PYTHONPATH": str(self.work), "PYTHONIOENCODING": "utf-8"}
        p = subprocess.run(
            [sys.executable, str(HERE / "sandbox" / "probe.py")],
            input=json.dumps(payload), capture_output=True, text=True,
            encoding="utf-8", errors="replace", cwd=self.work, env=env,
        )
        return p, parse_probe_output(p.stdout)

    def test_calls_the_target_and_marks_the_result(self):
        p, results = self.run_probe([
            {"module": "target", "func": "add", "args": [2, 3]},
        ])
        self.assertEqual(p.returncode, 0)
        self.assertEqual(results, [{"ok": True, "value": 5}])

    def test_reports_a_target_exception_without_crashing(self):
        p, results = self.run_probe([
            {"module": "target", "func": "boom", "args": []},
        ])
        self.assertEqual(p.returncode, 0)
        self.assertFalse(results[0]["ok"])
        self.assertIn("ValueError", results[0]["error"])

    def test_rejects_malformed_input(self):
        p, results = self.run_probe({"not": "a list"})
        self.assertEqual(p.returncode, 2)
        self.assertIsNone(results)


if __name__ == "__main__":
    unittest.main(verbosity=2)
```

Remove the existing `if __name__ == "__main__":` block before appending, then re-add it once at the end (only one `unittest.main()` call).

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: the probe tests fail because `sandbox/probe.py` does not exist.

- [ ] **Step 3: Implement the probe**

Create `runner/sandbox/probe.py`:

```python
# -*- coding: utf-8 -*-
"""Data-free call probe executed inside the acceptance container.

It reads and validates the whole input before importing any target module, so
agent code cannot consume or alter the case stream on first import.
"""
import importlib
import json
import sys

RESULT_PREFIX = "__PROBE_RESULT__"


def main():
    dumps = json.dumps
    out = sys.stdout
    try:
        cases = json.loads(sys.stdin.read())
        if not isinstance(cases, list):
            raise ValueError("probe input must be a list")
        for case in cases:
            if not isinstance(case, dict) or {"module", "func", "args"} - set(case):
                raise ValueError("probe case needs module/func/args")
    except Exception as exc:
        out.write(f"{RESULT_PREFIX}{dumps({'ok': False, 'error': f'input: {exc}'})}\n")
        out.flush()
        return 2

    results = []
    for case in cases:
        try:
            module = importlib.import_module(case["module"])
            func = getattr(module, case["func"])
            results.append({"ok": True, "value": func(*case["args"])})
        except Exception as exc:
            results.append({"ok": False, "error": f"{type(exc).__name__}: {exc}"})

    try:
        payload = dumps(results)
    except TypeError as exc:
        payload = dumps({"ok": False, "error": f"not json serializable: {exc}"})
    out.write(RESULT_PREFIX + payload + "\n")
    out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 13 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/sandbox/probe.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): add the data-free probe"
```

---

## Task 3: Docker argument construction and availability

**Files:**
- Modify: `runner/sandbox/docker.py`
- Test: `runner/test_sandbox.py` (append `DockerArgsTests`)

- [ ] **Step 1: Write the failing tests**

Append to `runner/test_sandbox.py`:

```python
from unittest import mock

from sandbox.docker import (
    DOCKER_ERROR_PREFIXES,
    DockerSandbox,
    ExecResult,
    docker_available,
    is_docker_error,
    sweep_stale,
)


class DockerArgsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)
        (self.work / ".git").mkdir()
        self.sandbox = DockerSandbox(self.work, "img:1", "abc123", host_uid="1000:1000")

    def tearDown(self):
        self.tmp.cleanup()

    def test_agent_args_lock_down_the_container(self):
        args = self.sandbox.agent_args()
        joined = " ".join(args)
        for needle in ("--network none", "--read-only", "--tmpfs /tmp:rw,size=256m",
                       "--cap-drop=ALL", "--security-opt no-new-privileges",
                       "--pids-limit 256", "--memory 2g", "--cpus 2",
                       "--init", "--ipc=private", "--user 1000:1000",
                       f"{self.work}:/work:rw",
                       f"{self.work / '.git'}:/work/.git:ro"):
            self.assertIn(needle, joined)
        self.assertNotIn("docker.sock", joined)
        self.assertNotIn("seccomp=unconfined", joined)

    def test_acceptance_args_mount_the_probe_read_only(self):
        args = self.sandbox.acceptance_args("python -m unittest", 60,
                                            probe=HERE / "sandbox" / "probe.py",
                                            probe_input=None)
        joined = " ".join(args)
        self.assertIn("probe.py:/probe/probe.py:ro", joined)
        self.assertIn("PYTHONPATH=/work", joined)
        self.assertIn("timeout -s KILL 60 sh -c", joined)

    def test_acceptance_args_without_probe(self):
        args = self.sandbox.acceptance_args("python -m unittest", 60,
                                            probe=None, probe_input=None)
        self.assertNotIn("probe.py", " ".join(args))
        self.assertNotIn("-i", args)

    def test_exec_wraps_with_timeout(self):
        args = self.sandbox.exec_args(["python", "-m", "pytest"], 30)
        self.assertEqual(args[-7:],
                         ["timeout", "-s", "KILL", "30", "python", "-m", "pytest"])
        self.assertIn("exec", args)

    def test_docker_errors_are_classified(self):
        from sandbox.docker import ExecResult
        self.assertTrue(is_docker_error(
            ExecResult(125, "", "Error response from daemon: No such container")))
        self.assertTrue(is_docker_error(
            ExecResult(126, "", "OCI runtime exec failed: exec failed")))
        self.assertFalse(is_docker_error(
            ExecResult(1, "", "AssertionError: -1 != 5")))

    def test_docker_available_is_false_when_the_binary_is_missing(self):
        with mock.patch("subprocess.run", side_effect=FileNotFoundError):
            self.assertFalse(docker_available())
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `ImportError` for `DOCKER_ERROR_PREFIXES`, or `AttributeError` for the new methods.

- [ ] **Step 3: Implement argument construction**

Replace `runner/sandbox/docker.py` with:

```python
# -*- coding: utf-8 -*-
"""Docker-backed sandbox for the agent benchmark."""
import dataclasses
import datetime
import os
import shlex
import subprocess
import time
from pathlib import Path

PROBE_CONTAINER_PATH = "/probe/probe.py"
DEFAULT_IMAGE = "bountygrid-bench:py313"
DOCKER_ERROR_PREFIXES = (
    "Error response from daemon",
    "OCI runtime exec failed",
    "No such container",
    "Cannot connect to the Docker daemon",
    "error during connect",
)


@dataclasses.dataclass
class ExecResult:
    exit_code: int | None
    stdout: str = ""
    stderr: str = ""
    timed_out: bool = False


@dataclasses.dataclass
class AcceptanceResult:
    commands: list[ExecResult]
    probe: ExecResult | None


def _host_uid():
    if hasattr(os, "getuid"):
        return f"{os.getuid()}:{os.getgid()}"
    return None


def is_docker_error(result):
    text = (getattr(result, "stderr", "") or "").strip()
    return any(text.startswith(prefix) for prefix in DOCKER_ERROR_PREFIXES)


def docker_available(timeout_s=5):
    try:
        p = subprocess.run(
            ["docker", "version", "--format", "{{.Server.Version}}"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            timeout=timeout_s,
        )
    except (OSError, subprocess.TimeoutExpired):
        return False
    return p.returncode == 0


def _run(argv, timeout_s, input_text=None):
    try:
        p = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=timeout_s, input=input_text)
        return ExecResult(p.returncode, p.stdout, p.stderr, False)
    except subprocess.TimeoutExpired as exc:
        stdout = exc.stdout or ""
        stderr = exc.stderr or ""
        if isinstance(stdout, bytes):
            stdout = stdout.decode("utf-8", "replace")
        if isinstance(stderr, bytes):
            stderr = stderr.decode("utf-8", "replace")
        return ExecResult(None, stdout, stderr, True)
    except OSError as exc:
        return ExecResult(None, "", str(exc), False)


class DockerSandbox:
    def __init__(self, work, image=DEFAULT_IMAGE, run_id="run",
                 docker_bin="docker", host_uid=None):
        self.work = Path(work).resolve()
        self.image = image
        self.run_id = run_id
        self.docker_bin = docker_bin
        self.name = f"bench-agent-{run_id}"
        self.user = host_uid if host_uid is not None else _host_uid()

    def _user_args(self):
        return ["--user", self.user] if self.user else []

    def common_args(self):
        return [
            "--network", "none",
            "--read-only",
            "--tmpfs", "/tmp:rw,size=256m",
            "--cap-drop=ALL",
            "--security-opt", "no-new-privileges",
            "--pids-limit", "256",
            "--memory", "2g",
            "--cpus", "2",
            "--init",
            "--ipc=private",
            "--label", "bench.runner=bountygrid",
            "--label", f"bench.run_id={self.run_id}",
        ] + self._user_args()

    def agent_args(self):
        return [
            self.docker_bin, "run", "-d", "--rm", "--name", self.name,
            *self.common_args(),
            "-v", f"{self.work}:/work:rw",
            "-v", f"{self.work / '.git'}:/work/.git:ro",
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "GIT_OPTIONAL_LOCKS=0",
            "--entrypoint", "sleep", self.image, "infinity",
        ]

    def exec_args(self, argv, timeout_s):
        return [
            self.docker_bin, "exec", "-w", "/work", self.name,
            "timeout", "-s", "KILL", str(int(timeout_s)), *argv,
        ]

    def acceptance_args(self, command, timeout_s, probe=None, probe_input=None):
        args = [self.docker_bin, "run", "--rm"]
        if probe is not None:
            args.append("-i")
        args += [*self.common_args(),
                 "-v", f"{self.work}:/work:rw"]
        if probe is not None:
            args += ["-v", f"{probe}:/probe/probe.py:ro"]
        args += [
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "GIT_OPTIONAL_LOCKS=0",
            "-e", "PYTHONPATH=/work",
            self.image,
            "sh", "-c",
            f"timeout -s KILL {int(timeout_s)} sh -c {shlex.quote(command)}",
        ]
        return args

    def probe_args(self, probe):
        return [
            self.docker_bin, "run", "--rm", "-i",
            *self.common_args(),
            "-v", f"{self.work}:/work:rw",
            "-v", f"{probe}:/probe/probe.py:ro",
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "PYTHONPATH=/work",
            self.image, "python", PROBE_CONTAINER_PATH,
        ]


def docker_exec(container, argv, timeout_s, docker_bin="docker"):
    return _run(
        [docker_bin, "exec", "-w", "/work", container,
         "timeout", "-s", "KILL", str(int(timeout_s)), *argv],
        int(timeout_s) + 30,
    )


def sweep_stale(max_age_s=7200, docker_bin="docker"):
    """Remove labelled agent containers older than ``max_age_s``."""
    listing = _run([docker_bin, "ps", "--filter", "label=bench.runner=bountygrid",
                    "--format", "{{.Names}}"], 30)
    if listing.exit_code != 0:
        return 0
    now = time.time()
    removed = 0
    for name in listing.stdout.split():
        info = _run([docker_bin, "inspect", "--format", "{{.State.StartedAt}}", name], 15)
        if info.exit_code != 0:
            continue
        try:
            started = datetime.datetime.fromisoformat(
                info.stdout.strip().replace("Z", "+00:00")).timestamp()
        except ValueError:
            continue
        if now - started > max_age_s:
            _run([docker_bin, "rm", "-f", name], 30)
            removed += 1
    return removed
```

Then replace `runner/sandbox/__init__.py` with the full public API:

```python
# -*- coding: utf-8 -*-
"""Docker sandbox for the agent benchmark."""
from .docker import (
    AcceptanceResult,
    DockerSandbox,
    ExecResult,
    docker_available,
    docker_exec,
    is_docker_error,
    sweep_stale,
)
from .hidden import build_probe_input, compare_hidden, parse_probe_output

__all__ = [
    "AcceptanceResult",
    "DockerSandbox",
    "ExecResult",
    "docker_available",
    "docker_exec",
    "is_docker_error",
    "sweep_stale",
    "build_probe_input",
    "compare_hidden",
    "parse_probe_output",
]
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 19 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/sandbox/docker.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): build locked-down Docker arguments"
```

---

## Task 4: Docker lifecycle, execution and acceptance

**Files:**
- Modify: `runner/sandbox/docker.py`
- Test: `runner/test_sandbox.py` (append `DockerLifecycleTests`)

- [ ] **Step 1: Write the failing tests**

Append to `runner/test_sandbox.py`:

```python
class DockerLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)
        (self.work / ".git").mkdir()
        self.sandbox = DockerSandbox(self.work, "img:1", "abc123", host_uid=None)

    def tearDown(self):
        self.tmp.cleanup()

    def test_start_agent_raises_on_docker_failure(self):
        with mock.patch("sandbox.docker._run",
                        return_value=ExecResult(125, "", "Error response from daemon: nope")):
            with self.assertRaises(RuntimeError):
                self.sandbox.start_agent()

    def test_exec_marks_timeouts(self):
        with mock.patch("sandbox.docker._run",
                        return_value=ExecResult(None, "", "", True)):
            self.assertTrue(self.sandbox.exec(["python", "-m", "pytest"], 30).timed_out)

    def test_run_acceptance_runs_commands_and_probe(self):
        calls = []

        def fake_run(argv, timeout_s, input_text=None):
            calls.append((argv, input_text))
            if "python" in argv:
                return ExecResult(0, "__PROBE_RESULT__[]", "")
            return ExecResult(0, "", "")

        with mock.patch("sandbox.docker._run", side_effect=fake_run):
            result = self.sandbox.run_acceptance(
                ["python -m unittest"], HERE / "sandbox" / "probe.py", "[]", timeout_s=60)
        self.assertEqual(len(result.commands), 1)
        self.assertEqual(result.probe.exit_code, 0)
        self.assertEqual(len(calls), 2)

    def test_stop_returns_the_docker_result(self):
        with mock.patch("sandbox.docker._run",
                        return_value=ExecResult(0, self.sandbox.name, "")):
            self.assertEqual(self.sandbox.stop().exit_code, 0)

    def test_image_digest_returns_none_on_failure(self):
        with mock.patch("sandbox.docker._run",
                        return_value=ExecResult(1, "", "no image")):
            self.assertIsNone(self.sandbox.image_digest())


class SweepTests(unittest.TestCase):
    def test_removes_only_old_labelled_containers(self):
        from datetime import datetime, timedelta, timezone
        old = (datetime.now(timezone.utc) - timedelta(hours=3)).isoformat()
        new = (datetime.now(timezone.utc) - timedelta(minutes=5)).isoformat()
        calls = []

        def fake_run(argv, timeout_s, input_text=None):
            calls.append(argv)
            if "ps" in argv:
                return ExecResult(0, "old new", "")
            if "inspect" in argv:
                return ExecResult(0, old if argv[-1] == "old" else new, "")
            return ExecResult(0, "", "")

        with mock.patch("sandbox.docker._run", side_effect=fake_run):
            removed = sweep_stale()
        self.assertEqual(removed, 1)
        self.assertTrue(any("rm" in c and "old" in c for c in calls))
        self.assertFalse(any("rm" in c and "new" in c for c in calls))
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `AttributeError: 'DockerSandbox' object has no attribute 'start_agent'`.

- [ ] **Step 3: Implement the lifecycle**

Append to `DockerSandbox` in `runner/sandbox/docker.py`:

```python
    def start_agent(self):
        result = _run(self.agent_args(), 120)
        if result.exit_code != 0:
            _run([self.docker_bin, "rm", "-f", self.name], 30)
            raise RuntimeError(result.stderr.strip() or "docker run failed")

    def exec(self, argv, timeout_s):
        return _run(self.exec_args(argv, timeout_s), int(timeout_s) + 30)

    def run_acceptance(self, commands, probe, probe_input, timeout_s=120):
        results = [
            _run(self.acceptance_args(command, timeout_s, probe=None), timeout_s + 30)
            for command in commands
        ]
        probe_result = None
        if probe is not None:
            probe_result = _run(self.probe_args(probe), timeout_s + 30,
                                input_text=probe_input or "[]")
        return AcceptanceResult(results, probe_result)

    def stop(self):
        return _run([self.docker_bin, "rm", "-f", self.name], 60)

    def image_digest(self):
        result = _run(
            [self.docker_bin, "image", "inspect", "--format", "{{.Id}}", self.image], 30)
        return result.stdout.strip() if result.exit_code == 0 else None
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 25 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/sandbox/docker.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): execute commands and acceptance in Docker"
```

---

## Task 5: Docker image

**Files:**
- Create: `runner/sandbox/Dockerfile.bench`
- Test: `runner/test_sandbox.py` (append `DockerfileTests`)

- [ ] **Step 1: Write the failing test**

Append to `runner/test_sandbox.py`:

```python
class DockerfileTests(unittest.TestCase):
    def test_image_copies_nothing_from_the_build_context(self):
        text = (HERE / "sandbox" / "Dockerfile.bench").read_text(encoding="utf-8")
        self.assertNotIn("COPY", text)
        self.assertNotIn("ADD ", text)

    def test_probe_is_not_referenced(self):
        text = (HERE / "sandbox" / "Dockerfile.bench").read_text(encoding="utf-8")
        self.assertNotIn("probe.py", text)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `FileNotFoundError` for `Dockerfile.bench`.

- [ ] **Step 3: Write the Dockerfile with the current base digest**

Run:

```bash
docker pull python:3.13-slim
DIGEST=$(docker image inspect --format '{{index .RepoDigests 0}}' python:3.13-slim | sed 's/.*@//')
cat > runner/sandbox/Dockerfile.bench <<EOF
# Pinned by digest; update deliberately, never with a floating tag.
FROM python:3.13-slim@$DIGEST

RUN apt-get update \
    && apt-get install -y --no-install-recommends git coreutils ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
EOF
```

The generated file contains the real digest, not a placeholder. Build:

```bash
docker build -f runner/sandbox/Dockerfile.bench -t bountygrid-bench:py313 runner/sandbox
```

- [ ] **Step 4: Run the test and verify it passes**

Run: `python runner/test_sandbox.py`
Expected: `Ran 27 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/sandbox/Dockerfile.bench \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): add the pinned sandbox image"
```

---

## Task 6: Adapter executor injection

**Files:**
- Modify: `runner/agent_openai_tools.py` (`tool_call`, `run_multiturn`, `main`)
- Test: `runner/test_sandbox.py` (append `AdapterExecutorTests`)

- [ ] **Step 1: Write the failing tests**

Append to `runner/test_sandbox.py`:

```python
import agent_openai_tools as agent


class AdapterExecutorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_run_command_uses_the_injected_executor(self):
        seen = []

        def executor(argv, timeout_s):
            seen.append((argv, timeout_s))
            return ExecResult(0, "ok\n", "", False)

        out = agent.tool_call(self.work, "run_command",
                              {"command": "python -m pytest", "timeout_s": 9999},
                              None, executor=executor)
        self.assertEqual(seen[0][0], ["python", "-m", "pytest"])
        self.assertEqual(seen[0][1], 300)
        self.assertIn("exit=0", out)
        self.assertIn("ok", out)

    def test_run_command_executor_timeout_is_an_error(self):
        def executor(argv, timeout_s):
            return ExecResult(None, "", "", True)

        out = agent.tool_call(self.work, "run_command",
                              {"command": "python -m pytest"}, None, executor=executor)
        self.assertTrue(out.startswith("ERROR: timeout"), out)

    def test_internal_listing_stays_host_side(self):
        (self.work / "hello.py").write_text("", encoding="utf-8")
        out = agent.tool_call(self.work, "run_command", {"command": "dir"},
                              None, executor=lambda argv, t: ExecResult(99, "", "", False))
        self.assertIn("hello.py", out)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `TypeError: tool_call() got an unexpected keyword argument 'executor'`.

- [ ] **Step 3: Implement the injection**

In `runner/agent_openai_tools.py`, change the signature and the `run_command` branch:

```python
def tool_call(workdir, name, args, allowed, executor=None):
```

Replace the external-command execution section with:

```python
        if executor is not None:
            try:
                result = executor(
                    argv,
                    clamp_timeout(args.get("timeout_s", DEFAULT_COMMAND_TIMEOUT)),
                )
            except Exception as exc:
                return f"ERROR: {exc}"
            if getattr(result, "timed_out", False):
                return "ERROR: timeout"
            return (f"exit={result.exit_code}\n"
                    f"stdout:\n{result.stdout[-3000:]}\n"
                    f"stderr:\n{result.stderr[-2000:]}")
        env = os.environ.copy()
```

Keep the existing `subprocess.run` path below it as the host fallback.

Change `run_multiturn(args)` to `run_multiturn(args, executor=None)` and pass
`executor` to every `tool_call`.

In `main()`:

```python
    ap.add_argument("--sandbox-container",
                    default=os.environ.get("BENCH_SANDBOX_CONTAINER", ""))
```

Add a module-level run state near `SYSTEM`:

```python
RUN_STATE = {"sandbox_error": None}
```

After parsing:

```python
    RUN_STATE["sandbox_error"] = None
    executor = None
    if args.sandbox_container:
        from sandbox.docker import docker_exec, is_docker_error

        def executor(argv, timeout_s):
            result = docker_exec(args.sandbox_container, argv, timeout_s)
            if is_docker_error(result):
                RUN_STATE["sandbox_error"] = result.stderr.strip()
                raise RuntimeError(result.stderr.strip())
            return result
```

Pass `executor` to `run_multiturn(args, executor)` and use it in the single-prompt
loop (`tool_call(..., executor=executor)`).

Add `"sandbox_container": args.sandbox_container or None` to both `usage`
dictionaries. Set `usage["sandbox_error"] = RUN_STATE["sandbox_error"]`
immediately before each `agent_usage.json` write, because the error can appear
during the loop after the dict was built.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 30 tests ... OK`.

Run the existing adapter tests:

Run: `python runner/test_command_policy.py`
Expected: `Ran 37 tests ... OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/agent_openai_tools.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): route adapter commands through the sandbox"
```

---

## Task 7: Task metadata and the shallow negative control

**Files:**
- Modify: `benchmark/tasks/t01_calc_fix.json`
- Create: `benchmark/solutions/t01_shallow/calc/ops.py`
- Modify: `runner/run_task.py` (`apply_shallow`, `run_arm`, arm choices)

- [ ] **Step 1: Add the task metadata**

Edit `benchmark/tasks/t01_calc_fix.json` and insert after `allowed_files`:

```json
  "sandbox_image": "bountygrid-bench:py313",
  "hidden_cases": [
    {"module": "calc.ops", "func": "add", "args": [-7, -8], "expect": -15},
    {"module": "calc.ops", "func": "add", "args": [1000000, 1], "expect": 1000001},
    {"module": "calc.ops", "func": "multiply", "args": [-3, 4], "expect": -12},
    {"module": "calc.ops", "func": "multiply", "args": [0, 9], "expect": 0},
    {"module": "calc.ops", "func": "multiply", "args": [1000000, 1000000], "expect": 1000000000000}
  ],
```

- [ ] **Step 2: Create the shallow solution**

Create `benchmark/solutions/t01_shallow/calc/ops.py`:

```python
def add(a, b):
    """Pass the public cases only; wrong on every hidden input."""
    public = {(2, 3): 5, (-4, 1): -3, (0, 0): 0}
    return public.get((a, b), 0)


def multiply(a, b):
    """Return the product of a and b."""
    return a * b
```

- [ ] **Step 3: Add the shallow arm to run_task.py**

Add:

```python
def apply_shallow(work, spec):
    src = SOLUTIONS / (spec["id"].split("_")[0] + "_shallow")
    if not src.exists():
        raise FileNotFoundError(f"shallow solution not found: {src}")
    shutil.copytree(src, work, dirs_exist_ok=True)
    refresh_copied_files(src, work)
```

In `run_arm`, add before the `external` branch:

```python
    elif arm == "shallow":
        apply_shallow(work, spec)
        info["agent_exit"] = 0
```

Change the CLI choices:

```python
    ap.add_argument("--arm", default="mock",
                    choices=["mock", "oracle", "shallow", "external"])
```

Treat `shallow` as a completed control in `run_one`:

```python
        if arm in ("oracle", "mock", "shallow"):
            agent_completed = True
```

- [ ] **Step 4: Verify the control by hand**

Run:

```bash
python runner/run_task.py --tasks t01_calc_fix --arm shallow --out shallow-check.jsonl
```

Expected: `baseline=fail result=PASS success=True` — the shallow solution
passes all four public tests, which is exactly why it is a useful negative
control. The hidden failure is asserted by the integration test in Task 9.
Delete `shallow-check.jsonl` afterwards.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/benchmark/tasks/t01_calc_fix.json \
        docs/benchmarks/agent/benchmark/solutions/t01_shallow \
        docs/benchmarks/agent/runner/run_task.py
git commit -m "feat(benchmarks): add t01 sandbox metadata and shallow control"
```

---

## Task 8: Two-phase orchestration and run records

**Files:**
- Modify: `runner/run_task.py`
- Modify: `runner/run_paired.py`
- Test: `runner/test_sandbox.py` (append `OrchestrationTests`)

- [ ] **Step 1: Write the failing tests**

Append to `runner/test_sandbox.py`:

```python
import run_task as rt


class FakeSandbox:
    def __init__(self, work, image, run_id, **kwargs):
        self.work = Path(work)
        self.image = image
        self.name = f"bench-agent-{run_id}"
        self.user = "1000:1000"
        self.started = False
        self.stopped = False
        self.stop_result = ExecResult(0, self.name, "")

    def start_agent(self):
        self.started = True

    def exec(self, argv, timeout_s):
        return ExecResult(0, "", "")

    def run_acceptance(self, commands, probe, probe_input, timeout_s=120):
        fixed = "a + b" in (self.work / "calc" / "ops.py").read_text(encoding="utf-8")
        code = 0 if fixed else 1
        return AcceptanceResult([ExecResult(code, "", "") for _ in commands],
                                ExecResult(0, "__PROBE_RESULT__[]", "") if probe else None)

    def stop(self):
        self.stopped = True
        return self.stop_result

    def image_digest(self):
        return "sha256:fake"


class OrchestrationTests(unittest.TestCase):
    def setUp(self):
        self.spec = next(s for s in rt.load_tasks({"t01_calc_fix"}))
        self.patch = mock.patch.multiple(
            rt, DockerSandbox=FakeSandbox, docker_available=lambda: True)
        self.patch.start()

    def tearDown(self):
        self.patch.stop()

    def test_missing_image_is_a_sandbox_error(self):
        spec = dict(self.spec)
        spec.pop("sandbox_image")
        rec = rt.run_one(spec, "mock", "", 1, sandbox_mode="docker")
        self.assertEqual(rec["sandbox_error"], "missing sandbox_image")
        self.assertFalse(rec["overall_success"])

    def test_docker_unavailable_is_a_sandbox_error(self):
        with mock.patch.object(rt, "docker_available", return_value=False):
            rec = rt.run_one(self.spec, "mock", "", 1, sandbox_mode="docker")
        self.assertEqual(rec["sandbox_error"], "docker unavailable")
        self.assertFalse(rec["overall_success"])

    def test_none_skips_hidden(self):
        rec = rt.run_one(self.spec, "mock", "", 1, sandbox_mode="none")
        self.assertTrue(rec["hidden"]["skipped"])
        self.assertIsNone(rec["hidden"]["pass"])

    def test_stop_failure_blocks_phase_two(self):
        class FailingStop(FakeSandbox):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, **kwargs)
                self.stop_result = ExecResult(1, "", "Error response from daemon: nope")

        with mock.patch.object(rt, "DockerSandbox", FailingStop):
            rec = rt.run_one(self.spec, "oracle", "", 1, sandbox_mode="docker")
        self.assertEqual(rec["sandbox_error"], "stop failed")
        self.assertFalse(rec["overall_success"])

    def test_agent_without_sandbox_container_is_unsandboxed(self):
        with tempfile.TemporaryDirectory() as d:
            script = Path(d) / "fake_agent.py"
            script.write_text(
                "import json, pathlib\n"
                "pathlib.Path('agent_usage.json').write_text("
                "json.dumps({'completed': True}))\n",
                encoding="utf-8",
            )
            cmd = f'"{sys.executable}" "{script}"'
            rec = rt.run_one(self.spec, "external", cmd, 1, sandbox_mode="docker")
        self.assertTrue(rec["agent_unsandboxed"])
        self.assertFalse(rec["overall_success"])

    def test_git_pins_line_endings_and_safe_directory(self):
        calls = []

        def fake_run(cmd, cwd, timeout):
            calls.append(cmd)
            return {"cmd": cmd, "exit_code": 0, "stdout": "", "stderr": "",
                    "timed_out": False, "duration_s": 0.0}

        with mock.patch.object(rt, "run_cmd", side_effect=fake_run):
            rt.git(Path("."), "status --porcelain")
        self.assertIn("core.autocrlf=false", calls[0])
        self.assertIn("safe.directory=*", calls[0])
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python runner/test_sandbox.py`
Expected: `TypeError: run_one() got an unexpected keyword argument 'sandbox_mode'`.

- [ ] **Step 3: Implement the orchestration**

At the top of `runner/run_task.py`, after `HERE` is defined, add:

```python
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

from sandbox import (  # noqa: E402
    AcceptanceResult,
    DockerSandbox,
    ExecResult,
    build_probe_input,
    compare_hidden,
    docker_available,
    is_docker_error,
    sweep_stale,
)
```

Add `PROBE_PATH = HERE / "sandbox" / "probe.py"`.

Change `git()` to pin line endings and the safe directory:

```python
def git(cwd, args, timeout=60):
    return run_cmd(
        "git -c user.email=bench@example.com -c user.name=bench "
        "-c core.autocrlf=false -c safe.directory=* " + args,
        cwd, timeout,
    )
```

Change `run_arm` to accept the container and export it:

```python
def run_arm(spec, arm, agent_cmd, work, sandbox_container=None):
```

In its `external` branch, add to the environment:

```python
        env["BENCH_SANDBOX_CONTAINER"] = sandbox_container or ""
```

Add `sandbox_mode="none"` as the last parameter of `run_one`:

```python
def run_one(spec, arm, agent_cmd, repeat, agent_max_steps=0, sandbox_mode="none"):
```

Inside, after `init_repo(work)`, add:

```python
    run_id = uuid.uuid4().hex[:12]
    hidden_cases = spec.get("hidden_cases") or []
    probe_input = build_probe_input(hidden_cases) if hidden_cases else None
    sandbox = None
    sandbox_info = {"mode": sandbox_mode}
    sandbox_error = None
    agent_unsandboxed = False
    started = False
```

Inside the `try:` before the baseline, add:

```python
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
```

Add this helper above `run_one`:

```python
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
```

Replace the baseline/agent/after block with:

```python
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
```

Add the hidden comparison after `scope_pass`:

```python
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
```

Change the success calculation:

```python
        overall_success = (
            functional_pass and scope_pass and agent_completed
            and hidden["pass"] is not False
            and not agent_unsandboxed
        )
```

Add to the record:

```python
            "sandbox": sandbox_info,
            "sandbox_error": None,
            "hidden": hidden,
            "agent_unsandboxed": agent_unsandboxed,
```

Update `main()`:

```python
    ap.add_argument("--sandbox", choices=["docker", "none"], default="docker")
```

In `main()`, after parsing, sweep stale containers before running tasks:

```python
    if args.sandbox == "docker":
        sweep_stale()
```

and call:

```python
            rec = run_one(spec, args.arm, args.agent_cmd, rep,
                          args.agent_max_steps, sandbox_mode=args.sandbox)
```

Update `runner/run_paired.py`'s `rt.run_one(...)` call to pass
`sandbox_mode="none"` explicitly, with the comment:

```python
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
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `python runner/test_sandbox.py`
Expected: `Ran 36 tests ... OK`.

Run the existing suites:

```bash
python runner/test_command_policy.py
python runner/test_listing.py
python runner/test_run_task.py
```
Expected: all `OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/benchmarks/agent/runner/run_task.py \
        docs/benchmarks/agent/runner/run_paired.py \
        docs/benchmarks/agent/runner/test_sandbox.py
git commit -m "feat(benchmarks): orchestrate two-phase sandboxed runs"
```

---

## Task 9: Integration tests with Docker

**Files:**
- Create: `runner/test_sandbox_integration.py`

- [ ] **Step 1: Write the integration tests**

Create `runner/test_sandbox_integration.py`:

```python
# -*- coding: utf-8 -*-
"""End-to-end and isolation tests. Skipped unless Docker and the image exist.

Run from docs/benchmarks/agent:
    python runner/test_sandbox_integration.py
"""
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import run_task as rt  # noqa: E402
from sandbox.docker import DEFAULT_IMAGE, DockerSandbox, docker_available  # noqa: E402


def image_exists(image=DEFAULT_IMAGE):
    try:
        import subprocess
        p = subprocess.run(["docker", "image", "inspect", image],
                           capture_output=True, text=True, timeout=10)
        return p.returncode == 0
    except Exception:
        return False


DOCKER_OK = docker_available() and image_exists()


@unittest.skipUnless(DOCKER_OK, "docker daemon or bountygrid-bench:py313 missing")
class EndToEndTests(unittest.TestCase):
    def setUp(self):
        self.spec = next(s for s in rt.load_tasks({"t01_calc_fix"}))

    def test_oracle_passes_public_and_hidden(self):
        rec = rt.run_one(self.spec, "oracle", "", 1, sandbox_mode="docker")
        self.assertIsNone(rec.get("sandbox_error"))
        self.assertTrue(rec["functional_pass"])
        self.assertTrue(rec["hidden"]["control_valid"])
        self.assertTrue(rec["hidden"]["pass"])
        self.assertTrue(rec["overall_success"])

    def test_shallow_fix_fails_hidden(self):
        rec = rt.run_one(self.spec, "shallow", "", 1, sandbox_mode="docker")
        self.assertTrue(rec["functional_pass"])
        self.assertFalse(rec["hidden"]["pass"])
        self.assertFalse(rec["overall_success"])


@unittest.skipUnless(DOCKER_OK, "docker daemon or bountygrid-bench:py313 missing")
class IsolationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name) / "work"
        shutil.copytree(rt.FIXTURES / "calc", self.work)
        rt.init_repo(self.work)
        self.sandbox = DockerSandbox(self.work, DEFAULT_IMAGE, "iso")
        self.sandbox.start_agent()

    def tearDown(self):
        self.sandbox.stop()
        self.tmp.cleanup()

    def exec_code(self, code):
        return self.sandbox.exec(["python", "-c", code], 30)

    def test_cannot_write_the_root_filesystem(self):
        result = self.exec_code("open('/etc/x','w').write('x')")
        self.assertNotEqual(result.exit_code, 0)

    def test_cannot_open_a_network_connection(self):
        result = self.exec_code(
            "import socket; socket.create_connection(('1.1.1.1', 53), 1)")
        self.assertNotEqual(result.exit_code, 0)

    def test_probe_is_not_mounted_in_the_agent_container(self):
        result = self.exec_code("import os; print(os.path.exists('/probe'))")
        self.assertIn("False", result.stdout)

    def test_worktree_is_writable(self):
        result = self.exec_code("open('written.txt','w').write('ok')")
        self.assertEqual(result.exit_code, 0)
        self.assertTrue((self.work / "written.txt").exists())

    def test_no_docker_socket(self):
        result = self.exec_code("import os; print(os.path.exists('/var/run/docker.sock'))")
        self.assertIn("False", result.stdout)

    def test_home_is_tmp(self):
        result = self.exec_code("import os; print(os.environ.get('HOME'))")
        self.assertIn("/tmp", result.stdout)

    def test_repository_is_not_mounted(self):
        result = self.exec_code("import os; print(os.path.exists('/benchmark/tasks'))")
        self.assertIn("False", result.stdout)


class NoneModeTests(unittest.TestCase):
    def test_none_records_skipped_hidden(self):
        spec = next(s for s in rt.load_tasks({"t01_calc_fix"}))
        rec = rt.run_one(spec, "mock", "", 1, sandbox_mode="none")
        self.assertEqual(rec["sandbox"]["mode"], "none")
        self.assertTrue(rec["hidden"]["skipped"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
```

- [ ] **Step 2: Start Docker and build the image**

```bash
# Start Docker Desktop first (GUI or: Start-Process "Docker Desktop")
docker version --format '{{.Server.Version}}'
docker build -f runner/sandbox/Dockerfile.bench -t bountygrid-bench:py313 runner/sandbox
```

Expected: the server version prints, and the build ends with `Successfully tagged
bountygrid-bench:py313`.

- [ ] **Step 3: Run the integration tests**

Run: `python runner/test_sandbox_integration.py`
Expected: `Ran 10 tests ... OK`. If any isolation probe fails, do not weaken the
test; fix the container arguments.

- [ ] **Step 4: Commit**

```bash
git add docs/benchmarks/agent/runner/test_sandbox_integration.py
git commit -m "test(benchmarks): prove sandbox isolation end to end"
```

---

## Task 10: Documentation and full verification

**Files:**
- Modify: `docs/benchmarks/agent/README.md`

- [ ] **Step 1: Add the sandbox section**

Insert after the "Harness fixes (2026-11-02)" section:

```markdown
## Execution sandbox (2026-11-02)

`run_task.py --sandbox docker` (the default) runs every agent command and
acceptance suite in Linux containers:

- agent container: no network, read-only root, only `/work` writable, `.git`
  read-only, no `docker.sock`, no host home;
- acceptance container: a fresh container after the agent is gone, with the
  data-free probe mounted read-only at `/probe/probe.py`;
- hidden expectations stay in the task JSON on the host and never enter a
  container;
- `--sandbox none` is an explicit, recorded host fallback and skips hidden
  checks because the task JSON is readable on the host.

Build the image once:

```bash
docker build -f runner/sandbox/Dockerfile.bench -t bountygrid-bench:py313 runner/sandbox
```

Only `t01_calc_fix` declares `sandbox_image` in this phase. The other tasks
fail closed under the default; run them with `--sandbox none` until they get
images. The `external` arm is only scored when the adapter records the
matching `sandbox_container`.

Known limits: Docker shares the Linux kernel (use gVisor/Kata for kernel-level
threats); the default seccomp profile is the baseline, not a custom deny list;
a source-based hidden test (as opposed to the probe form) is not hidden from
agent code during acceptance.
```

- [ ] **Step 2: Run the full local gate**

```bash
python runner/test_command_policy.py
python runner/test_listing.py
python runner/test_run_task.py
python runner/test_sandbox.py
python runner/test_sandbox_integration.py
```

Expected: every suite `OK` (integration skipped if Docker is stopped).

From the repository root:

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build ./...
```

Expected: all exit 0.

- [ ] **Step 3: Verify the oracle end to end by hand**

Run:

```bash
python runner/run_task.py --tasks t01_calc_fix --arm oracle --sandbox docker \
    --out sandbox-oracle.jsonl
```

Expected:

```text
t01_calc_fix    arm=oracle    rep=1 baseline=fail result=PASS success=True files=1
1/1 runs counted as success
```

Inspect the record:

```bash
python -c "import json,pathlib; r=json.loads(pathlib.Path('sandbox-oracle.jsonl').read_text().splitlines()[-1]); print(r['sandbox']['mode'], r['sandbox']['enforcement'], r['hidden']['pass'], r['overall_success'])"
```

Expected: `docker full True True`. Delete `sandbox-oracle.jsonl` afterwards.

- [ ] **Step 4: Commit**

```bash
git add docs/benchmarks/agent/README.md
git commit -m "docs(benchmarks): document the execution sandbox"
```

---

## Task 11: Final self-review and branch check

- [ ] **Step 1: Run the complete suite one more time**

```bash
python runner/test_command_policy.py
python runner/test_listing.py
python runner/test_run_task.py
python runner/test_sandbox.py
python runner/test_sandbox_integration.py
```

- [ ] **Step 2: Check the record keys against the spec**

```bash
python -c "import json,pathlib; r=json.loads(pathlib.Path('sandbox-oracle.jsonl').read_text().splitlines()[-1]); print(sorted(r.keys())); print(sorted(r['hidden'].keys())); print(sorted(r['sandbox'].keys()))"
```

Expected keys include `sandbox_error`, `hidden`, `agent_unsandboxed`, and the
hidden keys `declared, skipped, baseline_matched, baseline_error,
control_valid, pass, detail, error`.

- [ ] **Step 3: Do not push**

Leave the branch for review. The plan's commits are local until the user asks
to push.
