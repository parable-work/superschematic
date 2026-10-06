//! Serves shop-orders with the generated Rust server, keeping orders and
//! reviews in memory. go/clients_test.go starts it, reads the URL it prints
//! and runs every language's client against it, as it runs them against
//! the Go server.

use std::net::SocketAddr;
use std::sync::Arc;

use acme_shop_orders_api::build_router;
use acme_shop_orders_server::{implementations, Shop};

#[tokio::main]
async fn main() {
    let router = build_router(implementations(Arc::new(Shop::default())));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("a local port");
    // The first line is the base URL the clients call.
    println!(
        "http://{}",
        listener.local_addr().expect("the bound address")
    );
    // The peer address keys @rateLimit per client.
    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<SocketAddr>(),
    )
    .await
    .expect("the server runs");
}
