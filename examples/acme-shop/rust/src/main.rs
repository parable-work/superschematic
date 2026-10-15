//! Calls shop-orders through the generated Rust SDK. go/clients_test.go runs
//! it against the Go server and compares what it prints with the other
//! languages' clients. Its arguments are the server's base URL and the
//! shopper's bearer token, which shop-api's login issued: the Go and the
//! Rust server both read its session from shop-db's users.

use acme_shop_orders_sdk::namespaces::order::ListOrdersQueryParams;
use acme_shop_orders_sdk::namespaces::product_catalog::ListProductsQueryParams;
use acme_shop_orders_sdk::namespaces::product_reviews::ListReviewsQueryParams;
use acme_shop_orders_sdk::types::{IdentityUUID, PlaceOrderInput, PlaceOrderLine, ShippingAddress, WriteReviewInput};
use acme_shop_orders_sdk::{ClientConfig, Page, Pager, SDKError, ShopOrdersSdk};

#[cfg(test)]
mod types_test;

const PRODUCT: &str = "00000000-0000-4000-8000-0000000000b1";
const MISSING_ORDER: &str = "00000000-0000-4000-8000-0000000000c1";

fn status<T>(result: Result<T, SDKError>) -> String {
    match result {
        Ok(_) => "ok".to_string(),
        Err(SDKError::Api { status_code, .. }) => status_code.to_string(),
        Err(other) => panic!("{other}"),
    }
}

/// Whether a page is the last, which its next cursor, `None`, says.
fn last_page<T>(page: &Page<T>) -> &'static str {
    if page.next.is_none() {
        "the last page"
    } else {
        "more after it"
    }
}

/// How many items a pager yields, asking for each page as it needs it.
async fn count<T>(mut items: Pager<'_, T>) -> Result<usize, SDKError> {
    let mut n = 0;
    while let Some(item) = items.next().await {
        item?;
        n += 1;
    }
    Ok(n)
}

#[tokio::main]
async fn main() -> Result<(), SDKError> {
    let base_url = std::env::args().nth(1).expect("the server's base URL");
    let token = std::env::args().nth(2).expect("the shopper's bearer token");
    let anonymous = ShopOrdersSdk::new(ClientConfig::with_base_url(&base_url, None, None))?;
    let shopper = ShopOrdersSdk::new(ClientConfig::with_base_url(&base_url, Some(token), None))?;
    let product: IdentityUUID = PRODUCT.parse().expect("a UUID");

    // A paged list answers one page; its _all method yields every item,
    // asking for each page with the next of the one before.
    let two = ListProductsQueryParams { limit: Some(2), ..Default::default() };
    let products = anonymous.product_catalog.list_products(Some(&two), None).await?;
    println!("products: {}, {}", products.items.len(), last_page(&products));
    println!("every product: {}", count(anonymous.product_catalog.list_products_all(Some(&two), None)).await?);

    println!("get a missing product: {}", status(anonymous.product_catalog.get_product(product, None).await));

    let smoky = ListReviewsQueryParams { q: Some("smoky".to_string()), ..Default::default() };
    let reviews = anonymous.product_reviews.list_reviews(product, Some(&smoky), None).await?;
    println!("reviews that say smoky: {}, {}", reviews.items.len(), last_page(&reviews));

    let review = WriteReviewInput {
        rating: 5.0,
        title: "Lovely".to_string(),
        body: "Smoky and smooth.".to_string(),
    };
    println!(
        "write a review without a token: {}",
        status(anonymous.product_reviews.write_review(product, review, None).await)
    );

    let order = PlaceOrderInput {
        lines: vec![PlaceOrderLine { product_id: product, quantity: 2.0 }],
        shipping_address: ShippingAddress {
            recipient: "Ada Lovelace".to_string(),
            line1: "12 St James's Square".to_string(),
            line2: None,
            city: "London".to_string(),
            postcode: "SW1Y 4JH".to_string(),
            country: "GB".to_string(),
            phone: None,
        },
    };
    println!("order a product that does not exist: {}", status(shopper.order.place_order(order, None).await));

    let one = ListOrdersQueryParams { limit: Some(1), ..Default::default() };
    let orders = shopper.order.list_orders(Some(&one), None).await?;
    println!("orders: {}, {}", orders.items.len(), last_page(&orders));
    println!("every order: {}", count(shopper.order.list_orders_all(Some(&one), None)).await?);

    // e30 is the cursor of {}, a key that names no order.
    let forged = ListOrdersQueryParams { cursor: Some("e30".to_string()), ..Default::default() };
    println!(
        "orders after a cursor the server never returned: {}",
        status(shopper.order.list_orders(Some(&forged), None).await)
    );

    let missing: IdentityUUID = MISSING_ORDER.parse().expect("a UUID");
    println!("get a missing order: {}", status(shopper.order.get_order(missing, None).await));
    Ok(())
}
