import unittest

from ledger.totals import total_amount


class Transaction:
    def __init__(self, amount):
        self.amount = amount


class TestTotals(unittest.TestCase):
    def test_single_transaction(self):
        self.assertEqual(total_amount([Transaction(5)]), 5)

    def test_multiple_transactions(self):
        self.assertEqual(total_amount([Transaction(5), Transaction(7), Transaction(-2)]), 10)

    def test_empty(self):
        self.assertEqual(total_amount([]), 0)


if __name__ == "__main__":
    unittest.main()
