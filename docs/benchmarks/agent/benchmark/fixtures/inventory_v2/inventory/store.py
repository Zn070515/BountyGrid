from .models import Item


class Store:
    def __init__(self):
        self._items = {}

    def add(self, sku, quantity, price_cents):
        if quantity <= 0:
            raise ValueError("quantity must be positive")
        item = self._items.get(sku)
        if item is None:
            self._items[sku] = Item(sku, quantity, price_cents)
        else:
            item.quantity += quantity
            item.price_cents = price_cents

    def remove(self, sku, quantity):
        item = self._items.get(sku)
        if item is None:
            raise KeyError(sku)
        item.quantity -= quantity

    def quantity(self, sku):
        return self._items[sku].quantity

    def value_cents(self):
        return sum(item.quantity * item.price_cents for item in self._items.values())
