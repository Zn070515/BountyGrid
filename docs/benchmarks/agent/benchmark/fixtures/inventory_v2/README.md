# Inventory v2 fixture

The inventory package has three layers: `store.py` (mutations and value),
`report.py` (read-only summaries), and `api.py` (the public facade used by
other services). The tests exercise the store and the report.

Two behaviors are wrong:

- removing more units than are present silently drives the quantity negative;
- the report sums unit prices and ignores quantities.

Fix both without changing tests or `api.py`. The hidden probe calls the
public facade, so the store and report must agree.
