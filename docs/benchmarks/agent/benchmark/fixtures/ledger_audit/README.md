# Ledger audit fixture

A small accounting package used by several reporting jobs. The package is
intentionally larger than the failing test: most modules are stable and are
not involved in the current failure.

## Known issues

- `format.format_cents` uses round-half-even; finance has not decided whether
  that is acceptable for display.
- `io.parse_amount` does not accept thousands separators.
- `reports.account_summary` does not include the currency, because the account
  model has no currency field yet.
- The audit log writer is still a prototype and has no retention policy.
- The rate table is refreshed manually; it is not part of the test suite.

## Working on this fixture

Run the suite from the repository root:

    python -m unittest discover -s tests -v

Do not modify the tests. Keep the change local to the arithmetic helper that
the failing test points at.
