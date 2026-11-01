import unittest

from textutil.core import normalize, truncate


class TestNormalize(unittest.TestCase):
    def test_lowercases(self):
        self.assertEqual(normalize("Hello World"), "hello world")

    def test_collapses_whitespace(self):
        self.assertEqual(normalize("  a   b\tc  "), "a b c")

    def test_unicode(self):
        self.assertEqual(normalize("ÄPFEL  Birnen"), "äpfel birnen")


class TestTruncate(unittest.TestCase):
    def test_short(self):
        self.assertEqual(truncate("abc", 5), "abc")

    def test_exact(self):
        self.assertEqual(truncate("abcde", 5), "abcde")

    def test_cut(self):
        self.assertEqual(truncate("abcdef", 3), "abc...")


if __name__ == "__main__":
    unittest.main()
