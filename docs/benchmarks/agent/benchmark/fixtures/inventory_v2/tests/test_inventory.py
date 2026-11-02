import unittest

from inventory.report import summary
from inventory.store import Store


class TestInventory(unittest.TestCase):
    def test_add_and_value(self):
        store = Store()
        store.add("a", 2, 100)
        self.assertEqual(store.value_cents(), 200)

    def test_remove_to_zero(self):
        store = Store()
        store.add("a", 2, 100)
        store.remove("a", 2)
        self.assertEqual(store.quantity("a"), 0)

    def test_remove_more_than_present_is_rejected(self):
        store = Store()
        store.add("a", 2, 100)
        with self.assertRaises(ValueError):
            store.remove("a", 3)

    def test_summary_value_uses_quantity(self):
        store = Store()
        store.add("a", 2, 100)
        self.assertEqual(summary(store)["value_cents"], 200)


if __name__ == "__main__":
    unittest.main()
