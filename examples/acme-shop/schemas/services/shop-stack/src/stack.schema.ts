import { ShopApi } from "@acme/shop-api";
import { ShopOrders } from "@acme/shop-orders";
import { ShopStorefront } from "@acme/shop-storefront";
import { ShopWeb } from "@acme/shop-web";
import { environment, stack } from "@superschematic/stack";

// The shop as it runs: shop-api and shop-orders, each on a Go server of its
// own, and shop-db, the authDb of both, on a database; shop-storefront, on
// a TypeScript server that Bun runs; and shop-web, the static site whose
// browser code calls shop-api. Shoppers and staff call every API, so all
// three are exposed; a site always is.
@stack({ deploy: [ShopApi, ShopOrders, ShopStorefront, ShopWeb], expose: [ShopApi, ShopOrders, ShopStorefront] })
export abstract class Shop {}

// This machine: `superschematic stack dev` runs Postgres in a container and
// each server as a process, and derives every connection string, URL and
// port. shop-orders' worker FulfilOrders runs as a process too, and
// fulfils each order as it is placed; its job ShipOrders runs every minute
// here, so an order placed while developing ships within one.
@environment({
  target: "local",
  settings: [{ of: ShopOrders, job: "ShipOrders", schedule: "* * * * *" }]
})
export abstract class Dev {}
