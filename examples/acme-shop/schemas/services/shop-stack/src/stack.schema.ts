import { ShopApi } from "@acme/shop-api";
import { ShopOrders } from "@acme/shop-orders";
import { ShopStock } from "@acme/shop-stock";
import { ShopStorefront } from "@acme/shop-storefront";
import { ShopWeb } from "@acme/shop-web";
import { environment, stack } from "@superschematic/stack";

// The shop as it runs: shop-api and shop-orders, each on a Go server of its
// own, and shop-db, the authDb of both, on a database; shop-stock, on a
// Rust server over shop-db too, which shop-orders' server calls to reserve
// an order's stock; shop-storefront, on a TypeScript server that Bun runs;
// and shop-web, the static site whose browser code calls shop-api.
// Shoppers and staff call every API, so all four are exposed; a site
// always is.
@stack({
  deploy: [ShopApi, ShopOrders, ShopStock, ShopStorefront, ShopWeb],
  expose: [ShopApi, ShopOrders, ShopStock, ShopStorefront]
})
export abstract class Shop {}

// This machine: `superschematic stack dev` runs Postgres in a container and
// each server as a process, and derives every connection string, URL and
// port. shop-orders' worker FulfilOrders runs as a process too, and
// fulfils each order as it is placed; its job ShipOrders runs every minute
// here, so an order placed while developing ships within one. shop-stock's
// worker ReorderStock, a Rust process, files a restock request for each
// product an order runs low, and its job OrderRestocks runs only when
// `superschematic stack run` asks, so each request stays open until then.
@environment({
  target: "local",
  settings: [
    { of: ShopOrders, job: "ShipOrders", schedule: "* * * * *" },
    { of: ShopStock, job: "OrderRestocks", enabled: false }
  ]
})
export abstract class Dev {}
