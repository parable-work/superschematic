//! Serves the acme shop's Topcoat app on 127.0.0.1:3000, its catalog
//! holding the one product. Sign in at `POST /sign-in` (a button on any
//! page would post there), then write a review at `/reviews`.

use std::sync::Arc;

use acme_shop_orders_server::Shop;
use acme_shop_topcoat::{app, product};

#[tokio::main]
async fn main() -> std::io::Result<()> {
    let shop = Arc::new(Shop::with_prices([(product(), 1999)]));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:3000").await?;
    println!("http://{}", listener.local_addr()?);
    topcoat::serve(listener, app(shop)).await
}
