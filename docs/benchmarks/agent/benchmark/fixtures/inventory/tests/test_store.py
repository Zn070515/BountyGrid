import unittest

from inventory.store import Store


class TestStore(unittest.TestCase):
    def setUp(self):
        self.store = Store()

    def test_set_and_get(self):
        self.store.set_item("apple", 3)
        self.assertEqual(self.store.get_item("apple"), 3)

    def test_missing_item_is_zero(self):
        self.assertEqual(self.store.get_item("pear"), 0)

    def test_negative_quantity_rejected(self):
        with self.assertRaises(ValueError):
            self.store.set_item("apple", -1)

    def test_restock_adds(self):
        self.store.set_item("apple", 3)
        self.store.restock("apple", 4)
        self.assertEqual(self.store.get_item("apple"), 7)

    def test_restock_new_item(self):
        self.store.restock("pear", 2)
        self.assertEqual(self.store.get_item("pear"), 2)

    def test_restock_rejects_negative(self):
        with self.assertRaises(ValueError):
            self.store.restock("apple", -2)

    def test_total(self):
        self.store.set_item("apple", 3)
        self.store.set_item("pear", 2)
        self.assertEqual(self.store.total(), 5)


if __name__ == "__main__":
    unittest.main()
