import { NotesDb } from "@schemas/notes-db";
import { NotesIndex } from "@schemas/notes-index";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// End users read notes through it, and it calls notes-index to index each
// note it saves.
export default defineConfig({
  name: "notes-api",
  kind: SchemaKind.API,
  authDb: NotesDb,
  calls: [NotesIndex],
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true },
    sdk: { [TargetLanguage.Go]: { enabled: true } }
  }
});
