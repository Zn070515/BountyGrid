# Money rounding fixture

Money is stored as decimal strings in the ledger and converted to floats only
at the boundary. `money.rounding.round_cents` must round half away from zero
for both positive and negative amounts; this is the rule finance signed off on.

The current implementation uses Python's built-in `round`, which rounds
binary floats and ties-to-even. The tests use the exact expected cents.
Do not change the tests.
