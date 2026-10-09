//! Serves the acme shop's Topcoat app on 127.0.0.1:3000, its catalog
//! holding the one product and its users one shopper, both in memory. Sign
//! in at `/sign-in` as grace@example.com, with the password below, then
//! write a review at `/reviews`.

use std::sync::Arc;

use acme_shop_orders_server::{Shop, add_shopper, users_in_memory};
use acme_shop_topcoat::{IDENTITY_CONFIG, app, product};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let users = users_in_memory(IDENTITY_CONFIG)?;
    add_shopper(&users, "grace@example.com", "Grace Hopper", "grace's password").await?;
    let shop = Arc::new(Shop::with_prices([(product(), 1999)]));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:3000").await?;
    println!("http://{}", listener.local_addr()?);
    topcoat::serve(listener, app(shop, users)).await?;
    Ok(())
}
