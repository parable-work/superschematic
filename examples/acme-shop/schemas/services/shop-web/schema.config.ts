import { ShopApi } from "@acme/shop-api";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The shop's static site: its code at web/shop-web lists shop-api's
// products from the browser. It builds with its package's build script
// into dist, and serves index.html for a path that names no file.
export default defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  calls: [ShopApi],
  site: { build: "build", output: "dist", fallback: "index.html" }
});
