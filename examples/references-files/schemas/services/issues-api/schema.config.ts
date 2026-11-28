import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { IssuesDb } from "@schemas/issues-db";
import { IssuesFiles } from "@schemas/issues-files";

// The issue tracker's API, served in TypeScript over issues-db: an
// issue's conversation, and the references, backlinks and files of issues
// and notes (#723), the files in issues-files. It names no authDb: the
// app's own session reader puts the caller on each request.
export default defineConfig({
  name: "issues-api",
  kind: SchemaKind.API,
  dependencies: [IssuesDb],
  buckets: [IssuesFiles],
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true, language: "TYPESCRIPT", mcp: true },
    sdk: { [TargetLanguage.TypeScript]: { enabled: true } }
  }
});
