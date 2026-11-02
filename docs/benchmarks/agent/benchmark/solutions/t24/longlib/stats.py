import math


def _check(values) -> None:
    if not values:
        raise ValueError("values must not be empty")


def mean(values):
    """Arithmetic mean of a non-empty sequence. Raise ValueError when empty."""
    _check(values)
    return sum(values) / len(values)


def median(values):
    """Median of a non-empty sequence. Raise ValueError when empty."""
    _check(values)
    ordered = sorted(values)
    n = len(ordered)
    middle = n // 2
    if n % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def percentile(values, p):
    """Linear-interpolation percentile (p in [0, 100]) of a non-empty sequence.

    Raise ValueError for an empty sequence or p outside [0, 100].
    """
    _check(values)
    if not 0 <= p <= 100:
        raise ValueError("p must be in [0, 100]")
    ordered = sorted(values)
    n = len(ordered)
    if n == 1:
        return ordered[0]
    position = (n - 1) * p / 100
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[int(position)]
    return ordered[lower] * (upper - position) + ordered[upper] * (position - lower)
