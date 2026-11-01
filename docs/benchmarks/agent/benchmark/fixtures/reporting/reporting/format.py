def format_row(name: str, amount: float) -> str:
    return f"{name}: {amount:.2f}"


def summarize(rows) -> str:
    total = sum(amount for _, amount in rows)
    return f"total: {total:.2f}"
