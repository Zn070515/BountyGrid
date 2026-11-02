import threading
import unittest

from config.api import Loader


class TestLoader(unittest.TestCase):
    def test_instances_are_independent(self):
        first = Loader({"x": 1})
        second = Loader({"x": 2})
        self.assertEqual(first.get("x"), 1)
        self.assertEqual(second.get("x"), 2)

    def test_missing_key_raises(self):
        with self.assertRaises(KeyError):
            Loader({}).get("x")

    def test_concurrent_instances(self):
        results = {}

        def run(name, value):
            results[name] = Loader({"x": value}).get("x")

        threads = [threading.Thread(target=run, args=(f"t{i}", i)) for i in range(8)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(results, {f"t{i}": i for i in range(8)})


if __name__ == "__main__":
    unittest.main()
