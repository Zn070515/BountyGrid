_CACHE = {}


def read(source, key):
    if key not in _CACHE:
        _CACHE[key] = source[key]
    return _CACHE[key]
