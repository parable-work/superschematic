import { ShopApi } from "@schemas/shop-api";
import { ShopDb } from "@schemas/shop-db";
import { ShopOrders } from "@schemas/shop-orders";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  // The stack's build reads every service it reaches. Until the build plan
  // counts the handles a schema names as dependencies, the config lists
  // them, so a change to one rebuilds the stack.
  dependencies: [ShopDb, ShopApi, ShopOrders],
  outputs: {}
});
