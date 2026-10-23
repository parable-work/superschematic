import { ShopApi } from "@acme/shop-api";
import { ShopCart } from "@acme/shop-cart";
import { ShopOrders } from "@acme/shop-orders";
import { ShopStock } from "@acme/shop-stock";
import { ShopStorefront } from "@acme/shop-storefront";
import { ShopWeb } from "@acme/shop-web";
import { environment, stack } from "@superschematic/stack";

// The shop as it runs: shop-api and shop-orders, each on a Go server of its
// own, and shop-db, the authDb of both, on a database, whose engine types
// (D70) every server, job and worker on it opens, and whose engine runner
// joins as a deployable of its own, in shop-api's Go; shop-stock, on a
// Rust server over shop-db too, which shop-orders' server tells what a
// checkout sold; shop-cart, a shopper's cart and the pickup slots, and
// shop-storefront, each on a TypeScript server that Bun runs; and shop-web,
// the static site whose browser code calls shop-api. Shoppers and staff
// call every API, so all five are exposed; a site always is.
@stack({
  deploy: [ShopApi, ShopCart, ShopOrders, ShopStock, ShopStorefront, ShopWeb],
  expose: [ShopApi, ShopCart, ShopOrders, ShopStock, ShopStorefront]
})
export abstract class Shop {}

// This machine: `superschematic stack dev` runs Postgres in a container and
// each server as a process, and derives every connection string, URL and
// port. shop-orders' worker FulfilOrders runs as a process too, and
// fulfils each order as it is placed; its job ShipOrders runs every minute
// here, so an order placed while developing ships within one, and so does
// SettlePayments, so a payment acmepay takes settles within one.
// shop-stock's worker ReorderStock, a Rust process, files a restock request
// for each product a sale runs low, and its job OrderRestocks runs only
// when `superschematic stack run` asks, so each request stays open until
// then. shop-cart's deriver TriageTickets, a Bun process, fills in each
// support ticket's category, priority and summary seconds after it is
// opened (D74). acmepay's keys are this environment's secrets
// (`superschematic stack secrets set Dev`), and acmepay itself is go/acmepay's
// command, at the address shop-orders' ACMEPAY_URL defaults to.
@environment({
  target: "local",
  settings: [
    { of: ShopOrders, job: "ShipOrders", schedule: "* * * * *" },
    { of: ShopOrders, job: "SettlePayments", schedule: "* * * * *" },
    { of: ShopStock, job: "OrderRestocks", enabled: false }
  ]
})
export abstract class Dev {}
