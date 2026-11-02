from .loader import read


class Loader:
    def __init__(self, source):
        self.source = source

    def get(self, key):
        return read(self.source, key)
