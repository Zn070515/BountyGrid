from .line import Line
from .tax import invoice_total


def total_cents(lines, tax_bps=750):
    return invoice_total([Line(quantity, unit) for quantity, unit in lines], tax_bps)
