class InvalidTransition(ValueError):
    pass


ALLOWED = {
    "open": {"in_progress", "closed"},
    "in_progress": {"closed"},
    "closed": {"open"},
}


def transition(current: str, target: str) -> str:
    """Return target when the transition is allowed, else raise InvalidTransition.

    An unknown current or target state raises InvalidTransition.
    """
    if current not in ALLOWED or target not in ALLOWED:
        raise InvalidTransition(f"unknown state: {current!r} -> {target!r}")
    if target not in ALLOWED[current]:
        raise InvalidTransition(f"invalid transition: {current!r} -> {target!r}")
    return target
