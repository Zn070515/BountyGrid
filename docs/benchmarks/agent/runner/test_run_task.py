# -*- coding: utf-8 -*-
"""Tests for the oracle control arm in run_task.py.

A patch copied with ``copytree`` keeps the source mtime. When the patch and
the fixture file have the same size and mtime (same second), both git's stat
cache and Python's pyc header treat the file as unchanged. The oracle control
then silently runs the old code.

Run with: python runner/test_run_task.py
"""
import os
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import run_task as rt  # noqa: E402


@unittest.skipUnless(shutil.which("python"), "python is not on PATH")
class OraclePatchTests(unittest.TestCase):
    def setUp(self):
        self.spec = next(s for s in rt.load_tasks({"t01_calc_fix"}))
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name)
        self.work = base / "work"
        shutil.copytree(rt.FIXTURES / self.spec["fixture"], self.work)
        rt.init_repo(self.work)
        self.baseline = [rt.run_cmd(c, self.work, 120) for c in self.spec["acceptance"]]

        # Reproduce the production condition: the patch has the same size and
        # the same second-resolution mtime as the file the baseline ran.
        self.solutions = base / "solutions"
        shutil.copytree(rt.SOLUTIONS / "t01", self.solutions / "t01")
        source_mtime = (self.work / "calc" / "ops.py").stat().st_mtime
        for p in (self.solutions / "t01").rglob("*"):
            if p.is_file():
                os.utime(p, (source_mtime, source_mtime))
        self._previous_solutions = rt.SOLUTIONS
        rt.SOLUTIONS = self.solutions

    def tearDown(self):
        rt.SOLUTIONS = self._previous_solutions
        self.tmp.cleanup()

    def test_baseline_fails_before_the_patch(self):
        self.assertTrue(all(r["exit_code"] != 0 for r in self.baseline))

    def test_oracle_patch_is_visible_to_git(self):
        rt.apply_oracle(self.work, self.spec)
        self.assertIn("calc/ops.py", rt.changed_files(self.work))

    def test_oracle_patch_is_visible_to_python(self):
        rt.apply_oracle(self.work, self.spec)
        after = [rt.run_cmd(c, self.work, 120) for c in self.spec["acceptance"]]
        self.assertEqual([r["exit_code"] for r in after], [0])


if __name__ == "__main__":
    unittest.main(verbosity=2)
