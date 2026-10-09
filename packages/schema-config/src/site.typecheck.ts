// Type-level checks of a Site service's config (docs/stack-model.md,
// section 8.10, D55). Nothing imports this file: `bun run typecheck`,
// which `make ts` runs, checks it, and fails on an @ts-expect-error that no
// longer errors.
import { defineConfig, SchemaKind, service } from "./index";

const ShopApi = service({ name: "shop-api", kind: SchemaKind.API });
const ShopDb = service({ name: "shop-db", kind: SchemaKind.DB });

// A site names the APIs it calls and how it builds, and needs no outputs.
export const site = defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  calls: [ShopApi],
  site: { build: "build", output: "dist", fallback: "index.html" }
});

// Every member of site is optional.
export const defaults = defineConfig({ name: "shop-web", kind: SchemaKind.Site, site: {} });

export const refusedCall = defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  // @ts-expect-error a site calls API services only
  calls: [ShopDb]
});

export const refusedKey = defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  // @ts-expect-error site takes build, output and fallback only
  site: { outDir: "dist" }
});
