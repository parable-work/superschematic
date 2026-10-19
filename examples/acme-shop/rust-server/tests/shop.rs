//! shop-orders' lists page and its reviews search alike in memory (`Shop`)
//! and, with the `sqlite` feature, over a SQLite file of shop-db's tables
//! (`SqliteShop`): the catalog by name, with its lookup by ids; the orders
//! oldest first, filtered by status on every page; a product's reviews
//! oldest first, searched by every word of `q` in their titles and bodies,
//! a `%` matching itself. Each list follows `next` to its last page, whose
//! `next` is none, and refuses a cursor it never returned with 400. A
//! shopper lists, pages, reads and cancels their own orders alone, and
//! staff every customer's. scripts/check.sh runs them with `cargo test
//! --features sqlite`.

use std::thread::sleep;
use std::time::Duration;

use acme_shop_orders_api::runtime::page::{Cursor, Page, PageRequest};
use acme_shop_orders_api::runtime::{ApiError, Principal, RequestContext};
use acme_shop_orders_api::{
    types, OrderCancelOrderArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs,
    OrderPlaceOrderArgs, ProductCatalogGetProductArgs, ProductCatalogImplementation,
    ProductCatalogListProductsArgs, ProductReviewsImplementation, ProductReviewsListReviewsArgs,
    ProductReviewsWriteReviewArgs,
};
use acme_shop_orders_server::{Shop, SHOPPER_PERMISSIONS, STAFF_PERMISSIONS};

/// The catalog: its products' ids, SKUs, names and prices in cents.
const CATALOG: [(&str, &str, &str, i64); 3] = [
    (
        "00000000-0000-4000-8000-0000000000c3",
        "chisel",
        "Chisel",
        450,
    ),
    (
        "00000000-0000-4000-8000-0000000000a1",
        "anvil",
        "Anvil",
        1999,
    ),
    (
        "00000000-0000-4000-8000-0000000000b2",
        "bellows",
        "Bellows",
        2500,
    ),
];

/// The shoppers who write the reviews, one each, as shop-db's
/// one_per_author index allows.
const SHOPPERS: [&str; 3] = [
    "00000000-0000-4000-8000-0000000000e1",
    "00000000-0000-4000-8000-0000000000e2",
    "00000000-0000-4000-8000-0000000000e3",
];

fn id(text: &str) -> types::IdentityUUID {
    text.parse().expect("a UUID")
}

fn product(name: &str) -> types::IdentityUUID {
    let (product, ..) = CATALOG
        .iter()
        .find(|(.., n, _)| *n == name)
        .expect("a product of the catalog");
    id(product)
}

fn catalog() -> Vec<types::ProductView> {
    CATALOG
        .iter()
        .map(|&(product, sku, name, price_cents)| types::ProductView {
            id: id(product),
            sku: sku.to_owned(),
            name: name.to_owned(),
            price_cents,
            in_stock: true,
        })
        .collect()
}

/// A member of staff, who reads and cancels every customer's orders.
const STAFF: &str = "00000000-0000-4000-8000-0000000000f1";

/// A request of the shopper, who holds the shopper role's permissions.
fn shopper(id: &str) -> RequestContext {
    RequestContext {
        principal: Some(Principal::new(id, SHOPPER_PERMISSIONS)),
        ..RequestContext::default()
    }
}

/// A request of staff, who hold the staff role's.
fn staff() -> RequestContext {
    RequestContext {
        principal: Some(Principal::new(STAFF, STAFF_PERMISSIONS)),
        ..RequestContext::default()
    }
}

fn anyone() -> RequestContext {
    RequestContext::default()
}

/// The page after `page`, `limit` items.
fn after<T>(page: &Page<T>, limit: i64) -> PageRequest {
    PageRequest::after(page.next.clone().expect("a next page"), limit)
}

/// A cursor this server never returned: `{}`, which names no position.
fn forged() -> PageRequest {
    PageRequest::after(Cursor::parse("e30").expect("a cursor's text"), 2)
}

fn assert_refused_cursor<T: std::fmt::Debug>(result: Result<T, ApiError>) {
    let err = result.expect_err("a cursor the list never returned");
    assert_eq!(err.status.as_u16(), 400);
    assert_eq!(err.message, "cursor is not one this operation returned");
}

fn names(page: &Page<types::ProductView>) -> Vec<&str> {
    page.items
        .iter()
        .map(|product| product.name.as_str())
        .collect()
}

fn titles(page: &Page<types::ReviewView>) -> Vec<&str> {
    page.items
        .iter()
        .map(|review| review.title.as_str())
        .collect()
}

/// Orders, reviews and products page, and reviews search, the same over
/// any shop whose catalog is `CATALOG` and whose users include `SHOPPERS`.
async fn pages_and_searches<S>(shop: &S)
where
    S: OrderImplementation + ProductCatalogImplementation + ProductReviewsImplementation,
{
    products_page_by_name(shop).await;
    orders_page_oldest_first(shop).await;
    reviews_page_and_search(shop).await;
}

async fn products_page_by_name<S: ProductCatalogImplementation>(shop: &S) {
    let list = |ids: Option<Vec<types::IdentityUUID>>, page| {
        shop.list_products(anyone(), ProductCatalogListProductsArgs { ids, page })
    };
    let first = list(None, PageRequest::first(2)).await.unwrap();
    assert_eq!(names(&first), ["Anvil", "Bellows"]);
    let last = list(None, after(&first, 2)).await.unwrap();
    assert_eq!(names(&last), ["Chisel"]);
    assert_eq!(last.next, None);

    // The lookup: the products with the ids, by name, whatever the order of
    // the ids. No ids is every product.
    let looked_up = list(
        Some(vec![product("Chisel"), product("Anvil")]),
        PageRequest::first(100),
    )
    .await
    .unwrap();
    assert_eq!(names(&looked_up), ["Anvil", "Chisel"]);
    assert_eq!(looked_up.next, None);
    assert_eq!(
        names(
            &list(Some(Vec::new()), PageRequest::first(100))
                .await
                .unwrap()
        )
        .len(),
        3
    );
    assert_refused_cursor(list(None, forged()).await);

    let bellows = shop
        .get_product(
            anyone(),
            ProductCatalogGetProductArgs {
                product_id: product("Bellows"),
            },
        )
        .await
        .unwrap();
    assert_eq!(
        (bellows.sku.as_str(), bellows.price_cents, bellows.in_stock),
        ("bellows", 2500, true)
    );
    let missing = shop
        .get_product(
            anyone(),
            ProductCatalogGetProductArgs {
                product_id: id(SHOPPERS[0]),
            },
        )
        .await
        .expect_err("no product has a shopper's id");
    assert_eq!(missing.status.as_u16(), 404);
}

/// Two of the product, shipped to London.
fn order_of(name: &str) -> OrderPlaceOrderArgs {
    let input = types::PlaceOrderInput {
        lines: vec![types::PlaceOrderLine {
            product_id: product(name),
            quantity: 2.0,
        }],
        shipping_address: types::ShippingAddress {
            recipient: "Ada Lovelace".to_owned(),
            line1: "12 St James's Square".to_owned(),
            line2: None,
            city: "London".to_owned(),
            postcode: "SW1Y 4JH".to_owned(),
            country: "GB".to_owned(),
            phone: None,
        },
    };
    OrderPlaceOrderArgs { input }
}

fn order_ids(page: &Page<types::OrderView>) -> Vec<types::IdentityUUID> {
    page.items.iter().map(|order| order.id).collect()
}

async fn orders_page_oldest_first<S: OrderImplementation>(shop: &S) {
    let mut placed = Vec::new();
    for name in ["Anvil", "Bellows", "Chisel"] {
        let order = shop
            .place_order(shopper(SHOPPERS[0]), order_of(name))
            .await
            .unwrap();
        placed.push(order.id);
        // Each order is placed a millisecond or more after the one before,
        // so the list's order is the order they were placed in.
        sleep(Duration::from_millis(3));
    }
    let list = |statuses, page| {
        shop.list_orders(shopper(SHOPPERS[0]), OrderListOrdersArgs { statuses, page })
    };
    let ids = order_ids;

    let first = list(None, PageRequest::first(2)).await.unwrap();
    assert_eq!(ids(&first), placed[..2]);
    assert_eq!(
        first.items[1].total_cents, 5000,
        "an order's lines are read with it"
    );
    let last = list(None, after(&first, 2)).await.unwrap();
    assert_eq!(ids(&last), placed[2..]);
    assert_eq!(last.next, None);

    // A filter applies to every page: the cancelled order is on none of the
    // placed orders' pages.
    let cancel = OrderCancelOrderArgs {
        id: placed[1],
        reason: Some("Changed my mind".to_owned()),
    };
    shop.cancel_order(shopper(SHOPPERS[0]), cancel)
        .await
        .unwrap();
    let placed_only = Some(vec![types::OrderStatus::Placed]);
    let first = list(placed_only.clone(), PageRequest::first(1))
        .await
        .unwrap();
    assert_eq!(ids(&first), [placed[0]]);
    let second = list(placed_only, after(&first, 1)).await.unwrap();
    assert_eq!(ids(&second), [placed[2]]);
    assert_eq!(second.next, None);
    let cancelled = list(
        Some(vec![types::OrderStatus::Cancelled]),
        PageRequest::first(20),
    )
    .await
    .unwrap();
    assert_eq!(ids(&cancelled), [placed[1]]);
    assert_refused_cursor(list(None, forged()).await);
}

/// Every order `ctx` lists, a page of one at a time.
async fn every_order<S: OrderImplementation>(
    shop: &S,
    ctx: impl Fn() -> RequestContext,
) -> Vec<types::IdentityUUID> {
    let list = |page| {
        shop.list_orders(
            ctx(),
            OrderListOrdersArgs {
                statuses: None,
                page,
            },
        )
    };
    let mut page = list(PageRequest::first(1)).await.unwrap();
    let mut ids = order_ids(&page);
    while page.next.is_some() {
        page = list(after(&page, 1)).await.unwrap();
        ids.extend(order_ids(&page));
    }
    ids
}

/// Two shoppers place two orders each, in turn. Each lists, a page at a
/// time, and reads their own alone: another customer's order is the 404 of
/// one no one placed, to read and to cancel, and stays placed. Staff list
/// every customer's, oldest first, and read and cancel any.
async fn orders_are_their_customers<S: OrderImplementation>(shop: &S) {
    let mut placed: [Vec<types::IdentityUUID>; 2] = Default::default();
    for name in ["Anvil", "Bellows"] {
        for (n, customer) in SHOPPERS[..2].iter().enumerate() {
            let order = shop
                .place_order(shopper(customer), order_of(name))
                .await
                .unwrap();
            placed[n].push(order.id);
            sleep(Duration::from_millis(3));
        }
    }
    let get = |ctx, id| shop.get_order(ctx, OrderGetOrderArgs { id });
    let cancel = |ctx, id| {
        let reason = Some("Not mine".to_owned());
        shop.cancel_order(ctx, OrderCancelOrderArgs { id, reason })
    };
    for (n, customer) in SHOPPERS[..2].iter().enumerate() {
        assert_eq!(
            every_order(shop, || shopper(customer)).await,
            placed[n],
            "{customer} lists their own orders"
        );
        let own = get(shopper(customer), placed[n][0]).await.unwrap();
        assert_eq!(own.id, placed[n][0]);
        let theirs = placed[1 - n][0];
        for refused in [
            get(shopper(customer), theirs).await,
            cancel(shopper(customer), theirs).await,
        ] {
            let err = refused.expect_err("another customer's order");
            assert_eq!(
                (err.status.as_u16(), err.message.as_str()),
                (404, "order not found")
            );
        }
    }

    // Staff list everyone's, as they were placed, every one still placed.
    let everyone = vec![placed[0][0], placed[1][0], placed[0][1], placed[1][1]];
    assert_eq!(every_order(shop, staff).await, everyone);
    for id in &everyone {
        let order = get(staff(), *id).await.unwrap();
        assert_eq!(order.status, types::OrderStatus::Placed);
    }
    let cancelled = cancel(staff(), placed[1][0]).await.unwrap();
    assert_eq!(
        (cancelled.status, cancelled.cancel_reason.as_deref()),
        (types::OrderStatus::Cancelled, Some("Not mine"))
    );
    let seen = get(shopper(SHOPPERS[1]), placed[1][0]).await.unwrap();
    assert_eq!(seen.status, types::OrderStatus::Cancelled);
}

async fn reviews_page_and_search<S: ProductReviewsImplementation>(shop: &S) {
    let reviews = [
        (5.0, "Sturdy and heavy", "Survived three coyotes."),
        (2.0, "Too heavy", "100% iron, and it shows."),
        (4.0, "Rings true", "A STURDY anvil, heavy enough."),
    ];
    for (shopper_id, (rating, title, body)) in SHOPPERS.iter().zip(reviews) {
        let input = types::WriteReviewInput {
            rating,
            title: title.to_owned(),
            body: body.to_owned(),
        };
        let args = ProductReviewsWriteReviewArgs {
            product_id: product("Anvil"),
            input,
        };
        shop.write_review(shopper(shopper_id), args).await.unwrap();
        sleep(Duration::from_millis(3));
    }
    let again = types::WriteReviewInput {
        rating: 1.0,
        title: "Again".to_owned(),
        body: String::new(),
    };
    let refused = shop
        .write_review(
            shopper(SHOPPERS[0]),
            ProductReviewsWriteReviewArgs {
                product_id: product("Anvil"),
                input: again,
            },
        )
        .await
        .expect_err("a second review of the anvil by one shopper");
    assert_eq!(refused.status.as_u16(), 409);

    let list = |q: Option<&str>, min_rating, page| {
        let args = ProductReviewsListReviewsArgs {
            product_id: product("Anvil"),
            min_rating,
            q: q.map(str::to_owned),
            page,
        };
        shop.list_reviews(anyone(), args)
    };
    let all = |q| async move {
        titles(&list(q, None, PageRequest::first(50)).await.unwrap()).join(" | ")
    };

    // Every word, in any case, in the title or the body.
    assert_eq!(all(Some("sturdy")).await, "Sturdy and heavy | Rings true");
    assert_eq!(all(Some("COYOTES sturdy")).await, "Sturdy and heavy");
    assert_eq!(
        all(Some("  ")).await,
        "Sturdy and heavy | Too heavy | Rings true"
    );
    assert_eq!(all(None).await, "Sturdy and heavy | Too heavy | Rings true");
    // A % or _ in the search matches itself, not any text.
    assert_eq!(all(Some("100%")).await, "Too heavy");
    assert_eq!(all(Some("1%0")).await, "");
    assert_eq!(all(Some("i_on")).await, "");
    let rated = list(Some("sturdy"), Some(5.0), PageRequest::first(50))
        .await
        .unwrap();
    assert_eq!(titles(&rated), ["Sturdy and heavy"]);

    // A search pages as the list does, a review at a time.
    let mut page = list(Some("heavy"), None, PageRequest::first(1))
        .await
        .unwrap();
    let mut seen = titles(&page).join(" | ");
    while page.next.is_some() {
        page = list(Some("heavy"), None, after(&page, 1)).await.unwrap();
        for title in titles(&page) {
            seen.push_str(" | ");
            seen.push_str(title);
        }
    }
    assert_eq!(seen, "Sturdy and heavy | Too heavy | Rings true");
    assert_refused_cursor(list(None, None, forged()).await);

    let other = shop
        .list_reviews(
            anyone(),
            ProductReviewsListReviewsArgs {
                product_id: product("Bellows"),
                min_rating: None,
                q: None,
                page: PageRequest::first(20),
            },
        )
        .await
        .unwrap();
    assert_eq!((other.items.len(), other.next), (0, None));
}

#[tokio::test]
async fn the_shop_in_memory_pages_and_searches() {
    pages_and_searches(&Shop::with_products(catalog())).await;
}

#[tokio::test]
async fn the_shop_in_memory_keeps_orders_to_their_customers() {
    orders_are_their_customers(&Shop::with_products(catalog())).await;
}

#[cfg(feature = "sqlite")]
mod sqlite {
    use acme_shop_orders_server::sqlite::SqliteShop;

    use super::*;

    /// A SQLite shop in memory, its tables shop-db's, as the build's
    /// `sqlite/create.sql` creates them, with the catalog's products and
    /// the shoppers and staff as users.
    fn sqlite_shop() -> SqliteShop {
        let conn = rusqlite::Connection::open_in_memory().unwrap();
        conn.execute_batch(include_str!(
            "../../schemas/dist-rust/sql/shop-db/sqlite/create.sql"
        ))
        .unwrap();
        let shop = SqliteShop::with_connection(conn)
            .unwrap()
            .expect("shop-db's tables");
        for (product, sku, name, price_cents) in CATALOG {
            shop.add_product(&id(product), sku, name, price_cents)
                .unwrap();
        }
        for (n, shopper) in SHOPPERS.iter().enumerate() {
            shop.add_user(
                &id(shopper),
                &format!("shopper{n}@example.com"),
                "A shopper",
            )
            .unwrap();
        }
        shop.add_user(&id(STAFF), "staff@example.com", "A member of staff")
            .unwrap();
        shop
    }

    #[tokio::test]
    async fn the_shop_in_sqlite_pages_and_searches() {
        pages_and_searches(&sqlite_shop()).await;
    }

    #[tokio::test]
    async fn the_shop_in_sqlite_keeps_orders_to_their_customers() {
        orders_are_their_customers(&sqlite_shop()).await;
    }
}
