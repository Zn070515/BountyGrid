"""Thread-local write-authority for formal state transitions (M1-Core-Close, Batch A).

A RulesetVersion may only become FROZEN/current inside ``freeze_ruleset_version`` and a
StageResult may only become CONFIRMED inside ``confirm_stage_result``/``unlock_stage_result``.
These are authority transitions that carry Admin, Activity-lock, bound-compiler, authority-hash
and audit machinery, so an untrusted ORM ``.create()``/``.update()`` must be refused unless the
service that owns the transition is actively wrapping the write.

The guard is thread-local so concurrent service calls in separate threads do not leak authority
into one another; prior scope is restored on exit so a nested grant never persists past its
context. The same pattern already covers :class:`ManualDecision` (singer_contest.models) — this
module generalises it so RulesetVersion and StageResult share one mechanism rather than a public
``_allow_freeze``/``_bypass_confirmed`` kwarg that any caller could pass.
"""

import inspect
import threading
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any, cast

from django.core.exceptions import ValidationError

# Write-authority scope identifiers.
RULESET_FREEZE = "ruleset.freeze"
STAGE_RESULT_CONFIRM = "stageresult.confirm"
STAGE_RESULT_RESOLVE = "stageresult.resolve"
ACTIVITY_STATE = "activity.state"
CONTEST_ROUND_STATE = "contestround.state"
VOTE_SESSION_STATE = "votesession.state"
SCORE_SUMMARY_RECALCULATE = "scoresummary.recalculate"
TEST_DATA_CLEANUP = "test_data.cleanup"
RETENTION_CLEANUP = "retention.cleanup"
TEST_DATA_SEED = "test_data.seed"
ACCOUNT_AUTHORITY = "account.authority"
ENTRY_POINT_CONFIG = "entry_point.config"
ACCESS_GRANT_STATE = "access_grant.state"
EPHEMERAL_SESSION_STATE = "ephemeral_session.state"
TICKET_STATE = "ticket.state"
TICKET_SESSION_STATE = "ticket_access_session.state"
JUDGE_PANEL_STATE = "judge_panel.state"
JUDGE_SESSION_STATE = "judge_session.state"
ROUND_PERFORMANCE_STATE = "round_performance.state"
JUDGE_SCORE_SUBMISSION = "judge_score.submission"
SCORE_FACT_WRITE = "score_fact.write"
VOTE_SCORING_RULE = "vote_scoring_rule.write"
VOTE_BALLOT_WRITE = "voteballot.write"
GROUP_STAGE_STATE = "group_stage.state"
PLANNING_DRAFT_WRITE = "ruleset.planning_draft.write"
SINGER_CONTESTANT_NUMBER = "singer_contestant.number"
LIVE_CHALLENGE_STATE = "singer_contest.live_challenge.state"
PODIUM_TIE_DECISION = "singer_contest.podium_tie_decision"
RESULT_RELEASE_STATE = "public_portal.result_release.state"


@dataclass(frozen=True)
class BulkCreateOptions:
    """Parsed Django ``QuerySet.bulk_create`` conflict options.

    Django's public signature after ``objs`` is:
    ``batch_size, ignore_conflicts, update_conflicts, update_fields, unique_fields``.
    Guarded queryset wrappers that keep ``*args, **kwargs`` must inspect both forms
    before delegating, otherwise a positional ``update_conflicts=True`` can bypass the
    model-specific authority check and reach ORM SQL.
    """

    update_conflicts: bool
    update_fields: tuple[Any, ...]
    unique_fields: tuple[Any, ...]


def _option_tuple(value: Any) -> tuple[Any, ...]:
    if value is None:
        return ()
    if isinstance(value, str):
        return (value,)
    return tuple(value)


def parse_bulk_create_options(
    args: tuple[Any, ...], kwargs: Mapping[str, Any]
) -> BulkCreateOptions:
    """Read conflict-upsert options from positional or keyword bulk_create args.

    This intentionally mirrors Django's current call signature without normalizing
    or mutating the actual call arguments passed onward to ``super().bulk_create``.
    """

    update_conflicts = args[2] if len(args) >= 3 else kwargs.get("update_conflicts", False)
    update_fields = args[3] if len(args) >= 4 else kwargs.get("update_fields")
    unique_fields = args[4] if len(args) >= 5 else kwargs.get("unique_fields")
    return BulkCreateOptions(
        update_conflicts=bool(update_conflicts),
        update_fields=_option_tuple(update_fields),
        unique_fields=_option_tuple(unique_fields),
    )


_scope = threading.local()


def _active_scopes() -> frozenset[str]:
    return frozenset(getattr(_scope, "scopes", ()))


def authority_authorized(scope: str) -> bool:
    """True when *scope* is currently granted by an enclosing :func:`authority_write`."""
    return scope in _active_scopes()


def _set_scope(scopes: frozenset[str]) -> None:
    _scope.scopes = frozenset(scopes)


def _called_from_django_collector_delete() -> bool:
    frame = inspect.currentframe()
    try:
        while frame is not None:
            if (
                frame.f_globals.get("__name__") == "django.db.models.deletion"
                and frame.f_code.co_name == "delete"
                and frame.f_locals.get("self").__class__.__name__ == "Collector"
            ):
                return True
            frame = frame.f_back
    finally:
        del frame
    return False


class AuthorityQuerySetMixin:
    """Block direct private raw deletes while preserving guarded delete flows."""

    def _raw_delete(self, using: str | None = None) -> Any:
        if not _called_from_django_collector_delete():
            raise ValidationError("受 authority 保护的数据不能通过 QuerySet._raw_delete() 删除。")
        return cast(Any, super())._raw_delete(using)

    def delete(self, *args: Any, **kwargs: Any) -> Any:
        return cast(Any, super()).delete(*args, **kwargs)


@contextmanager
def authority_write(scope: str) -> Iterator[None]:
    """Bracket a formal write with *scope*; restores the prior authority on exit.

    ``authority_authorized`` is False outside any ``authority_write`` block, so a bare
    ``RulesetVersion.objects.create(status=\"frozen\", is_current=True)`` or
    ``StageResult.objects.create(status=\"confirmed\")`` raises instead of manufacturing
    a terminal row.
    """
    prior = _active_scopes()
    _set_scope(prior | {scope})
    try:
        yield
    finally:
        _set_scope(prior)
