import unittest

from calc.ops import add, multiply


class TestOps(unittest.TestCase):
    def test_add_positive(self):
        self.assertEqual(add(2, 3), 5)

    def test_add_negative(self):
        self.assertEqual(add(-4, 1), -3)

    def test_add_zero(self):
        self.assertEqual(add(0, 0), 0)

    def test_multiply(self):
        self.assertEqual(multiply(3, 4), 12)


if __name__ == "__main__":
    unittest.main()
