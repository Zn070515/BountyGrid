import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path

from longlib.cli import main


class TestCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.path = str(Path(self.tmp.name) / "data.json")

    def tearDown(self):
        self.tmp.cleanup()

    def run_cli(self, *args):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            code = main(list(args))
        return code, output.getvalue()

    def test_add_and_get_json(self):
        code, output = self.run_cli("add", self.path, "a", '{"x": 1}')
        self.assertEqual(code, 0)
        self.assertEqual(output.strip(), "ok")
        code, output = self.run_cli("get", self.path, "a")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output), {"x": 1})

    def test_add_plain_string(self):
        self.run_cli("add", self.path, "a", "hello")
        code, output = self.run_cli("get", self.path, "a")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output), "hello")

    def test_get_missing(self):
        code, output = self.run_cli("get", self.path, "nope")
        self.assertEqual(code, 1)
        self.assertIn("missing", output)

    def test_list_is_sorted(self):
        self.run_cli("add", self.path, "b", "1")
        self.run_cli("add", self.path, "a", "2")
        code, output = self.run_cli("list", self.path)
        self.assertEqual(code, 0)
        self.assertEqual(output.split(), ["a", "b"])


if __name__ == "__main__":
    unittest.main()
