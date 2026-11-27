import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureProvidersDb } from "@schemas/fixture-providers-db";

// An API whose config names a runtime provider per interface (D159, D185):
// the provider crates of examples/runtime-providers/rust, two SMS channel
// adapters of two shapes among them, which a Rust server links.
export default defineConfig({
  name: "fixture-providers-api",
  kind: SchemaKind.API,
  dependencies: [FixtureProvidersDb],
  providers: [
    "../../../../../../examples/runtime-providers/rust/sms-relay-a",
    "../../../../../../examples/runtime-providers/rust/sms-relay-b",
    "../../../../../../examples/runtime-providers/rust/vault-broker",
    "../../../../../../examples/runtime-providers/rust/local-sandbox",
    "../../../../../../examples/runtime-providers/rust/stub-model",
    "../../../../../../examples/runtime-providers/rust/text-preview"
  ],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
