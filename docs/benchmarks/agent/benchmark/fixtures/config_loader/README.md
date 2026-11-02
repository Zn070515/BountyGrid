# Config loader fixture

This package serves configuration values to worker threads. Each worker
creates its own `Loader` with a source mapping and reads keys through
`Loader.get`.

The failing behavior is values leaking between loaders: a global cache keeps
the first value seen for a key, so a second loader receives data from the
first one. Read `CONSTRAINTS.md` before changing the loader; the cache must not
be module-level mutable state.

Run the tests with:

    python -m unittest discover -s tests -v

Do not modify tests or `config/api.py`.
