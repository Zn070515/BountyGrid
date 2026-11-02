import unittest
from types import SimpleNamespace

from django.core.exceptions import PermissionDenied

from common.business_rules import (
    ensure_activity_unlocked,
    ensure_lifecycle_consistent,
    ensure_participant_can_edit,
)


def activity(**overrides):
    values = {
        "is_locked": False,
        "phase": "registration_open",
        "is_test_mode": True,
    }
    values.update(overrides)
    return SimpleNamespace(**values)


class TestBusinessRules(unittest.TestCase):
    def test_locked_activity_is_rejected(self):
        with self.assertRaises(PermissionDenied):
            ensure_activity_unlocked(activity(is_locked=True))

    def test_participant_can_edit_while_registration_open(self):
        ensure_participant_can_edit(SimpleNamespace(activity=activity()))

    def test_participant_cannot_edit_after_registration_closed(self):
        registration = SimpleNamespace(activity=activity(phase="registration_closed"))
        with self.assertRaises(PermissionDenied):
            ensure_participant_can_edit(registration)

    def test_lifecycle_marker_must_match_activity(self):
        test_activity = activity(is_test_mode=True)
        ensure_lifecycle_consistent(
            test_activity, SimpleNamespace(is_test_data=True), label="row"
        )
        with self.assertRaises(PermissionDenied):
            ensure_lifecycle_consistent(
                test_activity, SimpleNamespace(is_test_data=False), label="row"
            )


if __name__ == "__main__":
    unittest.main()
