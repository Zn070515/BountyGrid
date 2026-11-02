def summary(store):
    return {
        "skus": len(store._items),
        "value_cents": sum(item.price_cents for item in store._items.values()),
    }
