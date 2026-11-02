import unittest

from paging.paging import page


class TestPaging(unittest.TestCase):
    def test_first_page(self):
        self.assertEqual(page([1, 2, 3, 4, 5], 1, 2), [1, 2])

    def test_middle_page(self):
        self.assertEqual(page([1, 2, 3, 4, 5], 2, 2), [3, 4])

    def test_last_page_is_partial(self):
        self.assertEqual(page([1, 2, 3, 4, 5], 3, 2), [5])

    def test_beyond_the_end_is_empty(self):
        self.assertEqual(page([1, 2, 3], 5, 2), [])

    def test_invalid_page_number(self):
        with self.assertRaises(ValueError):
            page([1], 0, 2)

    def test_invalid_size(self):
        with self.assertRaises(ValueError):
            page([1], 1, 0)


if __name__ == "__main__":
    unittest.main()
