from decimal import Decimal, ROUND_HALF_UP


def invoice_total(lines, tax_bps=750):
    subtotal = sum(line.subtotal_cents() for line in lines)
    tax = (Decimal(subtotal) * Decimal(tax_bps) / Decimal(10000)).quantize(
        Decimal("1"), rounding=ROUND_HALF_UP)
    return subtotal + int(tax)
