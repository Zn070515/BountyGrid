class Account:
    def __init__(self, name):
        self.name = name
        self.transactions = []

    def add(self, transaction):
        self.transactions.append(transaction)
