class JsonStore:
    """A JSON-backed key/value store with atomic saves.

    Keys must be non-empty strings; values must be JSON-serializable.
    """

    def __init__(self, path):
        raise NotImplementedError("JsonStore.__init__ is not implemented")

    def set(self, key, value):
        raise NotImplementedError("JsonStore.set is not implemented")

    def get(self, key, default=None):
        raise NotImplementedError("JsonStore.get is not implemented")

    def delete(self, key):
        raise NotImplementedError("JsonStore.delete is not implemented")

    def keys(self):
        raise NotImplementedError("JsonStore.keys is not implemented")

    def save(self):
        raise NotImplementedError("JsonStore.save is not implemented")

    def load(self):
        raise NotImplementedError("JsonStore.load is not implemented")
