import unittest

from money.rounding import round_cents


class TestRounding(unittest.TestCase):
    def test_half_cent_rounds_up(self):
        self.assertEqual(round_cents(2.675), 2.68)

    def test_one_and_a_half_cents(self):
        self.assertEqual(round_cents(1.005), 1.01)

    def test_negative_half_cent(self):
        self.assertEqual(round_cents(-2.675), -2.68)

    def test_below_half(self):
        self.assertEqual(round_cents(0.004), 0.0)

    def test_large_amount(self):
        self.assertEqual(round_cents(1234567.895), 1234567.90)


if __name__ == "__main__":
    unittest.main()
