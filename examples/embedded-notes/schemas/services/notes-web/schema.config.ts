import { NotesApi } from "@notes/notes-api";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The notes' page, at web/notes-web, which the binary carries: its built
// files are compiled into it, and it calls notes-api on the page's own
// origin (D91, D116).
export default defineConfig({
  name: "notes-web",
  kind: SchemaKind.Site,
  calls: [NotesApi],
  site: { build: "build", output: "dist", fallback: "index.html" }
});
