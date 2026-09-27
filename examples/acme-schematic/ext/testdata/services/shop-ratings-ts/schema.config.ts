import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The TypeScript twin of shop-ratings: a General schema whose Product
// composes acme.Rating with @behavior. A General schema imports only
// @superschematic/schema; tsconfig.json includes acme's augmentation.
export default defineConfig({
  name: "shop-ratings",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
