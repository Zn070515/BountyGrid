# -*- coding: utf-8 -*-
"""Tests for bounded directory listing and paged file reads.

Run with: python runner/test_listing.py
"""
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import agent_openai_tools as agent  # noqa: E402


class ListFilesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_returns_at_most_the_limit(self):
        for i in range(350):
            (self.work / f"f{i:04d}.py").write_text("", encoding="utf-8")
        out = agent.list_files(self.work, ".", limit=300)
        self.assertEqual(len(out.splitlines()), 300)

    def test_limit_is_configurable(self):
        for i in range(10):
            (self.work / f"f{i}.py").write_text("", encoding="utf-8")
        self.assertEqual(len(agent.list_files(self.work, ".", limit=3).splitlines()), 3)

    def test_lexicographic_depth_first_order(self):
        (self.work / "b.py").write_text("", encoding="utf-8")
        (self.work / "a.py").write_text("", encoding="utf-8")
        (self.work / "c").mkdir()
        (self.work / "c" / "z.py").write_text("", encoding="utf-8")
        self.assertEqual(agent.list_files(self.work, ".").splitlines(),
                         ["a.py", "b.py", "c", "c/z.py"])

    def test_skips_cache_directories(self):
        (self.work / "keep.py").write_text("", encoding="utf-8")
        for name in (".git", "__pycache__", ".pytest_cache"):
            (self.work / name).mkdir()
            (self.work / name / "junk").write_text("", encoding="utf-8")
        self.assertEqual(agent.list_files(self.work, ".").splitlines(), ["keep.py"])

    def test_does_not_materialize_the_whole_tree(self):
        (self.work / "a.py").write_text("", encoding="utf-8")
        with mock.patch.object(Path, "rglob", side_effect=AssertionError("full walk")):
            out = agent.list_files(self.work, ".")
        self.assertIn("a.py", out)

    def test_stops_after_the_limit(self):
        for d in range(20):
            sub = self.work / f"d{d:02d}"
            sub.mkdir()
            for f in range(10):
                (sub / f"f{f}.py").write_text("", encoding="utf-8")
        calls = []
        real_scandir = os.scandir

        def counting_scandir(path):
            calls.append(str(path))
            return real_scandir(path)

        with mock.patch.object(agent.os, "scandir", counting_scandir):
            out = agent.list_files(self.work, ".", limit=5)
        self.assertLessEqual(len(out.splitlines()), 5)
        self.assertGreaterEqual(len(calls), 1)
        self.assertLessEqual(len(calls), 3)


class ReadTextTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_reads_only_the_requested_window(self):
        body = "\n".join(f"line{i}" for i in range(1, 101)) + "\n"
        (self.work / "big.txt").write_text(body, encoding="utf-8")
        out = agent.read_text(self.work, "big.txt", start=10, limit=3)
        self.assertIn("lines 10-12 of 100", out)
        self.assertEqual(out.splitlines()[1:], ["line10", "line11", "line12"])

    def test_does_not_load_the_whole_file(self):
        (self.work / "big.txt").write_text("a\nb\nc\n", encoding="utf-8")
        with mock.patch.object(Path, "read_text", side_effect=AssertionError("whole file")):
            out = agent.read_text(self.work, "big.txt", start=2, limit=1)
        self.assertIn("lines 2-2 of 3", out)

    def test_start_past_end(self):
        (self.work / "small.txt").write_text("a\nb\n", encoding="utf-8")
        out = agent.read_text(self.work, "small.txt", start=10, limit=2)
        self.assertIn("lines 10-9 of 2", out)


if __name__ == "__main__":
    unittest.main(verbosity=2)
