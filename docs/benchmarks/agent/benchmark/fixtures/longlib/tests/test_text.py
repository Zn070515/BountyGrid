import unittest

from longlib.text import slugify, truncate


class TestSlugify(unittest.TestCase):
    def test_basic(self):
        self.assertEqual(slugify("Hello World"), "hello-world")

    def test_collapses_runs_and_strips(self):
        self.assertEqual(slugify("  A  B  "), "a-b")

    def test_symbols(self):
        self.assertEqual(slugify("C++ & Go"), "c-go")

    def test_empty(self):
        self.assertEqual(slugify(""), "")


class TestTruncate(unittest.TestCase):
    def test_fitting_value_is_unchanged(self):
        self.assertEqual(truncate("hello", 5), "hello")

    def test_cut_adds_ellipsis(self):
        self.assertEqual(truncate("hello world", 6), "hello…")

    def test_limit_one(self):
        self.assertEqual(truncate("abc", 1), "…")

    def test_invalid_limit(self):
        with self.assertRaises(ValueError):
            truncate("abc", 0)


if __name__ == "__main__":
    unittest.main()
