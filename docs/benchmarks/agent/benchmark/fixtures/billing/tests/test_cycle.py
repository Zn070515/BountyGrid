import unittest
from datetime import date

from billing.cycle import next_cycle_end


class TestCycle(unittest.TestCase):
    def test_same_day_next_month(self):
        self.assertEqual(next_cycle_end(date(2026, 1, 15)), date(2026, 2, 15))

    def test_clamps_february(self):
        self.assertEqual(next_cycle_end(date(2026, 1, 31)), date(2026, 2, 28))

    def test_clamps_thirty_day_month(self):
        self.assertEqual(next_cycle_end(date(2026, 3, 31)), date(2026, 4, 30))

    def test_december_rolls_to_next_year(self):
        self.assertEqual(next_cycle_end(date(2026, 12, 15)), date(2027, 1, 15))

    def test_leap_year(self):
        self.assertEqual(next_cycle_end(date(2028, 1, 31)), date(2028, 2, 29))


if __name__ == "__main__":
    unittest.main()
