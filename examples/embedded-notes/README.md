# embedded-notes

A notes app as one binary (D116): an environment on the `embedded`
target, which `superschematic stack build` builds into a file a person
installs. The binary holds two Rust servers, a worker and the site in one
process, and keeps its database in a SQLite file under one data
directory, which it migrates as it starts from the plans its releases
recorded.

| Path | What it is |
|---|---|
| `schemas/services/notes-db` | DB, sqlite first: the `Note` table, the queue `PinRequested`, which the second release added, and the `Task` table, whose `status` is a Workflow, a behavior the table composes, which the third release added |
| `schemas/services/notes-api` | API over `notes-db`, served in Rust: eight operations marked `@tableOperation`, which the build serves from the database's Rust repositories on SQLite, a task's written through the behaviors' core, one behind `@requireService` for another API; the Workflow's transition, which `@behaviorOperations` serves; one operation the implementation writes, which enqueues a `PinRequested` in the request's transaction; the worker `PinNotes`, which handles it; and an MCP endpoint, whose tools are its table operations and the transition |
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
- `POST http://127.0.0.1:4170/api/notes-api/api/tasks` writes a task
  through the behaviors' core, which starts it `open`, and
  `POST .../api/notes-api/api/task/<id>/status/transition` with
  `{"to":"done"}` moves it by its Workflow.
- `POST http://127.0.0.1:4170/api/notes-api/api/mcp` is `notes-api`'s MCP
  endpoint: its tools are the table operations and the transition, by the
  names their declarations give them (`create_note`,
  `task_status_transition`), and a `tools/call` is a request to the
  tool's route.

A process of your own links the crate's library in place of the binary:
`Embedded::open` opens the data directory, `handle` answers a request,
and each of `workers()` has a `drain` a host with no async runtime calls
on a thread of its own, with the runtime's `block_on` polling the rest.

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
The example has three: `0001.json` made the `Note` table, `0002.json`
added the queue, and `0003.json` the `Task` table. A binary carries them
all, and takes a file the first release wrote forward by the second and
the third as it starts.

`superschematic stack dev` runs the environment as one process, with its
file under `schemas/.superschematic/`, migrated from whatever model it
recorded; it needs `superschematic-migrate` on `PATH`.

`cmd/superschematic`'s `TestEmbeddedNotesIsOneBinary` and
`TestStackDevRunsEmbeddedNotesAsOneProcess` build and run it; the first
also builds a host with no async runtime over the library, which drains
the worker on its own thread.
