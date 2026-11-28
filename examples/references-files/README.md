# references-files

An issue tracker whose rows point at one another and hold files (D154):
references from any row to any row, the backlinks of a row across every
table, files uploaded straight to a bucket, and a file filed onto another
row with no second upload. The API serves them as generated operations
(#723): routes, SDK methods and MCP tools. The guide's
[References and files on any row](https://parable-work.github.io/superschematic/guides/database-tables/#references-and-files-on-any-row)
section covers the tables, and
[A table's references and files](https://parable-work.github.io/superschematic/guides/api-routes/#a-tables-references-and-files)
the routes.

| Path | What it is |
|---|---|
| `schemas/services/issues-db` | DB, Postgres: `Issue` keeps a conversation (`Comments`), references to rows of any table (`@references`) and files of at most 25 MiB (`@attachments`); `Note` is an `@audience` table whose references point at issues and notes alone and whose files are documents and images; `ReferenceCleared` and `FileReleased` are the queues a target's delete and a file's release enqueue on, whose fields the build fills |
| `schemas/services/issues-files` | Bucket: the files, which a browser uploads straight to through URLs the API signs |
| `schemas/services/issues-api` | API, TypeScript: `IssueActions` serves the issue's conversation (`@behaviorOperations`); `IssueOperations` and `NoteOperations` serve each table's references, backlinks and files (`@rowFeatureOperations`), a note's but their removal |
| `server/server.ts` | what the app adds: its session reader as the router's caller, the repositories and the bucket, and the `FileReleased` handler a worker runs |

## What it shows

- A browser asks for an upload (`POST /api/issue/{id}/files`, with the
  file's name, media type, size and SHA-256), PUTs the bytes straight to
  the bucket through the URL it gets back, so they never pass through the
  server, and completes it (`POST /api/issue/{id}/files/{file}/complete`),
  which reads the object back as it streams and holds it to the size and
  SHA-256 the upload declared.
- A message on an issue's thread (`POST /api/issue/{id}/comments/post`)
  carries the file as an attachment block, `attachment:<id>`, which
  `Comments` holds to the config's schemes, media types and size.
- Filing the file onto a note (`POST /api/issue/{id}/files/{file}/file`,
  `{ "to": { "table": "Note", "key": "..." } }`) copies its row, not its
  bytes: both rows name one object. The issue then points at the note, so
  the note lists the issue among its backlinks
  (`GET /api/note/{id}/backlinks`). A ref block in a message points the
  same way.
- A note kept private by its owner is not in anyone else's backlinks or
  references, and no count gives it away. Its owner hears how many rows
  they cannot see point at it.
- The SDK has a method for each route (`sdk.issue.beginUpload(id, ...)`,
  `sdk.note.backlinks(id)`), and the MCP endpoint a tool for the reads and
  the add of a reference (`issue_backlinks`, `issue_add_reference`); an
  upload stays a route.
- Deleting a row clears the references to it and enqueues one
  `ReferenceCleared` message per row that held one. Deleting a file
  enqueues `FileReleased`, whose handler deletes the object once no file
  names it.

## Where it runs

`TestReferencesFilesExampleLoads` (`internal/loader/rowrefs`) loads the
database and the API, and `TestExamplesCheck` (`cmd/superschematic`)
checks and builds the example. The same scenario runs end to end against
Postgres through the generated routes, under the fixture names
`fixture-references-db` and `fixture-references-api`: in TypeScript in
`TestGeneratedRowFeaturesEndToEnd` (`internal/generator/tsrestgen`), over
a bucket on an R2 binding whose S3 API a stand-in serves, and in Go in
`TestRowFeaturesGoServerEndToEnd` (`internal/generator`). The HTTP
runtime's workerd fixture `uploads` signs, sends and checks a direct
upload to R2 inside workerd.

A Rust server keeps each of these operations a method of the
implementation's trait for now (#723).
