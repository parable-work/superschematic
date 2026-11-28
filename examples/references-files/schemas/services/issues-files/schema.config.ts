import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The files issues and notes hold (D154): a bucket, private, whose objects
// a browser uploads straight to through URLs issues-api signs. Its config
// is all it has.
export default defineConfig({ name: "issues-files", kind: SchemaKind.Bucket, outputs: {} });
