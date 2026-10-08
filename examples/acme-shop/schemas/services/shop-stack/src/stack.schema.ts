import { ShopApi } from "@acme/shop-api";
import { ShopOrders } from "@acme/shop-orders";
import { environment, stack } from "@superschematic/stack";

// The shop as it runs: shop-api and shop-orders, each on a server of its
// own, and shop-db, the authDb of both, on a database. Shoppers and staff
// call both APIs, so both are exposed. shop-storefront is served in
// TypeScript, which the local target does not run yet.
@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi, ShopOrders] })
export abstract class Shop {}

// This machine: `superschematic stack dev` runs Postgres in a container and
// each server as a process, and derives every connection string, URL and
// port. shop-orders' job ShipOrders runs every minute here, so an order
// placed while developing ships within one.
@environment({
  target: "local",
  settings: [{ of: ShopOrders, job: "ShipOrders", schedule: "* * * * *" }]
})
export abstract class Dev {}
