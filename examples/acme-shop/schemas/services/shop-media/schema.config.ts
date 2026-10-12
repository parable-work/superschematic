import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The images of the products shop-api sells: a bucket, private, whose
// objects a browser uploads and downloads through URLs shop-api signs
// (D54). Its config is all it has.
export default defineConfig({ name: "shop-media", kind: SchemaKind.Bucket, outputs: {} });
