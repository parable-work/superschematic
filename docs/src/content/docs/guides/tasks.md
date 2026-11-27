---
title: Tasks
description: Declare the work a commit of your repository needs in a Tasks service. Each task is a command with what it reads and what it writes. The build writes a task file a forge reads, and tasks plan gives every task an input key, so a forge runs only the work it has not already seen pass.
sidebar:
  order: 5.8
---

A Tasks service declares the work a commit of your repository needs: the
builds, the lints and the tests. Each task is a command with what it reads
and what it writes. From those declarations superschematic gives you three
things:

- a **task file**, generated JSON that is committed in the repository, so
  a forge reads the tasks of a commit and runs no compiler;
- an **input key** per task, a hash that names the work the task would do
  at a commit. Two runs with one key did the same work, so a forge that
  finds a trusted pass under a key does not run the task again;
- a **plan**, the tasks of a commit with their keys, in an order that runs
  each task after the tasks it depends on.

The core computes the task graph, the inputs, the key and the plan. When a
task runs, on which machine, within how long, and whose passes count are
your forge's questions. The core holds none of them: they go in a
[section](#sections-what-a-forge-owns) your forge registers.

`examples/acme-schematic` has a Tasks service, `shop-tasks`, a section,
and a test that runs its tasks across commits and reuses work by key.

## Declare the service

A Tasks service is a directory with a schema config, as every service is.
Its `outputs.tasks.install` is the path the task file is committed at,
from the repository root. The directory must exist.

```ts
// schemas/services/repo-tasks/schema.config.ts
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "repo-tasks",
  kind: SchemaKind.Tasks,
  outputs: { tasks: { install: "ci/tasks.json" } }
});
```

Nothing names a Tasks service, so it has no sentinel. A repository may
have several, each with an install path of its own.

## Declare the tasks

A task is an abstract class with `@task`, from `@superschematic/tasks`.
One class may carry `@tasks`, which gives the defaults of every task.

```ts
// schemas/services/repo-tasks/src/tasks.schema.ts
import { task, tasks } from "@superschematic/tasks";

@tasks({
  image: "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918",
  platform: "linux/amd64"
})
export abstract class Repo {}

@task({
  run: ["superschematic", "build-all", "schemas/services"],
  inputs: [{ kind: "schema", spec: { service: "shop-api" } }],
  outputs: [{ name: "generated", paths: ["schemas/dist"] }]
})
export abstract class SchemaBuild {}

@task({ run: ["golangci-lint", "run", "./..."], inputs: ["go", ".golangci.yml"] })
export abstract class Lint {}

@task({
  run: ["go", "test", "./..."],
  inputs: ["go", "go.mod", "go.sum"],
  uses: [{ task: SchemaBuild, output: "generated" }],
  after: [Lint]
})
export abstract class GoTest {}
```

A task's name is its class's in kebab case, `go-test` here, unless `name`
sets it. A class that a task names must be declared above it. The
declarations load from JSON and YAML too, and `format` converts between
the forms.

| Field | What it is | Enters the key |
| --- | --- | --- |
| `run` | The command, an argument list. Nothing starts a shell. A task that wants one says `["bash", "-c", "..."]`. | yes |
| `inputs` | What the task reads of the commit. A string is a path or a glob. | yes |
| `uses` | The outputs of other tasks this one reads. | yes, by the other task's key |
| `outputs` | The named results the task declares. | yes |
| `services` | The service containers the task runs beside, each by its image's digest. | yes |
| `env` | The environment the command starts with. | yes |
| `image` | The digest of the image the task runs from, in place of the default. | yes |
| `platform` | The platform the task runs on, as `linux/amd64`, in place of the default. | yes |
| `hermetic` | `false` for a task whose result its inputs do not decide. | yes |
| `after` | The tasks that must pass before this one starts. | no |
| `sections` | The task's part of each section. | no |

The first nine are the **work fields**. They say what the work is, so
they are what the key covers. `after` orders the plan and `sections`
belongs to a forge, so a change to either changes no key.

An image is named by its digest, never by a tag: a tag names no bytes.
The build refuses a tag for `image` and for a service container.

## Two kinds of edge

A task depends on another in one of two ways, and they are two fields
because they mean two things.

| Edge | Meaning | Enters the key |
| --- | --- | --- |
| `uses: [{ task, output }]` | This task reads a named output of that task. | Yes: that task's key. |
| `after: [task]` | This task starts only when that task passed. It reads nothing from it. | No. |

`GoTest` above reads the generated tree, so it uses `SchemaBuild`'s
output: an edit to a schema changes `SchemaBuild`'s key and so
`GoTest`'s. It only follows `Lint`: an edit to `.golangci.yml` changes
`Lint`'s key and leaves `GoTest`'s as it was, so a forge reuses the test
pass it has.

Write `uses` for what a task reads and `after` for what it only waits
for. An `after` edge written as `uses` costs reuse and nothing else. A
`uses` edge written as `after` is unsound: the key would not cover what
the task read.

## Outputs

An output is a named result a task declares:

- `{ name, paths }`: the files the task writes at those paths, each a
  path or a glob from the repository root;
- `{ name, kind: "image" }`: an image's digest;
- `{ name, kind: "value" }`: a string.

What an output means to a runner:

- On a trusted pass, the runner stores each declared output under the
  task's name and key.
- A task that uses an output gets the bytes stored under the upstream
  task's key, put in place before its command starts, whether the
  upstream task ran in this run or was reused.

So a key that covers an upstream key is sound: the task read exactly what
that key names. A cache, such as a compiler's, is not an output. A task
must give the same result with it and without it.

## Inputs and resolvers

A task names its inputs by kind. A **resolver** turns an input into
tracked files of the commit. The core has two.

**`path`** covers tracked files by path or glob. A string is short for
it:

```ts
inputs: ["go", "docs/**/*.md", { kind: "path", spec: { paths: ["scripts"], exclude: ["**/*.md"] } }]
```

A path that names a directory covers every file under it. `*`, `?` and
`[...]` match within one segment, and `**` stands for any number of
directories. A path with no wildcard that covers no tracked file fails
the plan: it is a slip, or a file a task writes, which a task reads
through `uses`.

**`schema`** covers a schema service by its name:

```ts
inputs: [{ kind: "schema", spec: { service: "shop-api" } }]
```

It resolves to a static set that holds everything a build of the service
may read of the commit:

- the service's own directory;
- the directory of every service of its schemas root that it names, and
  of every service those name in turn. A service names another by the
  other's directory name or package name, in its config, its schema
  files or a sidecar document;
- every tracked file of the schemas root that belongs to no service: the
  naming file, the workspace's manifests and the shared files;
- each file the naming file's `[cache] inputs` lists.

The set is found from tracked text alone, with no schema loaded. It is
wider than what one build reads and never narrower by design. Give
`root` when the repository has two schemas roots with a service of that
name.

A resolver runs before anything is generated: the plan comes first, and
the output root is not tracked. So every resolver follows four rules.
An extension that [registers one](/superschematic/extending/write-an-extension/#task-resolvers-and-sections)
follows them too.

- It reads tracked files only, as the commit holds them.
- It runs no build tool and starts no process. A Cargo resolver parses
  `Cargo.toml` files; it does not run `cargo metadata`, which fails when
  a path dependency has not been generated yet.
- It uses no network and executes nothing the tree holds.
- It reads at most 1 MiB of one file.

When a manifest names a path that no tracked file holds, such as a
workspace member a build writes, the resolver reports it. The plan then
requires the task to use an output that covers the path, and fails with
the path, the manifest and the edge to add when it does not.

## The task file

The build writes the file under the output root, at
`<out>/tasks/<service>/tasks.json`, and `build-all` installs it at
`outputs.tasks.install` on every run: when it built the service, when it
restored it from the cache and when it found it up to date. A missing
install directory fails the build. A build with no repository above it,
such as a Docker stage that copies only the schema tree, logs a line and
installs nothing.

```sh
superschematic tasks schemas/services/repo-tasks          # write one service's file
superschematic tasks schemas/services/repo-tasks --check  # fail when the committed file differs
```

Run `tasks --check` in CI. It writes nothing, and fails when the
committed file is missing or is not what the schema gives, with the
first line that differs.

The file is keys sorted, a two-space indent and a trailing newline. It
is a pure function of the schema: two checkouts at two paths write the
same bytes.

```json
{
  "image": "sha256:6a1f...2918",
  "platform": "linux/amd64",
  "tasks": {
    "go-test": {
      "after": ["lint"],
      "hermetic": true,
      "inputs": [{ "kind": "path", "spec": { "paths": ["go"] } }],
      "run": ["go", "test", "./..."],
      "uses": [{ "output": "generated", "task": "schema-build" }]
    }
  },
  "version": 1
}
```

### Read the file

The shape is defined once, as Go types in the public package
`github.com/parable-work/superschematic/tasks`. A Go reader decodes with
`tasks.Decode`. A reader in another language generates its types from
the JSON Schema, so nobody writes a mirror by hand:

```sh
superschematic json-schema --task-file > task-file.schema.json
superschematic json-schema --task-plan > task-plan.schema.json
```

A reader refuses a field it does not know and a version newer than it
reads, and names it. Three places are open by design: an input's `spec`,
`sections.<name>`, and a task's `sections.<name>`.

The file names the image that holds the plan command, so one change can
move the file's version and the plan command together. Only a forge is
updated out of band, and only when its own section changes.

## The plan and the input key

```sh
superschematic tasks plan HEAD --file ci/tasks.json
```

`tasks plan` reads the task file a commit holds and the commit's tree,
and prints the plan as JSON: each task with its work hash, its key and
its edges, and `order`, which lists every task after the tasks it uses
and follows.

A key covers, in order:

1. the key version, with the repository's object format in it, as
   `1:sha1`;
2. the task's **work hash**: the SHA-256 of its work fields in canonical
   JSON, with the image and the platform in effect;
3. for each input, a digest of the tracked files it resolves to;
4. for each `uses` edge, the upstream task's key.

The digest of an input is the SHA-256 over the sorted lines
`<mode> <path> <object id>` of the files it matches in the commit's
tree. Git already holds those ids, so the plan hashes no file contents.

- The mode is in the line, so an executable bit, a symbolic link and a
  submodule's commit change the key.
- The plan reads the commit and never the working tree. A dirty checkout
  changes nothing, because nothing reads it.
- In a default repository the ids are SHA-1, and the key rests on git's
  hardened SHA-1. A repository of the sha256 object format gives keys of
  another key version, so the two never share a key.
- Replace refs are off, so an id names the bytes it was made from.

Every key is known before any task runs, since an input is tracked files
and an output enters by its task's key. A forge can decide every reuse
at once.

`tasks plan` is the code that must be trusted for keys. Ship it in the
image the task file names, and have your forge accept a plan only from
an image on its own list.

### A change that edits a task

```sh
superschematic tasks plan "$MERGE_COMMIT" --base origin/main --file ci/tasks.json
```

`--base` names the commit the plan's commit lands on. A task whose work
fields differ from the base's is then keyed on both definitions and on
the inputs of both. A change cannot reuse a pass of the task it
replaced, and it cannot pass by narrowing what a task reads.

This has a cost, and it is stated: those keys differ from the keys of
the same tree planned with no base. After the change lands, that task
and each task that uses it runs once more. Narrow work fields and
`after` edges keep the cost small.

### Tasks that are not hermetic

A task is hermetic when its inputs decide its result. One that reads the
network or the clock says `hermetic: false`. It has no key, and its key
in the plan is `null`. A forge never reuses it, and its outputs are that
run's alone.

A hermetic task needs an image and a platform, its own or the default:
without them its tools would be a machine's. A hermetic task may not use
a task that is not hermetic, since its key would not decide what it
read. The build refuses both.

Start a task you have not measured with `hermetic: false`, and let it
earn `true` by a comparison of its results.

## Sections: what a forge owns

A section is a part of the task file that one forge owns and reads. The
core checks each part against the section's schema, copies it into the
file under `sections.<name>`, and reads nothing in it. A reader skips a
section it did not register.

```ts
@tasks({ sections: { forge: { tiers: [{ name: "pr", budgetSeconds: 180 }] } } })
export abstract class Repo {}

@task({ run: ["go", "vet", "./..."], inputs: ["go"], sections: { forge: { tier: "pr" } } })
export abstract class Vet {}
```

There are two ways to register a section.

**As data**, in the naming file, for a forge with no extension:

```toml
# schemas/superschematic.toml
[tasks.sections.forge]
file = "forge/file.schema.json"   # the part @tasks holds
task = "forge/task.schema.json"   # the part @task holds
```

Each is a JSON Schema file, relative to the schemas root. A rule a JSON
Schema cannot say, such as "a task names a tier that exists", is then
your forge's to check when it reads the file.

**As an extension's registration**, which adds a `Verify` in Go for
those rules. See [Task resolvers and sections](/superschematic/extending/write-an-extension/#task-resolvers-and-sections).

Two things belong in a forge and not in the file, because they are trust
and not graph: which images it trusts to compute keys, and whose passes
count for a trigger.

## Check a schema input after a build

```sh
superschematic build-all schemas/services
superschematic tasks inputs schemas/services/repo-tasks
```

A schema input's set is found from tracked text, before a build. A build
records what it read: the services its schema files reference and the
files its documents import. `tasks inputs` compares the two for every
schema input, and fails with each service or file the set at HEAD lacks.
The fix is an input that covers it: another schema input, or a path.

Run it in the task that builds your schemas. A schemas root with no
Tasks service never runs it, so a build changes for no one else.

## What is not built

- Resolvers for a Rust crate, a Go module, a JS package and an image.
  They are extensions; the core ships `path` and `schema`.
- A CI renderer that writes a workflow from a Tasks schema.
- `new service --kind Tasks`.

`docs/DECISIONS.md` lists these under D203.
