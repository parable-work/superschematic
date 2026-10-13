import { NotesApi } from "@schemas/notes-api";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// A static site whose browser code calls notes-api (D55).
export default defineConfig({
  name: "notes-web",
  kind: SchemaKind.Site,
  calls: [NotesApi]
});
