import { NotesApi } from "@notes/notes-api";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A second API of the binary, with a server of its own: it counts the
// notes by calling notes-api through notes-api's Rust SDK. In the binary
// the call never leaves the process: the SDK's transport hands it to
// notes-api's handle, with the loopback credential of this API's server,
// which notes-api's service clause admits (D116; docs/stack-model.md,
// section 9.9).
export default defineConfig({
  name: "notes-digest",
  kind: SchemaKind.API,
  calls: [NotesApi],
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "RUST" }
  }
});
