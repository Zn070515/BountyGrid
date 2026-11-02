# Loader constraints

The configuration loader is used by several worker threads at the same time.
Each `Loader` instance reads from its own source; values must never cross
between instances.

Hard constraints:

1. Do not keep module-level mutable state in `config/loader.py`. A global
   cache (or clearing one in `__init__`) makes concurrent instances interfere.
2. Do not change the public API in `config/api.py`.
3. Do not add third-party dependencies; the loader is stdlib-only.
4. Keep the existing behavior for missing keys: `Loader({}).get("x")` raises
   `KeyError`.
