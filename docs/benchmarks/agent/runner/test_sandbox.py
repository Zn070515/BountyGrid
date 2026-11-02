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
        self.assertTrue(is_docker_error(
            ExecResult(125, "", "Error response from daemon: No such container")))
        self.assertTrue(is_docker_error(
            ExecResult(126, "", "OCI runtime exec failed: exec failed")))
        self.assertFalse(is_docker_error(
            ExecResult(1, "", "AssertionError: -1 != 5")))

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


if __name__ == "__main__":
    unittest.main(verbosity=2)
