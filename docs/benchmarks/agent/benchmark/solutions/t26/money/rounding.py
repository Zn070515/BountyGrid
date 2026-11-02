from decimal import Decimal, ROUND_HALF_UP


def round_cents(amount):
    """Round a money amount to two decimal places, half away from zero."""
    return float(Decimal(str(amount)).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP))
