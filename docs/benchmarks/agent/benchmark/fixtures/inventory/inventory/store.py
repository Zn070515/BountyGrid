class Store:
    def __init__(self):
        self._items = {}

    def set_item(self, name, quantity):
        if quantity < 0:
            raise ValueError("quantity must be non-negative")
        self._items[name] = quantity

    def get_item(self, name):
        return self._items.get(name, 0)

    def restock(self, name, amount):
        raise NotImplementedError("restock is not implemented")

    def total(self):
        return sum(self._items.values())
