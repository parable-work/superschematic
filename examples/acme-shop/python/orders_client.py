"""Calls shop-orders through the generated Python SDK.

go/clients_test.go runs it against the Go server and compares what it
prints with the other languages' clients. Its arguments are the server's
base URL and the shopper's bearer token, token-1 when absent, as the Rust
server accepts it; the Go server takes a session token its sign-in issued.
"""

import sys
from typing import Any, Callable, Union

from acme_shop_orders_sdk import APIError, ClientConfig, ShopOrdersSDK
from acme_types_shop_orders import PlaceOrderInput, PlaceOrderLine, ShippingAddress, WriteReviewInput

PRODUCT = "00000000-0000-4000-8000-0000000000b1"
MISSING_ORDER = "00000000-0000-4000-8000-0000000000c1"


def status(call: Callable[[], Any]) -> Union[int, str]:
    try:
        call()
        return "ok"
    except APIError as error:
        return error.status_code


def main(base_url: str, token: str) -> None:
    anonymous = ShopOrdersSDK(ClientConfig(base_url=base_url))
    shopper = ShopOrdersSDK(ClientConfig(base_url=base_url, auth_token=token))

    reviews = anonymous.product_reviews.list_reviews(PRODUCT)
    print(f"reviews: {len(reviews)}")

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

    print(f"get a missing order: {status(lambda: shopper.order.get_order(MISSING_ORDER))}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2] if len(sys.argv) > 2 else "token-1")
