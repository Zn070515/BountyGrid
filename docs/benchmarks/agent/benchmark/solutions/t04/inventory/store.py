from .summary import total_of


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
        if amount < 0:
            raise ValueError("amount must be non-negative")
        self._items[name] = self.get_item(name) + amount

    def total(self):
        return total_of(self._items)
