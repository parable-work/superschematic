import { NotesDb } from "@schemas/notes-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Indexes the notes notes-api saves; only notes-api calls it.
export default defineConfig({
  name: "notes-index",
  kind: SchemaKind.API,
  authDb: NotesDb,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true },
    sdk: { [TargetLanguage.Go]: { enabled: true } }
  }
});
