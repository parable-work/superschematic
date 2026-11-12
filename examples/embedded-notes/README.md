# embedded-notes

A notes app as one binary (D116): an environment on the `embedded`
target, which `superschematic stack build` builds into a file a person
installs. The binary holds two Rust servers, a worker and the site in one
process, and keeps its database in a SQLite file under one data
directory, which it migrates as it starts from the plans its releases
recorded.

| Path | What it is |
|---|---|
| `schemas/services/notes-db` | DB, sqlite first: the `Note` table, and the queue `PinRequested`, which the second release added |
| `schemas/services/notes-api` | API over `notes-db`, served in Rust: five operations marked `@tableOperation`, which the build serves from the database's Rust repositories on SQLite, one behind `@requireService` for another API, one the implementation writes, which enqueues a `PinRequested` in the request's transaction, and the worker `PinNotes`, which handles it |
| `schemas/services/notes-digest` | API with a server of its own: it counts the notes by calling `notes-api` through its Rust SDK, which the binary answers in the process |
| `schemas/services/notes-web` | Site: the page the binary carries |
| `schemas/services/notes-stack` | Stack: the three on one origin, and `Desktop` on the `embedded` target. `src/embedded-target.ts` types the target's values and settings for the schema |
| `schemas/services/notes-stack/plans/Desktop/notes-db/` | the plans the example's releases recorded, which every binary carries |
| `rust/notes-api`, `rust/notes-digest` | the implementations: `notes-api`'s writes the pin request and the worker's method, since the build serves its other operations; `notes-digest`'s makes the call |
| `web/notes-web` | the site's page |
| `schemas/dist/Cargo.lock`, `schemas/dist/bun.lock` | the lockfiles the build is frozen to |

## Build it and run it

```sh
make cli                                   # from the checkout's root
cd examples/embedded-notes
../../bin/superschematic stack build Desktop
```

`stack build` prints the binary's path. It needs cargo and Bun.

```sh
PORT=4170 <the path it printed> --data ./data
```

The binary listens on `127.0.0.1`. `--listen` names another address, and
`--every-interface` listens on every interface.

- `http://127.0.0.1:4170/` is the site's page, from the binary.
- `http://127.0.0.1:4170/api/notes-api/api/notes` is `notes-api`.
- `http://127.0.0.1:4170/api/notes-digest/api/digest` is `notes-digest`,
  which calls `notes-api` in the process with its server's loopback
  credential. `notes-api`'s route for services,
  `/api/notes-api/api/internal/notes`, answers 401 to a browser.

- `POST http://127.0.0.1:4170/api/notes-api/api/notes/<id>/pin-requests`
  writes a message to `PinRequested`. The worker `PinNotes` runs inside
  the binary: the request's commit wakes it, and it pins the note.

Stop it and start it again on the same `--data`: it finds the file at the
model its last plan ends at, applies nothing, and the notes are there.

## Change the schema

Change `notes-db`, then build again. A build that is no release carries
the step from the last recorded plan to the new model and records
nothing. To release it:

```sh
../../bin/superschematic stack build Desktop --release
```

which writes the next plan under `plans/Desktop/notes-db/`, to commit.
The example has two: `0001.json` made the `Note` table, and `0002.json`
added the queue. A binary carries both, and takes a file the first
release wrote forward by the second as it starts.

`superschematic stack dev` runs the environment as one process, with its
file under `schemas/.superschematic/`, migrated from whatever model it
recorded; it needs `superschematic-migrate` on `PATH`.

`cmd/superschematic`'s `TestEmbeddedNotesIsOneBinary` and
`TestStackDevRunsEmbeddedNotesAsOneProcess` build and run it.
