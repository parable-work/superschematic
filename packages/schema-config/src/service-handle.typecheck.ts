// Type-level checks of ServiceHandle (docs/stack-model.md, section 4.3).
// Nothing imports this file: `bun run typecheck`, which `make ts` runs,
// checks it, and fails on an @ts-expect-error that no longer errors.
import { defineConfig, SchemaKind, service, type ServiceHandle } from "./index";

abstract class ShopApiConfig {
  LOG_LEVEL: string;
}

// What the sentinel generator writes: an API handle with its config type,
// and the other kinds with none.
const ShopApi = service<"API", ShopApiConfig>({ name: "shop-api", kind: SchemaKind.API });
const ShopDb = service({ name: "shop-db", kind: SchemaKind.DB });
const ShopCommon = service({ name: "shop-common", kind: SchemaKind.General });
const ShopCatalog = service({ name: "shop-catalog", kind: "Catalog" });

// A handle written by hand gets its kind from the enum member, as a string.
const handWrittenApi: ServiceHandle<"API"> = service({ name: "shop-orders", kind: SchemaKind.API });
const typedApi: ServiceHandle<"API", ShopApiConfig> = ShopApi;
const dbHandle: ServiceHandle<"DB"> = ShopDb;

// An API with @job classes: the sentinel writes their names as the third
// type argument, and the handle is still an API handle.
const ShopOrders = service<"API", unknown, "ExpireCarts" | "SendDigest">({ name: "shop-orders", kind: SchemaKind.API });
const ordersAsApi: ServiceHandle<"API"> = ShopOrders;

// Every handle is a ServiceHandle, so authDb and dependencies take any kind.
const anyHandles: readonly ServiceHandle[] = [ShopApi, ShopDb, ShopCommon, ShopCatalog, handWrittenApi, ShopOrders];

export const accepted = defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  authDb: ShopDb,
  dependencies: [ShopDb, ShopCommon],
  calls: [ShopApi, handWrittenApi, ShopOrders],
  outputs: {}
});

// @ts-expect-error a handle that may have any job does not stand for one whose jobs are named
const untypedJobs: ServiceHandle<"API", unknown, "ExpireCarts"> = handWrittenApi;

export const refusedDbCall = defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  // @ts-expect-error calls takes API handles, and ShopDb is a DB handle
  calls: [ShopDb],
  outputs: {}
});

export const refusedInlineDbCall = defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  // @ts-expect-error a DB handle written by hand is refused too
  calls: [service({ name: "shop-db", kind: SchemaKind.DB })],
  outputs: {}
});

export const refusedExtensionCall = defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  // @ts-expect-error an extension kind's handle is not an API handle
  calls: [ShopCatalog],
  outputs: {}
});

// @ts-expect-error a handle with no config type does not stand for one with
const untypedForTyped: ServiceHandle<"API", ShopApiConfig> = handWrittenApi;

// @ts-expect-error the kind argument must agree with the kind written
const mismatchedKind = service<"API">({ name: "shop-db", kind: SchemaKind.DB });

// @ts-expect-error a DB handle is not an API handle
const dbForApi: ServiceHandle<"API"> = ShopDb;

export const checked = { typedApi, dbHandle, anyHandles, untypedForTyped, mismatchedKind, dbForApi, ordersAsApi, untypedJobs };
