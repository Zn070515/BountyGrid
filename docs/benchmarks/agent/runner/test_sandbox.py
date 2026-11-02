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


from unittest import mock

from sandbox.docker import (
    DOCKER_ERROR_PREFIXES,
    AcceptanceResult,
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
        self.assertIn(f"{self.work / '.git'}:/work/.git:ro", joined)

    def test_probe_args_keep_git_read_only(self):
        args = self.sandbox.probe_args(HERE / "sandbox" / "probe.py")
        self.assertIn(f"{self.work / '.git'}:/work/.git:ro", " ".join(args))

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
        self.assertTrue(is_docker_error(
            ExecResult(125, "", "Error response from daemon: No such container")))
        self.assertTrue(is_docker_error(
            ExecResult(126, "", "OCI runtime exec failed: exec failed")))
        self.assertTrue(is_docker_error(
            ExecResult(125, "", "Unable to find image 'bountygrid-bench:py313' locally")))
        self.assertTrue(is_docker_error(
            ExecResult(125, "", "docker: Error response from daemon: pull access denied")))
        self.assertFalse(is_docker_error(
            ExecResult(1, "", "AssertionError: -1 != 5")))
        self.assertFalse(is_docker_error(
            ExecResult(1, "", "docker: cleanup finished")))

    def test_docker_available_is_false_when_the_binary_is_missing(self):
        with mock.patch("subprocess.run", side_effect=FileNotFoundError):
            self.assertFalse(docker_available())


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

    def test_stop_treats_an_already_removed_container_as_success(self):
        with mock.patch("sandbox.docker._run", return_value=ExecResult(
                1, "", "Error response from daemon: No such container: bench-agent-x")):
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


class DockerfileTests(unittest.TestCase):
    def test_image_copies_nothing_from_the_build_context(self):
        text = (HERE / "sandbox" / "Dockerfile.bench").read_text(encoding="utf-8")
        self.assertNotIn("COPY", text)
        self.assertNotIn("ADD ", text)

    def test_probe_is_not_referenced(self):
        text = (HERE / "sandbox" / "Dockerfile.bench").read_text(encoding="utf-8")
        self.assertNotIn("probe.py", text)


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
        # The error record must carry the same keys as a normal record.
        self.assertEqual(rec["wall_clock_s"], 0.0)
        self.assertEqual(rec["agent"]["usage"], {})

    def test_unavailable_image_is_a_sandbox_error(self):
        class NoImage(FakeSandbox):
            def image_digest(self):
                return None

        with mock.patch.object(rt, "DockerSandbox", NoImage):
            rec = rt.run_one(self.spec, "mock", "", 1, sandbox_mode="docker")
        self.assertEqual(rec["sandbox_error"],
                         "image unavailable: bountygrid-bench:py313")
        self.assertFalse(rec["overall_success"])

    def test_malformed_hidden_cases_are_recorded(self):
        spec = dict(self.spec)
        spec["hidden_cases"] = [{"module": "calc.ops", "func": "add"}]
        rec = rt.run_one(spec, "mock", "", 1, sandbox_mode="docker")
        self.assertTrue(rec["sandbox_error"].startswith("invalid hidden_cases"))
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
            rec = rt.run_one(self.spec, "external", f'"{sys.executable}" -c "pass"',
                             1, sandbox_mode="docker")
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
        self.assertEqual(rec["agent_trust"], "HOST_UNTRUSTED")
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


class CompletionStatusTests(unittest.TestCase):
    def test_stopped_normally(self):
        self.assertEqual(agent.classify_completion("stop", "done"), "STOPPED_NORMALLY")

    def test_length_is_truncated(self):
        self.assertEqual(agent.classify_completion("length", "partial"), "TOKEN_TRUNCATED")

    def test_empty_final_is_distinct(self):
        self.assertEqual(agent.classify_completion("stop", "   "), "EMPTY_FINAL")

    def test_missing_finish_reason_with_content_is_normal(self):
        self.assertEqual(agent.classify_completion("", "done"), "STOPPED_NORMALLY")


class BaselineGateTests(unittest.TestCase):
    def test_expected_fail_requires_a_failing_baseline(self):
        self.assertFalse(rt.baseline_valid({"expected_baseline": "fail"}, True))
        self.assertTrue(rt.baseline_valid({"expected_baseline": "fail"}, False))

    def test_expected_pass_requires_a_passing_baseline(self):
        self.assertTrue(rt.baseline_valid({"expected_baseline": "pass"}, True))
        self.assertFalse(rt.baseline_valid({"expected_baseline": "pass"}, False))

    def test_unknown_expectation_is_permissive(self):
        self.assertTrue(rt.baseline_valid({}, True))


class CompletionAndTrustTests(unittest.TestCase):
    def setUp(self):
        self.spec = next(s for s in rt.load_tasks({"t01_calc_fix"}))
        self.patch = mock.patch.multiple(
            rt, DockerSandbox=FakeSandbox, docker_available=lambda: True)
        self.patch.start()

    def tearDown(self):
        self.patch.stop()

    def test_step_cap_status_makes_run_incomplete(self):
        with tempfile.TemporaryDirectory() as d:
            script = Path(d) / "fake_agent.py"
            script.write_text(
                "import json, pathlib\n"
                "usage = {'completed': False, 'completion_status': 'STEP_CAP', "
                "'step_cap_hit': True}\n"
                "pathlib.Path('agent_usage.json').write_text(json.dumps(usage))\n",
                encoding="utf-8",
            )
            rec = rt.run_one(self.spec, "external",
                             f'"{sys.executable}" "{script}"', 1,
                             sandbox_mode="none")
        self.assertEqual(rec["completion_status"], "STEP_CAP")
        self.assertFalse(rec["agent_completed"])
        self.assertTrue(rec["step_cap_hit"])

    def test_adapter_backend_with_matching_container_is_trusted(self):
        with tempfile.TemporaryDirectory() as d:
            script = Path(d) / "fake_agent.py"
            script.write_text(
                "import json, os, pathlib\n"
                "usage = {'completed': True, 'completion_status': 'STOPPED_NORMALLY',\n"
                "         'sandbox_container': os.environ.get('BENCH_SANDBOX_CONTAINER', '')}\n"
                "pathlib.Path('agent_usage.json').write_text(json.dumps(usage))\n",
                encoding="utf-8",
            )
            rec = rt.run_one(self.spec, "external",
                             f'"{sys.executable}" "{script}"', 1,
                             sandbox_mode="docker", agent_backend="adapter")
        self.assertEqual(rec["agent_trust"], "TRUSTED_ADAPTER")
        self.assertFalse(rec["agent_unsandboxed"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
