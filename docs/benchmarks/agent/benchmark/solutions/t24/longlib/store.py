import json
import os


class JsonStore:
    """A JSON-backed key/value store with atomic saves.

    Keys must be non-empty strings; values must be JSON-serializable.
    """

    def __init__(self, path):
        self.path = path
        self._data = {}

    def set(self, key, value):
        if not isinstance(key, str) or not key:
            raise ValueError("key must be a non-empty string")
        json.dumps(value)
        self._data[key] = value

    def get(self, key, default=None):
        return self._data.get(key, default)

    def delete(self, key):
        if key in self._data:
            del self._data[key]
            return True
        return False

    def keys(self):
        return sorted(self._data)

    def save(self):
        temporary = f"{self.path}.tmp"
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(self._data, handle, sort_keys=True)
        os.replace(temporary, self.path)

    def load(self):
        if not os.path.exists(self.path):
            self._data = {}
            return self
        with open(self.path, encoding="utf-8") as handle:
            self._data = json.load(handle)
        return self
