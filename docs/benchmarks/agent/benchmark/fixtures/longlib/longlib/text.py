def slugify(value: str) -> str:
    """Lowercase, replace runs of non-alphanumerics with '-', strip '-'."""
    raise NotImplementedError("slugify is not implemented")


def truncate(value: str, limit: int) -> str:
    """Cut value to limit characters, ending with an ellipsis when cut.

    limit must be at least 1. A value that already fits is returned unchanged.
    """
    raise NotImplementedError("truncate is not implemented")
