"""The same price and feed item as go/types_test.go, typescript/types.test.ts
and rust/src/types_test.rs, decoded with the generated Python types.

scripts/check.sh runs it: python -B python/types_test.py
"""

import json

from acme_types_shop_common import Currency, FeedItem, Price
from pydantic import ValidationError

FEED_ITEM = {
    "sku": "darjeeling-first-flush",
    "name": "Darjeeling first flush",
    "price": {"amountCents": 1450, "currency": "GBP"},
    "tags": ["black", "india"],
    "attributes": {"origin": "Darjeeling", "harvest": "2026 spring"},
}


def test_a_price_decodes() -> None:
    price = Price.from_json('{"amountCents": 1999, "currency": "EUR"}')
    assert price.amount_cents == 1999 and price.currency == Currency.EUR
    try:
        Price.from_json('{"amountCents": 1999, "currency": "YEN"}')
    except ValidationError:
        pass
    else:
        raise AssertionError("Price accepted a currency outside the enum")


def test_a_feed_item_decodes() -> None:
    item = FeedItem.from_json(json.dumps(FEED_ITEM))
    assert item.price.amount_cents == 1450
    assert item.attributes["origin"] == "Darjeeling"


def test_strict_json_refuses_an_undeclared_key() -> None:
    try:
        FeedItem.from_json_non_strict(json.dumps({**FEED_ITEM, "colour": "amber"}))
    except ValidationError:
        pass
    else:
        raise AssertionError("FeedItem accepted a key it does not declare")
    # Price is not @strictJSON: its non-strict decoder skips the key.
    price = Price.from_json_non_strict('{"amountCents": 1450, "currency": "GBP", "note": "sale"}')
    assert price.amount_cents == 1450


if __name__ == "__main__":
    test_a_price_decodes()
    test_a_feed_item_decodes()
    test_strict_json_refuses_an_undeclared_key()
    print("ok")
