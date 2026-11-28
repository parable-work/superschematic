import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The files fixture-references-db's tickets and notes hold (D154): a
// bucket, private, whose objects a browser uploads straight to through
// URLs fixture-references-api signs. Its config is all it has.
export default defineConfig({ name: "fixture-references-files", kind: SchemaKind.Bucket, outputs: {} });
