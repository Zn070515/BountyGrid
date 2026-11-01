import unittest

from reporting.format import format_row, summarize


class TestFormat(unittest.TestCase):
    def test_format_row(self):
        self.assertEqual(format_row("alpha", 3.5), "alpha: 3.50")

    def test_summarize(self):
        rows = [("alpha", 3.5), ("beta", 1.25)]
        self.assertEqual(summarize(rows), "total: 4.75")


if __name__ == "__main__":
    unittest.main()
