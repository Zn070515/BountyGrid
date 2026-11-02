import re


def slugify(value: str) -> str:
    """Lowercase, replace runs of non-alphanumerics with '-', strip '-'."""
    text = re.sub(r"[^a-z0-9]+", "-", value.strip().lower())
    return text.strip("-")


def truncate(value: str, limit: int) -> str:
    """Cut value to limit characters, ending with an ellipsis when cut.

    limit must be at least 1. A value that already fits is returned unchanged.
    """
    if limit < 1:
        raise ValueError("limit must be at least 1")
    if len(value) <= limit:
        return value
    return value[: limit - 1] + "…"
