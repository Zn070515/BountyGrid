import unittest
from datetime import date

from window.range import in_window


class TestWindow(unittest.TestCase):
    def test_start_is_inclusive(self):
        self.assertTrue(in_window(date(2026, 1, 1), date(2026, 1, 1), date(2026, 1, 31)))

    def test_end_is_inclusive(self):
        self.assertTrue(in_window(date(2026, 1, 31), date(2026, 1, 1), date(2026, 1, 31)))

    def test_inside(self):
        self.assertTrue(in_window(date(2026, 1, 15), date(2026, 1, 1), date(2026, 1, 31)))

    def test_before_and_after(self):
        self.assertFalse(in_window(date(2025, 12, 31), date(2026, 1, 1), date(2026, 1, 31)))
        self.assertFalse(in_window(date(2026, 2, 1), date(2026, 1, 1), date(2026, 1, 31)))


if __name__ == "__main__":
    unittest.main()
