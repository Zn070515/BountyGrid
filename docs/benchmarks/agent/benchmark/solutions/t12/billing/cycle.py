import calendar
from datetime import date


def next_cycle_end(start: date) -> date:
    """Return the last day of the monthly cycle that starts on start.

    The cycle ends on the same day of the following month. When that day does
    not exist, it clamps to the last day of that month. The end date is
    inclusive.
    """
    year = start.year + (1 if start.month == 12 else 0)
    month = 1 if start.month == 12 else start.month + 1
    last_day = calendar.monthrange(year, month)[1]
    return date(year, month, min(start.day, last_day))
