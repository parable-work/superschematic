import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// shop-api's product images (D54): a bucket's config is all it has.
export default defineConfig({ name: "shop-media", kind: SchemaKind.Bucket, outputs: {} });
