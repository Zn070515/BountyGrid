import tempfile
import unittest
from pathlib import Path

from longlib.store import JsonStore


class TestStore(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.path = str(Path(self.tmp.name) / "data.json")

    def tearDown(self):
        self.tmp.cleanup()

    def test_set_get(self):
        store = JsonStore(self.path)
        store.set("a", {"x": 1})
        self.assertEqual(store.get("a"), {"x": 1})
        self.assertIsNone(store.get("b"))
        self.assertEqual(store.get("b", 5), 5)

    def test_keys_are_sorted(self):
        store = JsonStore(self.path)
        store.set("b", 1)
        store.set("a", 2)
        self.assertEqual(store.keys(), ["a", "b"])

    def test_delete(self):
        store = JsonStore(self.path)
        store.set("a", 1)
        self.assertTrue(store.delete("a"))
        self.assertFalse(store.delete("a"))

    def test_save_load_roundtrip(self):
        store = JsonStore(self.path)
        store.set("a", [1, 2])
        store.save()
        loaded = JsonStore(self.path).load()
        self.assertEqual(loaded.get("a"), [1, 2])

    def test_load_missing_file_is_empty(self):
        self.assertEqual(JsonStore(self.path).load().keys(), [])

    def test_invalid_key(self):
        with self.assertRaises(ValueError):
            JsonStore(self.path).set("", 1)

    def test_non_serializable_value(self):
        with self.assertRaises(TypeError):
            JsonStore(self.path).set("a", object())


if __name__ == "__main__":
    unittest.main()
