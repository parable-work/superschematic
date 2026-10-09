//! Serves the acme shop's Topcoat app on 127.0.0.1:3000, its catalog
//! holding the one product. Sign in at `POST /sign-in` (a button on any
//! page would post there), then write a review at `/reviews`.
//!
//! With `DATABASE_URL` set, as superschematic-migrate reads it
//! (`sqlite:shop.db`, or a path), the shop is that SQLite file, which
//! shop-db's SQLite plan must have migrated: its reviews and orders outlive
//! the process. Without it the shop is in memory.

use std::sync::Arc;

use acme_shop_orders_server::Shop;
use acme_shop_orders_server::sqlite::SqliteShop;
use acme_shop_topcoat::{app, product, shopper};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let router = match std::env::var("DATABASE_URL") {
        Ok(url) => {
            let shop = SqliteShop::open(&url)?;
            // The demo's shopper and product, which a real shop's own
            // flows add.
            shop.add_user(&shopper(), "shopper@example.com", "Demo Shopper")?;
            shop.add_product(&product(), "anvil", "Anvil", 1999)?;
            println!("the shop is in {url}");
            app(Arc::new(shop))
        }
        Err(_) => app(Arc::new(Shop::with_prices([(product(), 1999)]))),
    };
    let listener = tokio::net::TcpListener::bind("127.0.0.1:3000").await?;
    println!("http://{}", listener.local_addr()?);
    topcoat::serve(listener, router).await?;
    Ok(())
}
