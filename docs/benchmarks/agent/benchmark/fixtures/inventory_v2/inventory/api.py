from .report import summary
from .store import Store


def add_item(store, sku, quantity, price_cents):
    store.add(sku, quantity, price_cents)


def remove_item(store, sku, quantity):
    store.remove(sku, quantity)


def stock_value(store):
    return store.value_cents()


def after_remove_value(quantity, price_cents, removed):
    store = Store()
    store.add("x", quantity, price_cents)
    store.remove("x", removed)
    return store.value_cents()


def remove_rejects(quantity, removed):
    store = Store()
    store.add("x", quantity, 100)
    try:
        store.remove("x", removed)
    except ValueError:
        return True
    return False


def summary_value(quantity, price_cents):
    store = Store()
    store.add("x", quantity, price_cents)
    return summary(store)["value_cents"]
