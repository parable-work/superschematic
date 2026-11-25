import { ShopCommon } from "@acme/shop-common";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  dependencies: [ShopCommon],
  // An older copy of a client the environment's MIN_CLIENT_VERSIONS lists
  // is answered 426 (D143).
  clientVersions: true,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
