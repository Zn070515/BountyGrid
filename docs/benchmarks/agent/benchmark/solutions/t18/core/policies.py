from __future__ import annotations

from enum import StrEnum
from typing import Any

from django.core.exceptions import PermissionDenied


class ActivityAction(StrEnum):
    SUBMIT_REGISTRATION = "submit_registration"
    REVIEW_REGISTRATION = "review_registration"
    REVIEW_GROUP_MATERIAL = "review_group_material"
    UPLOAD_MATERIAL = "upload_material"
    SCORE = "score"
    CAST_VOTE = "cast_vote"
    MANAGE_VOTE = "manage_vote"
    MANAGE_TICKETS = "manage_tickets"
    CHECK_IN = "check_in"
    MANAGE_AWARD = "manage_award"
    PUBLISH_RESULT = "publish_result"
    ARCHIVE = "archive"


# phase -> actions allowed in that phase. The set is intentionally a single
# source of truth so views never need to sprinkle `if activity.phase == ...`.
#
# Bugs this closes:
#   * DRAFT no longer allows scoring.
#   * RESULTS_PUBLISHED no longer allows changing registration/review state.
#   * ARCHIVED is read-only (no upload/material/review/score/submit).
_PHASE_ACTIONS: dict[str, frozenset[ActivityAction]] = {
    "draft": frozenset(
        {
            ActivityAction.UPLOAD_MATERIAL,
            ActivityAction.REVIEW_REGISTRATION,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_AWARD,
        }
    ),
    "testing": frozenset(
        {
            ActivityAction.UPLOAD_MATERIAL,
            ActivityAction.REVIEW_REGISTRATION,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_TICKETS,
            ActivityAction.CHECK_IN,
            ActivityAction.MANAGE_AWARD,
        }
    ),
    "registration_open": frozenset(
        {
            ActivityAction.SUBMIT_REGISTRATION,
            ActivityAction.REVIEW_REGISTRATION,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.UPLOAD_MATERIAL,
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_TICKETS,
        }
    ),
    "registration_closed": frozenset(
        {
            ActivityAction.REVIEW_REGISTRATION,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.UPLOAD_MATERIAL,
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_TICKETS,
            ActivityAction.CHECK_IN,
        }
    ),
    "reviewing": frozenset(
        {
            ActivityAction.REVIEW_REGISTRATION,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.UPLOAD_MATERIAL,
        }
    ),
    "rehearsal": frozenset(
        {
            ActivityAction.UPLOAD_MATERIAL,
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_TICKETS,
            ActivityAction.CHECK_IN,
        }
    ),
    "live": frozenset(
        {
            ActivityAction.REVIEW_GROUP_MATERIAL,
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_TICKETS,
            ActivityAction.CHECK_IN,
            ActivityAction.MANAGE_AWARD,
            ActivityAction.PUBLISH_RESULT,
        }
    ),
    "results_pending": frozenset(
        {
            ActivityAction.SCORE,
            ActivityAction.CAST_VOTE,
            ActivityAction.MANAGE_VOTE,
            ActivityAction.MANAGE_AWARD,
            ActivityAction.ARCHIVE,
            ActivityAction.PUBLISH_RESULT,
        }
    ),
    "results_published": frozenset(
        {
            ActivityAction.MANAGE_AWARD,
            ActivityAction.ARCHIVE,
            ActivityAction.PUBLISH_RESULT,
        }
    ),
    "archived": frozenset(),
}


def allowed_actions(activity: Any) -> frozenset[ActivityAction]:
    return _PHASE_ACTIONS.get(activity.phase, frozenset())


def ensure_activity_action_allowed(activity: Any, action: ActivityAction) -> None:
    if action not in allowed_actions(activity):
        raise PermissionDenied(
            f"Activity phase '{activity.get_phase_display()}' does not allow this action."
        )
