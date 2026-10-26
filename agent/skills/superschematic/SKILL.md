---
name: superschematic
description: Build backends with superschematic by declaring them in schema files (.schema.ts), not by writing SQL, migrations, validation, DTOs, routes, auth checks, OpenAPI or clients. The compiler generates Postgres and SQLite DDL, migration plans, a Go ORM, validated servers in Go, TypeScript or Rust, OpenAPI, SDKs in four languages, MCP tools and deploys. Use whenever a project has a superschematic.toml or schema.config.ts, or the task is to add or change a type, table, field, route, permission, user model, job, queue, bucket, memory store, site or environment in such a project, or to start a new one.
---

# superschematic

superschematic is a schema compiler. You describe a service once, in a
schema, and `superschematic build` generates every layer that would
otherwise restate it. Your job in a superschematic project is to **say
what you want in the schema and write only the code the schema cannot
say**.

## The rule

Before you write code, ask whether a declaration says it. It usually
does. You never write, edit or duplicate:

- SQL DDL or migrations: the DB schema is the table; `migrate plan` diffs
  two versions of it.
- Validation: a field's scalar type and `Validate<T, {...}>` are checked
  by every generated decoder, server and SDK alike. Never re-check in a
  handler what a constraint can say.
- Request and response types, DTOs or mappers between layers: the types
  are generated in Go, TypeScript, Python and Rust.
- Routing, argument decoding, 400s, auth and permission checks, rate,
  body and time limits, pagination plumbing, webhook signature checks.
- OpenAPI, SDKs, MCP tool documents, env-var loaders, server entrypoints,
  Dockerfiles, deploy scripts, CI.
- Anything under the output root, `<schemas-root>/dist/`: it is rewritten
  on every build.

You do write:

- each operation's body, against the generated interface (`Constructor`,
  `Implementations`, `Deps` in Go; the same names in TypeScript and Rust);
- ownership checks behind `@requireOwnership`;
- `@virtual` fields of a view, job bodies, worker handlers, and the
  handler of a `@manualRouteRegistration` operation;
- tests.

`superschematic catalog capabilities` maps each thing a backend needs to
its declaration, what is generated and what is left for you. Read it when
you are not sure how to say something.

## The model

A **schemas root** holds `superschematic.toml` (the naming file),
`tsconfig.base.json` and `services/`. Each service is a directory:
`schema.config.ts` (its name, kind and `outputs`) and `src/*.schema.ts`.
Output goes to `<schemas-root>/dist`.

The **kind** decides what a class means:

| Kind | A class is | Imports | Generates |
| --- | --- | --- | --- |
| `General` | a plain type shared by services; with `@envVars`, a service's settings | `@superschematic/schema`, `superscalar` | types, env loader |
| `DB` | a table | `+ @superschematic/db` | SQL, Go ORM, types |
| `API` | a request or response type; with `@source(Table)` a view of a table; a class of methods is an operation set | `+ @superschematic/api` | types, server, OpenAPI, SDKs |
| `Stack` | the stack, a server, a database or an environment | `@superschematic/stack` | entrypoints, `environment.json`, CI |
| `MemoryStore` | with `@channel` or `@key`, a channel or a key of a Redis or Valkey store that replicas share and can lose; its fields are the name's parts | `@superschematic/memory-store`, `superscalar` | typed definitions per language |
| `Bucket`, `Site` | config only: a file bucket, a static front end | | |

Schema classes are `abstract`. An operation's body is
`throw new Error("schema declaration only")`: nothing runs it, the
compiler reads the declaration. An API reaches a DB through `authDb`, a
DB `dependencies` entry or `@source`; another API through `calls`; a
bucket through `buckets`; a memory store through `memoryStores`, and
`rateLimitStore` to share its `@rateLimit` budgets across replicas. A
memory store never holds a fact: every key expires, and the code keeps a
path for when the store is unavailable.

## The loop

1. **Pick the service and kind.** A new table goes in a DB schema, a new
   route in an API schema, a type several services share in a General one.
2. **Edit the schema.** Choose a scalar for every field first
   (`superschematic catalog scalars`): `Contact.Email`, not `string` plus a
   pattern. Then wrappers and decorators (`superschematic catalog
   decorators`).
3. **Check, then build.** From the directory holding `schemas/`:
   ```sh
   superschematic check --json                        # load and verify in tens of milliseconds; writes no code
   superschematic check --watch --json schemas/services  # re-check what each edit affects, one document per line
   superschematic build-all schemas/services          # every service, in dependency order
   superschematic build --with-deps schemas/services/shop-api  # one service and what it needs
   ```
   Check after every schema edit; build when the schemas check clean and
   you need the generated code.
4. **Read the error.** Check and build errors name the field and say the fix, often
   with the decision record behind it (`(D34)`). Change the schema as the
   message says; don't work around it in code. When output surprises you,
   `superschematic build <dir> --emit-ir | jq` shows what the generators were
   given.
5. **Implement what was generated.** `build --scaffold` (or a stack's
   build) writes a missing implementation: Go at `go/<service>`,
   TypeScript at `typescript/<service>`, Rust at `rust/<service>`, beside
   the schemas root. Each method answers 501 until you fill it. Compile
   errors after a schema change point at exactly the code to follow.
6. **Test.** `superschematic stack dev schemas/services/<stack>` runs the
   whole stack locally, with Postgres in Docker and migrations applied. A
   site whose config names `site.dev` runs its dev server there, with its
   APIs under `/api/<service>` on the site's origin; `--build-sites`
   builds it as a deploy does. Run the language's own tests against the
   generated SDKs.
7. **Changing a table that holds data?** `superschematic migrate plan
   schemas/services/<db> --from-ref origin/main` plans expand and contract
   steps with their hazards. Show the hazards to the user.

## Choices that matter

- **Scalars before constraints, constraints before code.** A superscalar
  type brings parsing, normalizing (an email is trimmed and lowercased),
  the check and the SQL type everywhere. `Validate` adds ranges, lengths
  and patterns. Handler code is for rules that span fields or rows.
- **Views, not tables, in responses.** An API returns `@source(Table)`
  classes listing the fields it shows; the loader checks each against the
  table, and `@sourceMustProject` warns when a view drops a field it must
  carry.
- **Permissions on the operation.** `extends Authenticated`, `@auth`,
  `@requirePermission([...])`, `@publicRoute`. For users and sign-in, turn
  on the user model (`implements User<{ login }>` on a DB table,
  `@userSessions` on an API class) instead of writing auth.
- **Background work is declared.** `@job` for schedules, `@queue` on a DB
  class plus `@worker` on an API class for messages written in the same
  transaction as the change that causes them; `key` with `onDuplicate` or
  `serialize` for one waiting or one running message per key;
  `@enqueueOnChange` on a table to enqueue whatever writes its rows; and
  `@deriver` on an API class to fill an engine type's Derived fields from
  a model.
- **Your own HTTP program is an app.** Pages or a back office over a Rust
  API is `@app` on an empty API class: the stack runs the crate at
  `[implementation_paths] rust_app` (`apps/{app}`), whose `serve(parts)`
  gets the API's `Deps`, implementations and engine, beside the API's
  servers. Expose it in `@stack`, or no one reaches it. An
  `@app({ extensions: ["topcoat"] })` builds only with
  `superschematic-topcoat`, which each release ships beside
  `superschematic`: `superschematic` plans the stack and refuses
  `build-all`, `stack dev` and deploys, and the generated CI of every
  stack in that services root runs `superschematic-topcoat`. Topcoat's
  `<service>_with_parts` serves the app's pages alone; the JSON API and
  engine routes need `_with_parts_and_api`.
- **No server at all?** If the data's shape is defined at run time, or
  the need is workflow, comments, revisions, approvals and the like, the
  engine runs a schema with no generated code: types compose behaviors
  with `@behavior("Workflow", {...})` (`superschematic catalog behaviors`).
- **Outputs must line up.** A server or SDK in a language needs
  `outputs.types` in that language, and a DB always needs Go types; the
  config fails to load and says so otherwise.

## Rules worth knowing

Most are load errors; knowing them saves a build round-trip:

- `schema.config.ts` imports only `@superschematic/schema-config` and other
  services' **sentinels** (`import { ShopDb } from "@acme/shop-db"`), never
  a class or a type. A service's first build writes its sentinel to
  `src/service.generated.ts` and exports it from `src/index.ts`; commit
  both.
- A schema file imports only the authoring packages its kind allows (the
  table above), `superscalar` and other services' packages.
- A DB schema cannot import `@superschematic/api`, and an API schema
  cannot import `@superschematic/db`.
- Every plain class in a DB schema is a table; give each one a `@key`.
  A `@jsonField` class, a `@queue` message, a `@projection` and a type
  that composes a behavior are not tables.

## Starting a project

Two commands write the boilerplate; don't write these files by hand:

```sh
superschematic init --superschematic <checkout>      # writes ./schemas
superschematic new service shop-db --kind DB         # from the project or schemas root
superschematic new service shop-api --kind API
```

`init [<schemas-root>]` writes `superschematic.toml` (`--go-module-root`,
`--npm-scope`, and a `[paths]` table pointing the generated packages at
the checkout's runtimes), `tsconfig.base.json` (whose `paths` map
`@superschematic/*` and `superscalar` to the checkout and
`"<npm_scope>/*"` to `./services/*/src/index.ts`), the `.gitignore` for
`dist/` and an empty `services/`. superschematic is pre-release and
nothing is published, so it needs a checkout prepared with `make setup`:
`--superschematic` names it, else the checkout that holds the project.

`new service <name> --kind DB|API|General|Bucket|Site|MemoryStore [--root <schemas-root>]`
writes `services/<name>/`: `schema.config.ts` with an `outputs` block
that loads, `package.json` (`<npm_scope>/<name>`), `tsconfig.json`,
`src/index.ts` and, for DB, API, General and MemoryStore, a starter
`src/<name>.schema.ts` to rename and grow. Then it builds the service
once, which writes `src/service.generated.ts`, so other configs can
import its handle at once. Wire services together afterwards in the
configs (`authDb`, `calls`, `buckets`, `memoryStores`, `rateLimitStore`,
`dependencies`).

Both refuse to replace a file that exists and write nothing then. Once
APIs exist, `superschematic stack init` writes the Stack service.

## Before you deploy

`stack dev` runs on your machine only. `stack bootstrap`, `stack secrets
set`, `stack deploy` and `stack destroy` act on cloud accounts and cost
money. Run `stack plan <environment>` and show it to the user, and run
those commands only when the user asks for that environment.

On the cloudflare target, a DB service that lists `postgres` first is a
Postgres outside Cloudflare (Neon, Supabase, Cloud SQL) that Workers reach
through a Hyperdrive config: the user makes the config, names its id in
the environment's `settings` (`{ of: FinancesDb, hyperdrive: "<id>" }`),
and keeps the database's connection string in `HYPERDRIVE_ORIGIN_<DB>`
for the deploy's migration. Don't make the config or handle the
connection string yourself. Queue workers there must be TypeScript; each
drains its queue once a minute under a Cron Trigger.

## Reference

The binary prints its own references, written from what it links, its
extensions included, so they always match the compiler you build with.
Each is Markdown on stdout; save one to a scratch file or pipe it through
`grep` rather than reading it all:

| Command | What it lists |
| --- | --- |
| `superschematic catalog capabilities` | need → declaration → what is generated → what you write |
| `superschematic catalog scalars` | every scalar: its checks, its SQL type, its type in each language |
| `superschematic catalog decorators` | every decorator and wrapper: its TypeScript declaration, where it may sit, which kinds may use it |
| `superschematic catalog behaviors` | every engine behavior: its config, what it requires, the fields and operations it adds |

The decorator catalog is long. Find one entry with
``superschematic catalog decorators | grep -A 15 '^### `@paginated`'``.

Also:

- `superschematic json-schema` prints the schema the JSON and YAML forms
  of a schema file are checked against, an extension's decorators
  included, and `superschematic --help` lists the commands of the binary
  you run.
- The docs: <https://parable-work.github.io/superschematic/>. The guides
  under `guides/` cover each area; `reference/cli/` lists every command
  and flag.

A binary older than the `catalog` command answers `unknown command
"catalog"`; read the docs site's capability map, scalar and decorator
pages instead, and tell the user the binary predates the command.
