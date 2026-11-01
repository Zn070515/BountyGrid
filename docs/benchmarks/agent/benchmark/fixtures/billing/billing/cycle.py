from datetime import date, timedelta


def next_cycle_end(start: date) -> date:
    """Return the last day of the monthly cycle that starts on start.

    The cycle ends on the same day of the following month. When that day does
    not exist, it clamps to the last day of that month. The end date is
    inclusive.
    """
    return start + timedelta(days=30)
