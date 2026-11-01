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
    raise NotImplementedError("transition is not implemented yet")
