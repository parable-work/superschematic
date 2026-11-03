import { ShopApi } from "@acme/shop-api";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The shop's static site: its code at web/shop-web lists shop-api's
// products from the browser. It builds with its package's build script
// into dist, and serves index.html for a path that names no file. Under
// stack dev its dev script runs Bun's dev server, which reloads the page on
// an edit, behind a proxy that serves shop-api under /api/shop-api on the
// site's origin (D82).
export default defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  calls: [ShopApi],
  site: { build: "build", output: "dist", fallback: "index.html", dev: { command: "dev" } }
});
