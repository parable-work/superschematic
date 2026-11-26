import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { NotesDb } from "@notes/notes-db";

// The notes app's API, served in TypeScript over notes-db. It lists the
// runtime provider packages it may use (D159): the build reads each one's
// package.json and checks it against its interface's contract, and the
// provider slots of NotesApiConfig name one of them per environment.
export default defineConfig({
  name: "notes-api",
  kind: SchemaKind.API,
  dependencies: [NotesDb],
  providers: [
    "../../../providers/sms-relay-a",
    "../../../providers/sms-relay-b",
    "../../../providers/vault-broker",
    "../../../providers/local-sandbox",
    "../../../providers/stub-model",
    "../../../providers/text-preview"
  ],
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
