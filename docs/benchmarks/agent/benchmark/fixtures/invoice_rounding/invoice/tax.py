def invoice_total(lines, tax_bps=750):
    total = 0
    for line in lines:
        subtotal = line.subtotal_cents()
        total += subtotal + round(subtotal * tax_bps / 10000)
    return total
