//! Serves the acme shop's Topcoat app on 127.0.0.1:3000, its catalog
//! holding the one product and its users a shopper and a member of staff.
//! Open `/orders/new` or `/reviews`, sign in with the button there as
//! grace@example.com, with the password below, then place an order, filter
//! and cancel it at `/orders`, and write a review. A shopper sees their own
//! orders alone; sign in as ada@example.com, staff, to see and cancel every
//! customer's.
//!
//! With `DATABASE_URL` set, as superschematic-migrate reads it
//! (`sqlite:shop.db`, a `file:` URI, or a path), the shop and its users are
//! that SQLite file, which shop-db's SQLite plan must have migrated: its
//! reviews, orders and sessions outlive the process. A URL of another
//! database, such as a Postgres one left in the shell, stops the app, and
//! the error names its scheme alone, since the URL may hold a password.
//! Without it both are in memory.
//!
//! The console's lists are live: with Topcoat's runtime in the browser, a
//! filter and a page's links render the list again in place. The runtime's
//! script is a file of the asset bundle, which `topcoat asset bundle --bin
//! acme-shop-topcoat` (Topcoat's CLI) writes beside the built binary, in
//! `assets/`. Without a bundle there the app says so and serves the pages
//! without the runtime: a filter and a page's links load the page.

use std::sync::Arc;

use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_orders_server::{add_shopper, add_staff, users_at, users_in_memory};
use acme_shop_orders_topcoat::api::runtime::identity::StoreError;
use acme_shop_topcoat::{IDENTITY_CONFIG, app, shop_in_memory, stock_sqlite};
use topcoat::asset::{AssetBundle, AssetConfig};

/// The demo shopper's email, name and password.
const LOGIN: &str = "grace@example.com";
const NAME: &str = "Grace Hopper";
const PASSWORD: &str = "grace's password";

/// The demo staff's, who read and cancel every customer's orders.
const STAFF_LOGIN: &str = "ada@example.com";
const STAFF_NAME: &str = "Ada Lovelace";
const STAFF_PASSWORD: &str = "ada's password";

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let assets = match AssetBundle::load() {
        Ok(bundle) => Some(AssetConfig::from(bundle)),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            println!(
                "no asset bundle beside the binary (`topcoat asset bundle --bin acme-shop-topcoat` writes one): the console's lists load the page for each filter"
            );
            None
        }
        Err(err) => return Err(err.into()),
    };
    let router = match std::env::var("DATABASE_URL") {
        Ok(url) => {
            let shop = SqliteShop::open(&url).map_err(|err| format!("DATABASE_URL: {err}"))?;
            let users = users_at(&url, IDENTITY_CONFIG).map_err(|err| format!("DATABASE_URL: {err}"))?;
            // The demo's shopper and staff and the catalog's products,
            // which a real shop's own flows add, unless an earlier run
            // added them.
            match users.store().find_login(LOGIN).await {
                Ok(_) => {}
                Err(StoreError::NotFound) => add_shopper(&users, LOGIN, NAME, PASSWORD).await?,
                Err(err) => return Err(err.into()),
            }
            match users.store().find_login(STAFF_LOGIN).await {
                Ok(_) => {}
                Err(StoreError::NotFound) => add_staff(&users, STAFF_LOGIN, STAFF_NAME, STAFF_PASSWORD).await?,
                Err(err) => return Err(err.into()),
            }
            stock_sqlite(&shop)?;
            println!("the shop is in {url}");
            app(Arc::new(shop), users, assets)
        }
        Err(_) => {
            let users = users_in_memory(IDENTITY_CONFIG)?;
            add_shopper(&users, LOGIN, NAME, PASSWORD).await?;
            add_staff(&users, STAFF_LOGIN, STAFF_NAME, STAFF_PASSWORD).await?;
            app(Arc::new(shop_in_memory()), users, assets)
        }
    };
    let listener = tokio::net::TcpListener::bind("127.0.0.1:3000").await?;
    println!("http://{}", listener.local_addr()?);
    topcoat::serve(listener, router).await?;
    Ok(())
}
