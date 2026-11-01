import unittest

from tickets.state import InvalidTransition, transition


class TestState(unittest.TestCase):
    def test_open_to_in_progress(self):
        self.assertEqual(transition("open", "in_progress"), "in_progress")

    def test_open_to_closed(self):
        self.assertEqual(transition("open", "closed"), "closed")

    def test_in_progress_to_closed(self):
        self.assertEqual(transition("in_progress", "closed"), "closed")

    def test_reopen(self):
        self.assertEqual(transition("closed", "open"), "open")

    def test_invalid_transitions(self):
        with self.assertRaises(InvalidTransition):
            transition("in_progress", "open")
        with self.assertRaises(InvalidTransition):
            transition("closed", "in_progress")

    def test_unknown_states(self):
        with self.assertRaises(InvalidTransition):
            transition("bogus", "open")
        with self.assertRaises(InvalidTransition):
            transition("open", "bogus")


if __name__ == "__main__":
    unittest.main()
