from typing import Any

from django.core.exceptions import PermissionDenied


def ensure_activity_unlocked(activity: Any) -> None:
    if activity and activity.is_locked:
        raise PermissionDenied("Activity results are locked.")


def ensure_lifecycle_consistent(activity: Any, related_object: Any, *, label: str) -> None:
    """Reject binding a runtime child whose test marker differs from its activity.

    A TEST activity only admits test-marked rows; a FORMAL activity only admits
    formal-marked rows. Attaching the wrong marker produces mixed-test/formal data
    that the cleanup/lifecycle guards treat as corruption.
    """
    marker = getattr(related_object, "is_test_data", None)
    if marker is None:
        marker = getattr(related_object, "is_test", None)
    if marker is None:
        return
    if marker != activity.is_test_mode:
        raise PermissionDenied(f"{label} 与当前活动的数据生命周期不匹配。")


def ensure_round_unlocked(contest_round: Any) -> None:
    ensure_activity_unlocked(contest_round.activity)
    if contest_round.is_locked:
        raise PermissionDenied("Contest round results are locked.")


def ensure_same_activity(expected_activity: Any, related_object: Any, *, label: str) -> None:
    if not related_object or getattr(related_object, "activity_id", None) != expected_activity.pk:
        raise PermissionDenied(f"{label} must belong to the selected activity.")


def ensure_participant_can_edit(registration: Any) -> None:
    """Participants may freely edit their own submission only while registration is open.

    From ``REGISTRATION_CLOSED`` onward the participant's own registration data is frozen.
    The one remaining participant write is a staff-designated material supplement, which
    has its own authority (``files.services.submit_participant_material_for_check``) and
    never runs through this guard.
    """
    ensure_activity_unlocked(registration.activity)
    if registration.activity.phase != "registration_open":
        raise PermissionDenied("当前活动阶段不允许修改报名。")
