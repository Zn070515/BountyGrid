import unittest

from longlib.stats import mean, median, percentile


class TestStats(unittest.TestCase):
    def test_mean(self):
        self.assertEqual(mean([1, 2, 3]), 2.0)

    def test_mean_empty(self):
        with self.assertRaises(ValueError):
            mean([])

    def test_median_odd(self):
        self.assertEqual(median([3, 1, 2]), 2)

    def test_median_even(self):
        self.assertEqual(median([1, 2, 3, 4]), 2.5)

    def test_median_empty(self):
        with self.assertRaises(ValueError):
            median([])

    def test_percentile_edges(self):
        self.assertEqual(percentile([1, 2, 3, 4], 0), 1)
        self.assertEqual(percentile([1, 2, 3, 4], 100), 4)

    def test_percentile_interpolates(self):
        self.assertEqual(percentile([1, 2, 3, 4], 50), 2.5)

    def test_percentile_invalid(self):
        with self.assertRaises(ValueError):
            percentile([1, 2], -1)
        with self.assertRaises(ValueError):
            percentile([1, 2], 101)
        with self.assertRaises(ValueError):
            percentile([], 50)


if __name__ == "__main__":
    unittest.main()
