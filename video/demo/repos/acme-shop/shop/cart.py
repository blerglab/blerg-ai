"""A shopping cart with line items and a total in cents."""


class Cart:
    def __init__(self):
        self.items = {}

    def add(self, sku, price_cents, qty=1):
        line = self.items.setdefault(sku, [price_cents, 0])
        line[1] += qty

    def total(self):
        return sum(price * qty for price, qty in self.items.values())
