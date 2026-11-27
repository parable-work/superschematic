import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureReferencesDb } from "@schemas/fixture-references-db";

// A Go server with runtime providers (D159, D184) over a database whose
// rows hold references and files (D154). Its config lists the provider
// packages of examples/runtime-providers, each of which holds a Go module
// beside its TypeScript package: the server imports each module, and
// FixtureProvidersConfig's slots name one of them per environment.
export default defineConfig({
  name: "fixture-providers-go-api",
  kind: SchemaKind.API,
  dependencies: [FixtureReferencesDb],
  providers: [
    "../../../../../../examples/runtime-providers/providers/sms-relay-a",
    "../../../../../../examples/runtime-providers/providers/sms-relay-b",
    "../../../../../../examples/runtime-providers/providers/vault-broker",
    "../../../../../../examples/runtime-providers/providers/local-sandbox",
    "../../../../../../examples/runtime-providers/providers/stub-model",
    "../../../../../../examples/runtime-providers/providers/text-preview"
  ],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
