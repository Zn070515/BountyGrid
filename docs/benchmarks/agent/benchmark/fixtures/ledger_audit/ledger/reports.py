from .totals import total_amount


def account_summary(account):
    return {"name": account.name, "total": total_amount(account.transactions)}
