import unittest
from types import SimpleNamespace

from django.core.exceptions import PermissionDenied

from core.policies import (
    ActivityAction,
    allowed_actions,
    ensure_activity_action_allowed,
)


def activity(phase):
    return SimpleNamespace(phase=phase, get_phase_display=lambda: phase)


class TestCorePolicies(unittest.TestCase):
    def test_archived_is_read_only(self):
        self.assertEqual(allowed_actions(activity("archived")), frozenset())

    def test_draft_rejects_scoring(self):
        self.assertNotIn(ActivityAction.SCORE, allowed_actions(activity("draft")))

    def test_results_published_rejects_registration_changes(self):
        actions = allowed_actions(activity("results_published"))
        self.assertNotIn(ActivityAction.SUBMIT_REGISTRATION, actions)
        self.assertNotIn(ActivityAction.REVIEW_REGISTRATION, actions)

    def test_registration_open_allows_submission(self):
        self.assertIn(
            ActivityAction.SUBMIT_REGISTRATION,
            allowed_actions(activity("registration_open")),
        )

    def test_unknown_phase_allows_nothing(self):
        self.assertEqual(allowed_actions(activity("bogus")), frozenset())

    def test_ensure_raises_for_disallowed_action(self):
        with self.assertRaises(PermissionDenied):
            ensure_activity_action_allowed(activity("archived"), ActivityAction.SCORE)


if __name__ == "__main__":
    unittest.main()
