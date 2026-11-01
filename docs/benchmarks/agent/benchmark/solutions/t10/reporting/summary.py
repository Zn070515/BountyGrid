def summarize(rows) -> str:
    total = sum(amount for _, amount in rows)
    return f"total: {total:.2f}"
