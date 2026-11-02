import unittest

from ledger.core import Account


class Transaction:
    def __init__(self, amount):
        self.amount = amount


class TestAccount(unittest.TestCase):
    def test_add_keeps_transactions(self):
        account = Account("cash")
        account.add(Transaction(5))
        self.assertEqual(len(account.transactions), 1)


if __name__ == "__main__":
    unittest.main()
