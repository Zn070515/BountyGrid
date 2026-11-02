from datetime import date


def in_window(day: date, start: date, end: date) -> bool:
    """Return True when day is inside the inclusive range [start, end]."""
    return start <= day <= end
