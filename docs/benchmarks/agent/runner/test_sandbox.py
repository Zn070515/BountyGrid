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


if __name__ == "__main__":
    unittest.main(verbosity=2)
