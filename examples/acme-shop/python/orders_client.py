"""Calls shop-orders through the generated Python SDK.

go/clients_test.go runs it against the Go server and compares what it
prints with the other languages' clients. Its arguments are the server's
base URL and the shopper's bearer token, which shop-api's login issued: the
Go and the Rust server both read its session from shop-db's users.
"""

import sys
from typing import Any, Callable, Union

from acme_shop_orders_sdk import APIError, ClientConfig, Page, ShopOrdersSDK
from acme_types_shop_orders import PlaceOrderInput, PlaceOrderLine, ShippingAddress, WriteReviewInput

PRODUCT = "00000000-0000-4000-8000-0000000000b1"
MISSING_ORDER = "00000000-0000-4000-8000-0000000000c1"


def status(call: Callable[[], Any]) -> Union[int, str]:
    try:
        call()
        return "ok"
    except APIError as error:
        return error.status_code


def last_page(page: Page[Any]) -> str:
    """Whether a page is the last, which its next cursor, None, says."""
    return "the last page" if page.next is None else "more after it"


def main(base_url: str, token: str) -> None:
    anonymous = ShopOrdersSDK(ClientConfig(base_url=base_url))
    shopper = ShopOrdersSDK(ClientConfig(base_url=base_url, auth_token=token))

    # A paged list answers one page; its _all method yields every item,
    # asking for each page with the next of the one before.
    products = anonymous.product_catalog.list_products(limit=2)
    print(f"products: {len(products.items)}, {last_page(products)}")
    print(f"every product: {sum(1 for _ in anonymous.product_catalog.list_products_all(limit=2))}")

    print(f"get a missing product: {status(lambda: anonymous.product_catalog.get_product(PRODUCT))}")

    reviews = anonymous.product_reviews.list_reviews(PRODUCT, q="smoky")
    print(f"reviews that say smoky: {len(reviews.items)}, {last_page(reviews)}")

    review = WriteReviewInput(rating=5, title="Lovely", body="Smoky and smooth.")
    print(f"write a review without a token: {status(lambda: anonymous.product_reviews.write_review(PRODUCT, review))}")

    order = PlaceOrderInput(
        lines=[PlaceOrderLine(product_id=PRODUCT, quantity=2)],
        shipping_address=ShippingAddress(
            recipient="Ada Lovelace",
            line1="12 St James's Square",
            line2=None,
            city="London",
            postcode="SW1Y 4JH",
            country="GB",
            phone=None,
        ),
    )
    print(f"order a product that does not exist: {status(lambda: shopper.order.place_order(order))}")

    orders = shopper.order.list_orders(limit=1)
    print(f"orders: {len(orders.items)}, {last_page(orders)}")
    print(f"every order: {sum(1 for _ in shopper.order.list_orders_all(limit=1))}")

    # e30 is the cursor of {}, a key that names no order.
    print(f"orders after a cursor the server never returned: {status(lambda: shopper.order.list_orders(cursor='e30'))}")

    print(f"get a missing order: {status(lambda: shopper.order.get_order(MISSING_ORDER))}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
