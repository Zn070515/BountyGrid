def page(items, page_number: int, size: int):
    """Return one page of items.

    Pages are 1-based. A page beyond the end returns an empty list. A
    page_number below 1 or a size below 1 raises ValueError.
    """
    if page_number < 1:
        raise ValueError("page_number must be at least 1")
    if size < 1:
        raise ValueError("size must be at least 1")
    start = (page_number - 1) * size
    return list(items[start:start + size])
