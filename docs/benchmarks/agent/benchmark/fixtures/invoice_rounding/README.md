# Invoice rounding fixture

Tax is 7.5% (750 basis points). Finance computes tax on the invoice subtotal
and rounds once, half away from zero; it must not round each line and then
sum, because that shifts totals by a cent on multi-line invoices.

`invoice/tax.py` currently rounds per line. Fix the layer, not just the
rounding function. Do not change tests or `invoice/api.py`.
