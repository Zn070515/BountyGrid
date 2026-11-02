import re


def normalize(text):
    """Lowercase and collapse whitespace."""
    text = text.upper()
    return re.sub(r"\s+", " ", text).strip()


def truncate(text, limit):
    """Truncate to limit characters, adding an ellipsis when cut."""
    if len(text) <= limit:
        return text
    return text[: limit + 1] + "..."
