import { ShopApi } from "@schemas/shop-api";
import { ShopDb } from "@schemas/shop-db";
import { ShopOrders } from "@schemas/shop-orders";
import { environment, server, stack } from "@superschematic/stack";

// The stack of docs/stack-model.md, section 4.1, on stacktest's fake target:
// stack/stacktest's Shop, written as a schema.
@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi] })
export abstract class Shop {}

// Serves shop-orders in place of its default server. Its edges are
// shop-orders': shop-db through authDb, and shop-api through calls.
@server({ serves: [ShopOrders] })
export abstract class Orders {}

@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  domain: "staging.acme.dev",
  dns: { "fake.dns": { zone: "acme.dev" } },
  settings: [{ of: Orders, env: { FULFILLMENT_REGION: "us" } }]
})
export abstract class Staging {}

@environment({
  target: "fake",
  fake: { project: "acme-prod", region: "us-east1", production: true },
  domain: "acme.dev",
  settings: [
    { of: ShopDb, tier: "large", highAvailability: true },
    { of: ShopApi, minInstances: 1, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, env: { FULFILLMENT_REGION: "us" } }
  ]
})
export abstract class Production {}

@environment({
  parameters: ["pr"],
  settings: [{ of: ShopApi, env: { PREVIEW_ID: { parameter: "pr" } } }]
})
export abstract class Preview extends Staging {}
