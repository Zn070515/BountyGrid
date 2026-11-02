# -*- coding: utf-8 -*-
"""Tests for the benchmark tool interface's command and read policy.

Run with: python runner/test_command_policy.py
"""
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import agent_openai_tools as agent  # noqa: E402


class CheckCommandTests(unittest.TestCase):
    def assertDenied(self, cmd):
        argv, reason = agent.check_command(cmd)
        self.assertIsNone(argv, f"expected {cmd!r} to be denied")
        self.assertTrue(reason, "a denial must carry a reason")

    def assertAllowed(self, cmd):
        argv, reason = agent.check_command(cmd)
        self.assertIsNone(reason, f"expected {cmd!r} to be allowed, got {reason}")
        self.assertIsInstance(argv, list)
        return argv

    # --- write-capable find primitives the old regex missed -----------------

    def test_rejects_find_execdir(self):
        self.assertDenied("find . -execdir touch outside.txt")

    def test_rejects_find_okdir(self):
        self.assertDenied("find . -okdir rm {}")

    def test_rejects_find_fprint0(self):
        self.assertDenied("find . -fprint0 outside.bin")

    def test_rejects_find_exec(self):
        self.assertDenied("find . -exec touch outside.txt")

    def test_rejects_find_delete(self):
        self.assertDenied("find . -delete")

    # --- shell chaining is meaningless without a shell, so refuse it --------

    def test_rejects_shell_chaining(self):
        self.assertDenied("ls && rm -rf /")

    def test_rejects_command_substitution(self):
        self.assertDenied("ls $(rm -rf /)")

    # --- write-capable flags on otherwise allowed commands ------------------

    def test_rejects_git_diff_output(self):
        self.assertDenied("git diff --output=outside.patch")

    def test_rejects_pytest_basetemp(self):
        self.assertDenied("python -m pytest --basetemp=/tmp/elsewhere")

    def test_rejects_pytest_junitxml(self):
        self.assertDenied("python -m pytest --junitxml=outside.xml")

    def test_rejects_go_test_o(self):
        self.assertDenied("go test -o outside.exe")

    def test_rejects_rg_pre(self):
        self.assertDenied("rg --pre cat needle")

    # --- the commands the benchmark actually needs stay allowed -------------

    def test_allows_pytest(self):
        self.assertEqual(self.assertAllowed("python -m pytest tests/"),
                         ["python", "-m", "pytest", "tests/"])

    def test_allows_unittest(self):
        self.assertAllowed("python -m unittest tests.test_store")

    def test_allows_go_test(self):
        self.assertAllowed("go test ./...")

    def test_allows_git_diff(self):
        self.assertAllowed("git diff --stat")

    def test_allows_git_status(self):
        self.assertAllowed("git status --porcelain")

    def test_allows_find_without_write_flags(self):
        self.assertAllowed("find . -name '*.py'")

    # --- read-only tools must stay inside the worktree ----------------------

    def test_rejects_read_outside_workdir(self):
        self.assertDenied("head C:/Windows/win.ini")

    def test_rejects_parent_traversal(self):
        self.assertDenied("grep secret ../../outside.txt")

    def test_rejects_absolute_grep_path(self):
        self.assertDenied("grep secret /etc/passwd")

    def test_allows_grep_in_workdir(self):
        self.assertAllowed("grep -r secret .")

    def test_allows_wc_on_relative_file(self):
        self.assertAllowed("wc -l calc/ops.py")

    # --- everything else ----------------------------------------------------

    def test_rejects_unknown_executable(self):
        self.assertDenied("curl http://example.com")

    def test_rejects_shell_interpreter(self):
        self.assertDenied("bash -c ls")

    def test_rejects_empty(self):
        self.assertDenied("   ")

    def test_rejects_unbalanced_quote(self):
        self.assertDenied("grep 'oops")


class TimeoutTests(unittest.TestCase):
    def test_clamps_above_max(self):
        self.assertEqual(agent.clamp_timeout(100000), agent.MAX_COMMAND_TIMEOUT)

    def test_clamps_below_min(self):
        self.assertEqual(agent.clamp_timeout(-5), agent.MIN_COMMAND_TIMEOUT)

    def test_defaults_when_missing(self):
        self.assertEqual(agent.clamp_timeout(None), 60)

    def test_defaults_when_not_a_number(self):
        self.assertEqual(agent.clamp_timeout("soon"), 60)


class ReadPolicyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)
        (self.work / ".git").mkdir()
        (self.work / ".git" / "config").write_text("[core]\n", encoding="utf-8")
        (self.work / "hello.py").write_text("print('hi')\n", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def test_read_file_denies_git_internals(self):
        out = agent.tool_call(self.work, "read_file", {"path": ".git/config"}, None)
        self.assertTrue(out.startswith("ERROR:"), out)
        self.assertNotIn("[core]", out)

    def test_read_file_still_reads_normal_files(self):
        out = agent.tool_call(self.work, "read_file", {"path": "hello.py"}, None)
        self.assertIn("print('hi')", out)


class ReadOnlyBuiltinTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = Path(self.tmp.name)
        (self.work / "hello.py").write_text("print('hi')\n", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def test_dir_lists_workdir(self):
        out = agent.tool_call(self.work, "run_command", {"command": "dir"}, None)
        self.assertIn("hello.py", out)

    def test_type_reads_file(self):
        out = agent.tool_call(self.work, "run_command", {"command": "type hello.py"}, None)
        self.assertIn("print('hi')", out)

    def test_type_cannot_escape_workdir(self):
        out = agent.tool_call(self.work, "run_command", {"command": "type ../outside"}, None)
        self.assertTrue(out.startswith("ERROR:"), out)

    def test_type_requires_a_path(self):
        out = agent.tool_call(self.work, "run_command", {"command": "type"}, None)
        self.assertTrue(out.startswith("ERROR:"), out)


if __name__ == "__main__":
    unittest.main(verbosity=2)
