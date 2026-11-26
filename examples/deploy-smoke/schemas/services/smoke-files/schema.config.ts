import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The files a line item holds (D154): a bucket, private, whose objects a
// client uploads straight to through URLs smoke-api signs. Its config is
// all it has.
export default defineConfig({ name: "smoke-files", kind: SchemaKind.Bucket, outputs: {} });
