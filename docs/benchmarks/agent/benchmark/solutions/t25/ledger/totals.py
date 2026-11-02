def total_amount(transactions):
    """Return the sum of the transaction amounts."""
    return sum(t.amount for t in transactions)
