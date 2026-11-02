# -*- coding: utf-8 -*-
"""End-to-end and isolation tests. Skipped unless Docker and the image exist.

Run from docs/benchmarks/agent:
    python runner/test_sandbox_integration.py
"""
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

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
