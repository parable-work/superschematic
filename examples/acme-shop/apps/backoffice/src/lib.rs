//! The program of app Backoffice of shop-stock (D78): the back office
//! staff run the warehouse from, a Topcoat app over shop-stock's Topcoat
//! crate, which a stack runs as a deployable of its own beside shop-stock's
//! server. Its entrypoint, which shop-stack's build writes, builds the
//! parts as the server builds its own: shop-stock's implementations over
//! shop-db's Postgres, its identity service, whose sessions shop-api's
//! sign-in starts, and shop-db's engine, open, where the stack's checkouts
//! hold and take the stock. Then it calls `serve`.
//!
//! The pages are under `/stock`: the stock's list and each product's, and
//! the forms that count and receive it; the engine console under
//! `/stock/engine`, whose Inventory pages receive stock through the Stock
//! panel's forms; and sign-in, the account, users and roles. Each page calls
//! shop-stock or the engine in-process as its caller: staff who hold
//! `stock.write` receive stock. The app serves its pages alone: neither
//! shop-stock's JSON API nor the engine's routes are on its address, so
//! exposing the app exposes no more of shop-stock than the pages do.
//! `shop_stock_with_parts_and_api` would mount them at `/api` and `/engine`.
//!
//! The image carries no Topcoat asset bundle, so the lists are GET forms
//! that load the page.

use acme_shop_stock_api::apps::BackofficeParts;
use acme_shop_stock_topcoat::RouterBuilderShopStockExt;
use topcoat::router::{Router, RouterBuilderDiscoverExt};

/// Serves the back office on `parts.addr`, every interface at the port
/// $PORT names, until the process receives Ctrl+C or SIGTERM. It binds the
/// address itself: `topcoat::start` would bind `HOST`, 127.0.0.1 unless
/// set, which no other host reaches.
pub async fn serve(parts: BackofficeParts) -> std::io::Result<()> {
    let listener = tokio::net::TcpListener::bind(parts.addr).await?;
    let router = Router::builder().discover().shop_stock_with_parts(parts).build();
    topcoat::serve(listener, router).await
}
