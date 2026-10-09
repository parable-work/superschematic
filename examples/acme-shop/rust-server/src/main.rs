//! Serves shop-orders with the generated Rust server, keeping orders and
//! reviews in memory and reading the shop's users from the SQLite database
//! ACME_SHOP_DATABASE names, which holds shop-db's identity tables.
//! go/clients_test.go creates that database, signs a shopper in through
//! shop-api's login over it, starts this server, reads the URL it prints and
//! runs every language's client against it with the shopper's token, as it
//! runs them against the Go server.

use std::net::SocketAddr;
use std::sync::Arc;

use acme_shop_orders_api::build_router;
use acme_shop_orders_server::{implementations, users, Shop};

/// The identity config: the server serves plain HTTP on a local port, so a
/// session cookie is not `Secure`.
const IDENTITY_CONFIG: &str = r#"{"cookie": {"secure": false}}"#;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let database = std::env::var("ACME_SHOP_DATABASE")
        .map_err(|_| "set ACME_SHOP_DATABASE to the shop's SQLite database")?;
    let users = users(&database, IDENTITY_CONFIG)?;
    let router = build_router(implementations(Arc::new(Shop::default()), users));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await?;
    // The first line is the base URL the clients call.
    println!("http://{}", listener.local_addr()?);
    // The peer address keys @rateLimit per client.
    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<SocketAddr>(),
    )
    .await?;
    Ok(())
}
