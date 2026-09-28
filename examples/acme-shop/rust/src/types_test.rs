//! The same price and feed item as go/types_test.go, typescript/types.test.ts
//! and python/types_test.py, decoded with the generated Rust types.

use acme_shop_common_types::{Currency, FeedItem, Price};
use serde_json::json;

fn feed_item() -> serde_json::Value {
    json!({
        "sku": "darjeeling-first-flush",
        "name": "Darjeeling first flush",
        "price": {"amountCents": 1450, "currency": "GBP"},
        "tags": ["black", "india"],
        "attributes": {"origin": "Darjeeling", "harvest": "2026 spring"}
    })
}

#[test]
fn a_price_decodes() {
    let price: Price = serde_json::from_str(r#"{"amountCents": 1999, "currency": "EUR"}"#).expect("a price");
    assert_eq!(price.amount_cents, 1999);
    assert_eq!(price.currency, Currency::Eur);
    assert!(serde_json::from_str::<Price>(r#"{"amountCents": 1999, "currency": "YEN"}"#).is_err());
}

#[test]
fn a_feed_item_decodes() {
    let item: FeedItem = serde_json::from_value(feed_item()).expect("a valid feed item");
    assert_eq!(item.price.amount_cents, 1450);
    assert_eq!(item.attributes["origin"], "Darjeeling");
}

#[test]
fn strict_json_refuses_an_undeclared_key() {
    let mut with_colour = feed_item();
    with_colour["colour"] = json!("amber");
    assert!(serde_json::from_value::<FeedItem>(with_colour).is_err());

    // Price is not @strictJSON: serde skips the key.
    let price: Price = serde_json::from_str(r#"{"amountCents": 1450, "currency": "GBP", "note": "sale"}"#).expect("a price");
    assert_eq!(price.amount_cents, 1450);
}
