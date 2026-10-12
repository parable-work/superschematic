//! Serves the acme shop's Topcoat app on 127.0.0.1:3000, its catalog
//! holding the one product and its users one shopper. Open `/orders/new`
//! or `/reviews`, sign in with the button there as grace@example.com, with
//! the password below, then place an order, filter and cancel it at
//! `/orders`, and write a review.
//!
//! With `DATABASE_URL` set, as superschematic-migrate reads it
//! (`sqlite:shop.db`, a `file:` URI, or a path), the shop and its users are
//! that SQLite file, which shop-db's SQLite plan must have migrated: its
//! reviews, orders and sessions outlive the process. A URL of another
//! database, such as a Postgres one left in the shell, stops the app, and
//! the error names its scheme alone, since the URL may hold a password.
//! Without it both are in memory.

use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{add_shopper, users_at, users_in_memory};
use acme_shop_orders_topcoat::api::runtime::identity::StoreError;
use acme_shop_topcoat::{IDENTITY_CONFIG, app, shop_in_memory, stock_sqlite};

/// The demo shopper's email, name and password.
const LOGIN: &str = "grace@example.com";
const NAME: &str = "Grace Hopper";
const PASSWORD: &str = "grace's password";

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let router = match std::env::var("DATABASE_URL") {
        Ok(url) => {
            let shop = SqliteShop::open(&url).map_err(|err| format!("DATABASE_URL: {err}"))?;
            let users = users_at(&url, IDENTITY_CONFIG).map_err(|err| format!("DATABASE_URL: {err}"))?;
            // The demo's shopper and the catalog's products, which a real
            // shop's own flows add, unless an earlier run added them.
            match users.store().find_login(LOGIN).await {
                Ok(_) => {}
                Err(StoreError::NotFound) => add_shopper(&users, LOGIN, NAME, PASSWORD).await?,
                Err(err) => return Err(err.into()),
            }
            stock_sqlite(&shop)?;
            println!("the shop is in {url}");
            app(Arc::new(shop), users)
        }
        Err(_) => {
            let users = users_in_memory(IDENTITY_CONFIG)?;
            add_shopper(&users, LOGIN, NAME, PASSWORD).await?;
            app(Arc::new(shop_in_memory()), users)
        }
    };
    let listener = tokio::net::TcpListener::bind("127.0.0.1:3000").await?;
    println!("http://{}", listener.local_addr()?);
    topcoat::serve(listener, router).await?;
    Ok(())
}
