from .summary import summarize

__all__ = ["format_row", "summarize"]


def format_row(name: str, amount: float) -> str:
    return f"{name}: {amount:.2f}"
