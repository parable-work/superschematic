//! Calls shop-orders through the generated Rust SDK. go/clients_test.go runs
//! it against the Go server and compares what it prints with the other
//! languages' clients.

use acme_shop_orders_sdk::types::{IdentityUUID, PlaceOrderInput, PlaceOrderLine, ShippingAddress, WriteReviewInput};
use acme_shop_orders_sdk::{ClientConfig, SDKError, ShopOrdersSdk};

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

#[tokio::main]
async fn main() -> Result<(), SDKError> {
    let base_url = std::env::args().nth(1).expect("the server's base URL");
    let anonymous = ShopOrdersSdk::new(ClientConfig::with_base_url(&base_url, None, None))?;
    let shopper = ShopOrdersSdk::new(ClientConfig::with_base_url(&base_url, Some("token-1".to_string()), None))?;
    let product: IdentityUUID = PRODUCT.parse().expect("a UUID");

    let reviews = anonymous.product_reviews.list_reviews(product, None, None).await?;
    println!("reviews: {}", reviews.len());

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

    let missing: IdentityUUID = MISSING_ORDER.parse().expect("a UUID");
    println!("get a missing order: {}", status(shopper.order.get_order(missing, None).await));
    Ok(())
}
