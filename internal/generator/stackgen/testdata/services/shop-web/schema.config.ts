import { ShopApi } from "@schemas/shop-api";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// A static site whose code at web/shop-web calls shop-api from the browser
// (D55). It builds with its package's build script into dist, the
// defaults, and serves index.html for a path that names no file.
export default defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  calls: [ShopApi],
  site: { fallback: "index.html" }
});
