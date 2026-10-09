import { ShopApi } from "@schemas/shop-api";
import { ShopDb } from "@schemas/shop-db";
import { ShopMedia } from "@schemas/shop-media";
import { ShopOrders } from "@schemas/shop-orders";
import { ShopWeb } from "@schemas/shop-web";
import { environment, server, stack } from "@superschematic/stack";

// stack/stacktest's Shop WithSite, on its fake target, written as a schema.
@stack({ deploy: [ShopApi, ShopOrders, ShopWeb], expose: [ShopApi] })
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
  settings: [
    { of: Orders, env: { FULFILLMENT_REGION: "us" } },
    // shop-orders' job runs hourly, in New York's time.
    { of: ShopOrders, job: "ShipOrders", schedule: "0 * * * *", timeZone: "America/New_York" }
  ]
})
export abstract class Staging {}

@environment({
  target: "fake",
  fake: { project: "acme-prod", region: "us-east1", production: true },
  domain: "acme.dev",
  settings: [
    { of: ShopDb, tier: "large", highAvailability: true },
    { of: ShopApi, minInstances: 1, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, env: { FULFILLMENT_REGION: "us" } },
    { of: ShopOrders, job: "ShipOrders", cpu: "2" },
    { of: ShopMedia, versioning: true }
  ]
})
export abstract class Production {}

@environment({
  parameters: ["pr"],
  settings: [{ of: ShopApi, env: { PREVIEW_ID: { parameter: "pr" } } }]
})
export abstract class Preview extends Staging {}
