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
from the repository root. The directory must exist. Its
`outputs.tasks.ci` names the CI the tasks run on, when a workflow is
[written from them](#run-the-tasks-on-github-actions).

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
`superschematic new service repo-tasks --kind Tasks` writes one with a
starter task.

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
tracked files of the commit. The core has two, `path` and `schema`, and
the installed binary links four more, one per
[unit of a toolchain](#a-go-module-a-rust-crate-a-js-package-an-image).

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

**`schema`** covers a schema service, by its handle or its name:

```ts
import { ShopApi } from "@acme/shop-api";

inputs: [{ kind: "schema", spec: { service: ShopApi } }, { kind: "schema", spec: { service: "shop-db" } }]
```

A handle is the service's sentinel, imported from its package, so a
wrong name fails in the editor. The task file holds the name either way,
and the import names the service without reading any type of it.

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

It leaves out what a build writes and a tool keeps, wherever it sits
under the root: the directories named `dist`, `node_modules`, `bin`,
`target`, `.venv`, `__pycache__`, `.pytest_cache` and `.ruff_cache`, and
the copy of the dependency graph the naming file's `[deps] copy` names.
They are the names the build cache leaves out of a service's digest, so
a file in one cannot change what a service generates. A lockfile
committed under the output root is one: a build of the generated
workspace reads it, and a build of the schemas does not. Name it as a
`path` input of the task that installs from it.

The set is found from tracked text alone, with no schema loaded. It is
wider than what one build reads and never narrower by design. Give
`root` when the repository has two schemas roots with a service of that
name. [Measure it](#measure-a-schema-inputs-reach) on your own history.

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

`tasks plan --sandbox` [holds the process to these rules](#the-sandbox)
while it resolves.

When a manifest names a path that no tracked file holds, such as a
workspace member a build writes, the resolver reports it. The plan then
requires the task to use an output that covers the path, and fails with
the path, the manifest and the edge to add when it does not.

### A Go module, a Rust crate, a JS package, an image

The official `resolvers` extension, which the installed binary links,
resolves the common units of a repository. Each is named by the
directory of its manifest, from the repository root, `.` for the root
itself.

```ts
inputs: [
  { kind: "go-module", spec: { dir: "go/shop-api" } },
  { kind: "rust-crate", spec: { dir: "crates/shop" } },
  { kind: "js-package", spec: { dir: "web/shop" } },
  { kind: "image", spec: { dockerfile: "docker/api.Dockerfile", context: "." } }
]
```

| Kind | Resolves to |
| --- | --- |
| `go-module` | the module's files, without the modules nested in it; each module a `replace` of its `go.mod` points at by a path; the `go.work` above it, and each module that workspace uses that the module requires |
| `rust-crate` | the crate's files, without the crates nested in it; each crate its dependency tables name by a path, `workspace = true` read from the workspace's own table; each crate a `[patch]` points at; the workspace's `Cargo.toml` and `Cargo.lock`; the `.cargo/config.toml` and `rust-toolchain.toml` above it. A workspace's directory is every member |
| `js-package` | the package's files, without the workspace members nested in it; each member of its workspace it depends on by name; each package it names by `file:`, `link:` or `portal:`; the workspace root's `package.json`, lockfiles and package manager configuration; each tsconfig it extends by a path; each project its tsconfigs reference |
| `image` | the Dockerfile and its ignore file; each file of the context a `COPY` or an `ADD` names; each file a `RUN --mount=type=bind` mounts; less what the ignore file leaves out. The context is the Dockerfile's directory unless `context` names one |

Each follows a unit to the units it names, and those in turn. Each
parses manifests and runs nothing: no `go list`, no `cargo metadata`, no
install.

A manifest often names something a build writes: a `replace` to a
generated Go module, a path dependency on a generated crate, a `COPY` of
a binary. The resolver reports it, and the plan requires the task to
use the output that covers it:

```ts
@task({
  run: ["go", "test", "./..."],
  inputs: [{ kind: "go-module", spec: { dir: "go/shop-api" } }],
  uses: [{ task: SchemaBuild, output: "generated" }]
})
export abstract class GoTest {}
```

Without the `uses`, the plan fails and names the edge to add:

```
task go-test: go/shop-api/go.mod names schemas/dist/orm/shop-db, which no
tracked file of the commit holds. Output generated of task schema-build
covers it: add `uses: [{ task: schema-build, output: "generated" }]`
```

Five cases need a word:

- **A JS dependency on a generated package.** A workspace often lists a
  directory a build writes, as `schemas/dist/types/typescript/*`. A
  dependency whose version is `workspace:` or `*`, and whose name no
  tracked member has, may be one of those packages, and the workspace's
  lockfile says which. The resolver reads `bun.lock`,
  `package-lock.json`, `npm-shrinkwrap.json` and `pnpm-lock.yaml`. A
  name the lockfile resolves to a directory of the workspace is that
  directory, reported as generated when the commit does not hold it. A
  name it resolves to a version of the registry is the registry's. A
  name it does not resolve, and a workspace with no lockfile the
  resolver reads, fall back to a wider rule: the directory of each
  member pattern that names no tracked package is reported as
  generated.
- **A tsconfig's project references.** A referenced directory that
  holds a `package.json` is taken as a package, with what it depends on.
  Any other is taken with the files under it. One the commit does not
  hold is reported as generated.
- **An ignore file.** The image resolver applies the Dockerfile's own
  `<Dockerfile>.dockerignore` when the commit holds one, and the
  context's `.dockerignore` otherwise, as BuildKit chooses. Its lines
  are read as Docker reads them: a pattern from the context's root, a
  matched directory with all under it, `**` across directories, and `!`
  to take files back in, the last matching line deciding. The
  Dockerfile and both ignore files stay in the set whatever the lines
  say.
- **A source with a variable.** `COPY ${DIR} /app` cannot be read from
  the Dockerfile, so the whole context is taken, less what the ignore
  file leaves out.
- **What a unit reads from outside itself and names in no manifest**,
  as `include_str!("../../x")` does, is a `path` input's.

A resolver's set is wider than what one build reads and never narrower
by design. A `COPY --from` another stage, an `ADD` of a URL and a base
image are not the commit's.

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

### The sandbox

```sh
superschematic tasks plan HEAD --file ci/tasks.json --sandbox
```

A resolver reads tracked files, starts no process and uses no network.
`--sandbox` holds the process to that while it resolves. The plan first
reads the commit's tree and starts the one git process that serves file
contents. From then on the kernel refuses the process a new program, a
socket, and any file it would open, create, rename or remove. A
resolver that breaks a rule fails the plan with the kernel's refusal.

The sandbox is a seccomp filter. It works on Linux, on amd64 and arm64,
and the flag fails anywhere else. It needs no privilege. It is a guard
against a resolver that drifts from its rules, such as one that reads a
generated file from the checkout, which would key a task on bytes no
commit holds. It is not a defence against a binary built to escape it:
the plan command is trusted code either way.

The plan with `--sandbox` is the plan without it. Run it in the job
that computes keys.

### What a plan costs

```sh
superschematic tasks plan HEAD --file ci/tasks.json --profile
```

`--profile` writes the plan's cost to standard error: the time to read
the commit, and for each resolver kind the inputs the tasks name, how
many of those differ, the tracked files they hold and the time.

```
tasks plan: 1000 tasks over 12429 tracked files
  read the task file and the tree       146.3ms
  resolve the inputs and key              447ms
    go-module      184 inputs,    16 resolved,    95242 files    207.2ms
    js-package     987 inputs,    82 resolved,     1338 files     15.3ms
    path          1000 inputs,  1000 resolved,    16715 files     34.6ms
    rust-crate     170 inputs,    14 resolved,     1391 files     10.9ms
    schema         659 inputs,    49 resolved,     1358 files     97.5ms
```

Tasks that name one input share one resolution and one digest, so a
plan's cost follows the units a repository has more than the tasks that
name them. On superschematic's own repository, 12,429 tracked files,
with synthetic task files over its 161 real units, a plan took 0.6
seconds for 10 tasks, 0.8 for 100, 1.1 for 1,000, 2.0 for 5,000 and 4.6
for 20,000, of which the last spends 1.4 seconds decoding a 16 MB task
file.

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

## Run the tasks on GitHub Actions

A forge that reads the plan reuses a pass by its key. GitHub Actions is
not one, and it can still run the tasks. The core's `github` renderer
writes a workflow from the task file: a job per task.

```ts
// schemas/services/repo-tasks/schema.config.ts
outputs: {
  tasks: { install: "ci/tasks.json", ci: { github: {} } }
}
```

What is GitHub's and not the graph's is the `github` section: when the
workflow runs, and where the image is pulled from.

```ts
@tasks({
  image: "sha256:6a1f...2918",
  platform: "linux/amd64",
  sections: {
    github: {
      on: { pull_request: { branches: ["main"] }, push: { branches: ["main"] } },
      imageRepository: "ghcr.io/acme/ci"
    }
  }
})
export abstract class Repo {}

@task({
  run: ["scripts/deploy.sh"],
  after: [GoTest],
  hermetic: false,
  sections: { github: { if: "github.event_name == 'push'", timeoutMinutes: 30 } }
})
export abstract class Deploy {}
```

| Section field | On | Meaning |
| --- | --- | --- |
| `on` | `@tasks` | the workflow's triggers, written as GitHub writes `on`. Required |
| `name` | `@tasks` | the workflow's name. The service's name unless given |
| `permissions` | `@tasks` | the workflow's permissions. `contents: read` unless given |
| `imageRepository` | `@tasks` | the repository the tasks' image is pulled from by its digest. Required when a task runs from an image |
| `runsOn` | `@tasks`, `@task` | the runner. Unless given it is the hosted runner of the task's platform: `ubuntu-latest` for `linux/amd64`, `ubuntu-24.04-arm` for `linux/arm64` |
| `timeoutMinutes` | `@task` | the job's timeout |
| `if` | `@task` | the job's condition, written as GitHub writes `if` |

The build writes `<out>/tasks/<service>/ci/github/<service>.yml`, and
`build-all` installs it in `.github/workflows`, or in the directory
`outputs.tasks.ci.github.install` names. The directory must exist.
`tasks --check` holds the workflow current as it holds the task file.

What the workflow does with each part of a task:

| Of a task | In the workflow |
| --- | --- |
| `run` | one step. The argument list is quoted, so the step's shell splits and expands nothing |
| `uses` and `after` | the job's `needs`. GitHub has one kind of edge |
| a paths output | a tar archive, uploaded as the artifact `<task>.<output>`. Each job that uses it downloads it and unpacks it at the repository root before its command starts. A declared output that matches no file fails the job |
| a value or an image output | a job output. The command writes it, one line, to the file `$TASK_OUTPUTS/<output>`. A job that uses it reads the file `$TASK_USES/<task>/<output>` |
| `image` | the job's `container`, as `<imageRepository>@<digest>` |
| `services` | the job's service containers, each by its image |
| `env` | the environment of the step that runs the command |

The renderer refuses what the workflow cannot carry, and names the task:
a command or a variable that holds `${{`, which GitHub would read as an
expression; a platform with no hosted runner and no `runsOn`; an image
with no `imageRepository`; a variable named `TASK_OUTPUTS`, `TASK_USES`
or `TASK_ARTIFACTS`, which the workflow sets.

Three things to know:

- **Every run runs every task.** The workflow keeps no pass by its
  input key. A forge that reads `tasks plan` does that.
- **The file does not say how a command hands a runner a value.** The
  two directories above are this renderer's rule. Another runner may
  have another.
- **A paths output needs `tar` in the image, and `bash` to expand its
  patterns.**

The workflow is a pure function of the task file. It names no release
of superschematic, so an upgrade does not make the committed file stale.

An extension registers a renderer for another CI with `RenderTasks` on
its `CIRendererSpec`, and names it under `outputs.tasks.ci`.

## Measure a schema input's reach

```sh
superschematic tasks reach schemas
```

A schema input's set is wider than what one build reads. How much wider
decides what the input is worth: a set that every commit changes keys
every task that reads schemas on every commit, and reuses nothing.
`tasks reach` measures it on a repository's own history. It walks the
commits that changed a schemas root, back along first parents, and for
each one compares every service's schema input with its parent's.

```
examples/acme-shop/schemas: 43 commits that changed it, back from HEAD (1 more left out: no parent, or the root begins there)
  changed every schema input's digest          13   30%
  changed some                                 23   53%
  changed none                                  7   16%
  changed a file that belongs to no service    12   28%
  median services edited                        1 of 9
  median schema inputs changed                  6 of 9
```

Read it this way:

- **Changed every input** is the cost of the root's shared files. Each
  is in every service's set, so a commit that edits the naming file or
  a shared tsconfig changes them all.
- **Changed some** is the input working: the commit changed the
  services that reach what it edited, and no other.
- **Changed none** is a commit that touched only what a build writes.

A high first line means shared files change often. The numbers above
are from this repository, whose examples edit their naming files in a
quarter of their commits. Before the set left out what a build writes,
the first line was 60 percent: half of it was lockfiles committed under
the output root.

It reads commits through git, loads no schema and runs no build.
`--commits` bounds the walk and `--json` writes every measured commit.

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

- A workflow that reuses a pass by its input key. The github renderer
  runs every task on every run.
- A schema input that reads the naming file by what it says and not by
  its bytes. An edit to the naming file changes every schema input of
  its root, whatever the edit is.
- A JS package whose workspace root is itself a file a build writes.
  The commit holds no workspace above it, so a `workspace:` dependency
  fails the resolver, which says so. Name the package's files with a
  `path` input, and use the output of the task that writes the
  workspace.
- A `yarn.lock` and a binary `bun.lockb` are not read, so their
  workspaces keep the wider rule for a generated member.

[D203](https://github.com/parable-work/superschematic/issues/692) lists
these under Left.
