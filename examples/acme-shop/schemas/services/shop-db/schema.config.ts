import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    // SQLite too: the Topcoat app in topcoat/ can keep the shop in a
    // SQLite file, migrated from the SQLite plan.
    sql: { dialects: ["postgres", "sqlite"] },
    // Inventory, CollectionPoint, Checkout and Refund compose behaviors, so
    // they are engine types (D58, D70): the build writes their engine
    // document, and the project's engine crate with the commerce crate,
    // which every server, job, worker and runner on shop-db opens.
    engine: {
      // Ordered rules, the first that matches deciding. The shop's own
      // servers, jobs and workers call the engine in-process as their API
      // (D37): shop-orders keeps checkouts, refunds and their payments, and
      // through Holds the units and slots a checkout holds; shop-cart
      // fills a shopper's cart and reads the stock and the pickup slots;
      // shop-stock keeps the stock. Any other service is refused. The
      // runner, which expires checkouts and runs the Reactions rules, acts
      // as shop-db-engine-runner, holding each engine type's name. Staff
      // reach the engine at /engine/shop-db with their roles: refunds.approve
      // signs refunds off, and issuing one asks the checkout's Payments for
      // it (refundFor); orders.any reads every checkout; stock.write and
      // calendar.manage keep the stock and the collection points.
      policy: {
        rules: [
          { service: ["shop-orders"], action: ["read", "write"], allow: true },
          { service: ["shop-cart"], schema: ["Checkout"], action: ["read", "write"], allow: true },
          { service: ["shop-cart"], schema: ["Inventory", "CollectionPoint"], action: "read", allow: true },
          { service: ["shop-stock"], schema: ["Inventory"], action: ["read", "write"], allow: true },
          { service: true, allow: false },
          { subject: ["shop-db-engine-runner"], require: "{schema}" },
          { schema: ["Refund"], action: ["read", "write"], require: "refunds.approve" },
          { schema: ["Checkout"], action: "write", operation: ["refundFor"], require: "refunds.approve" },
          { schema: ["Checkout"], action: "read", require: ["orders.any", "refunds.approve"] },
          { schema: ["Inventory"], action: ["read", "write"], require: "stock.write" },
          { schema: ["CollectionPoint"], action: ["read", "write"], require: "calendar.manage" }
        ]
      },
      tools: {
        // The operations that move money or stock, which a build that
        // composes Payments or Stock must name (D70, amended). Every
        // Payments operation asks: an agent opens, captures, voids or
        // refunds a shopper's money only once a person says yes.
        // receiveStock is auto: booking a delivery in is a warehouse's
        // routine, recorded in the movement ledger with its reason, and
        // an adjustment takes it back. adjustStock and countStock ask:
        // each can take units out of sale, a judgment a person makes.
        invocation: {
          "Payments.openPayment": "ask",
          "Payments.capturePayment": "ask",
          "Payments.voidPayment": "ask",
          "Payments.refundPayment": "ask",
          "Payments.refundFor": "ask",
          "Stock.receiveStock": "auto",
          "Stock.adjustStock": "ask",
          "Stock.countStock": "ask"
        }
      }
    }
  }
});
