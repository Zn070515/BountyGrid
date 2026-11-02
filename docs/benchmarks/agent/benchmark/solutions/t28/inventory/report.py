def summary(store):
    return {
        "skus": len(store._items),
        "value_cents": store.value_cents(),
    }
