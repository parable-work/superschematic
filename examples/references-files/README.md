# references-files

An issue tracker whose rows point at one another and hold files (D154):
references from any row to any row, the backlinks of a row across every
table, files uploaded straight to a bucket, and a file filed onto another
row with no second upload.

| Path | What it is |
|---|---|
| `schemas/services/issues-db` | DB, Postgres: `Issue` keeps a conversation (`Comments`), references to rows of any table (`@references`) and files of at most 25 MiB (`@attachments`); `Note` is an `@audience` table whose references point at issues and notes alone and whose files are documents and images; `ReferenceCleared` and `FileReleased` are the queues a target's delete and a file's release enqueue on, whose fields the build fills |
| `server/server.ts` | hand-written routes over the generated repositories and a bucket: ask for an upload, complete it, post a file on an issue's thread, file it onto a note, list a row's files and download one, a row's backlinks, and the `FileReleased` handler a worker runs |

## What it shows

- A browser uploads a file straight to the bucket through a URL the API
  signs, so its bytes never pass through the server. Completing the upload
  reads the object back as it streams and holds it to the size and
  SHA-256 the upload declared.
- A message on an issue's thread carries the file as an attachment block,
  `attachment:<id>`, which `Comments` holds to the config's schemes, media
  types and size.
- Filing the file onto a note copies its row, not its bytes: both rows
  name one object. The issue then points at the note, so the note lists
  the issue among its backlinks. A ref block in a message points the same
  way.
- A note kept private by its owner is not in anyone else's backlinks or
  references, and no count gives it away. Its owner hears how many rows
  they cannot see point at it.
- Deleting a row clears the references to it and enqueues one
  `ReferenceCleared` message per row that held one. Deleting a file
  enqueues `FileReleased`, whose handler deletes the object once no file
  names it.

## Where it runs

`TestReferencesFilesExampleLoads` (`internal/loader/rowrefs`) loads the
schema. The same scenario, under the fixture name `fixture-references-db`,
runs end to end against Postgres in `TestReferencesTablesEndToEnd`
(`internal/generator/tsgen`), over a bucket on an R2 binding whose S3 API a
stand-in serves. The HTTP runtime's workerd fixture `uploads` signs, sends
and checks a direct upload to R2 inside workerd.
