import unittest

from invoice.api import total_cents


class TestTax(unittest.TestCase):
    def test_single_line(self):
        self.assertEqual(total_cents([[1, 1000]]), 1075)

    def test_round_tax_once_per_invoice(self):
        self.assertEqual(total_cents([[1, 100]] * 5), 538)

    def test_negative_invoice(self):
        self.assertEqual(total_cents([[-1, 100]] * 5), -538)


if __name__ == "__main__":
    unittest.main()
