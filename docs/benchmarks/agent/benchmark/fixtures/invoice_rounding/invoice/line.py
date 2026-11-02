class Line:
    def __init__(self, quantity, unit_cents):
        self.quantity = quantity
        self.unit_cents = unit_cents

    def subtotal_cents(self):
        return self.quantity * self.unit_cents
