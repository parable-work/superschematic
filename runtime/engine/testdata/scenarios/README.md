# Engine scenarios

What the engine did, recorded as data, so every binding of it (D58: the Rust
engine itself, its napi and C ABI bindings, and the servers that link it) can
replay it and hold itself to the same answers. The corpus was first recorded
from the TypeScript engine (`runtime/engine/typescript` before the cutover,
milestone 14), which the Rust engine was held to; since the cutover
`@superschematic/engine` is the Rust engine through its Node-API addon, and
the corpus is recorded from it, through the package's suite on the test
addon (`scripts/engine-napi.sh --js-behaviors`), whose bridge runs the
behaviors the tests write in TypeScript. Each file is the recording of one test
file of the engine's suite, or, as `workqueue-<file>.json`, of the work
queue's (below); each scenario in it is one test, the calls the
test made on the engines it opened and what each call returned or threw.
`../http` holds the same for the HTTP routes, the event stream and MCP.

The files are written by the recorder, `typescript/test/recorder.ts`, never
by hand. With `ENGINE_RECORD=<dir>` set, every engine a test opens through
`test/helpers.ts` (`openTestEngine`, `openEngine`) is a proxy that records
each public call the test makes through it, and at exit each test file's
scenarios are written to `<dir>/scenarios/<file>.json`. `test/corpus.test.ts`
records the curated files below again, under Node.js, and fails when a
committed file is not what the recorder writes:

```
scripts/engine-napi.sh --js-behaviors
cd runtime/engine/typescript && bun run build
UPDATE_ENGINE_CORPUS=1 node --test test/corpus.test.ts   # rewrite scenarios/ and http/; review the diff
ENGINE_RECORD=/tmp/corpus node --test 'test/*.test.ts'   # record every test file, curated or not
```

The recording is Node.js's. A test's name carries `(node)`, the SQLite
module the TypeScript engine ran it on, which the engine, with a SQLite of
its own, keeps as the name of its one run.

## Which tests

The corpus holds the scenarios of 30 test files: every core behavior's
(`core-*.test.ts`) and the engine's own `access`, `compat`, `events`,
`instances`, `list-filters`, `namespaces`, `normalize`, `registry` and
`unique`. `test/corpus.test.ts` lists them (`SCENARIO_FILES`). Every test file
records, but the others are left out of the committed corpus to keep it
small enough to review (about 4 MB):

- the behavior API's files (`behavior-*.test.ts`) and the runner's, retention's
  and the value store's (`runner*.test.ts`, `retention.test.ts`,
  `values.test.ts`) define nearly every behavior they run in TypeScript; D58
  runs the engine's TypeScript suite against the Rust engine, these through
  the test addon's bridge, and a replayer without TypeScript cannot run them;
- `tools.test.ts`, `tools-parity.test.ts`, `behavior-guidance.test.ts`,
  `display.test.ts` and `client-codegen.test.ts` are mostly the describe and
  tools documents, megabytes of them; `../tool_parameters_parity.json` and
  `../client_codegen_parity.json` hold what the generators read;
- `engine-migrations.test.ts` writes old files with SQL of its own; `../sqlite`
  holds a file today's engine wrote;
- `identity.test.ts` is the host's identity service (D50), whose session ids
  come from a random source the recorder does not seed;
- the client's tests (`client*.test.ts`) drive the HTTP API, which `../http`
  records from the HTTP tests.

A scenario whose JSON is longer than 100,000 characters (one that creates
hundreds of instances to reach a limit) is not written; its file names it in
`omitted`.

## The work queue's files

The 15 `workqueue-*.json` files (about 1.8 MB) are the work queue's suite,
`runtime/engine-workqueue/typescript/test`, recorded by the same recorder:
its `test/helpers.ts` opens every engine through `recordOpen` and calls
`recordPackage('workqueue-', workQueueBehaviors)`, which names each file
`workqueue-<file>.json` and says which behaviors are the package's. Its
`test/corpus.test.ts` lists the curated files (`SCENARIO_FILES`) and holds
them to what the recorder writes, through the check the engine's own corpus
test runs (`typescript/test/corpus.ts`); each check rewrites and holds only
its own package's files:

```
cd runtime/engine-workqueue/typescript && bun run build   # after the engine's
UPDATE_ENGINE_CORPUS=1 node --test test/corpus.test.ts     # rewrite scenarios/workqueue-*.json; review the diff
```

Every test file that opens an engine is in the corpus, but three:

- `guidance.test.ts` reads the describe and tools documents; each of its
  scenarios is past 100,000 characters;
- `worker.test.ts` records nothing (`openTestEngine(options, { record: false })`):
  the worker (`@superschematic/engine-workqueue/worker`) is a TypeScript
  runtime that reaches the engine over HTTP on its own timers and the wall
  clock, several handlers at once, so which calls the engine sees, and in
  what order, changes from run to run, and what the tests hold to account is
  the worker, not the engine;
- `sqlite-corpus.test.ts` writes the SQLite corpus (`../sqlite`), opening its
  engine directly.

A replayer reads them as it reads the engine's, with these differences:

- `open`'s `behaviors` names the work queue's behaviors, `Lease`,
  `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget` and `Retries`, in
  the order the test passed them: all seven, or a few beside test behaviors.
  A replayer registers its own implementations of them by name, as it does
  the core's; they are never `testBehaviors`. A test's copy of one that wraps
  a hook or an operation only to count its calls (`queue-exclusion.test.ts`'s
  `counting()`) carries the package's declaration and records as the
  package's.
- The test behaviors are `test/fixtures.ts`'s: `test.Signal`, `test.Gate`,
  `test.Lock`, and a stand-in for Budget's reserve under Budget's own name,
  which one scenario of `workqueue-queue` composes ("with Budget on the type,
  claim reserves ..."), listing `Budget` in `testBehaviors`. A name in
  `open`'s `behaviors` is a test behavior when the scenario's
  `testBehaviors` lists it, and the package's otherwise. An engine that
  registers one also loads its schemas against the open meta-schema
  (`metaSchema: "open"`). No fixture reads state the test sets, so no
  scenario is marked `testHook.*`: what the Budget stand-in hears goes to
  an array the test reads and no behavior does.
- Marked `js`: one scenario of `workqueue-filters` (`args`,
  `storage.transaction`), which creates more jobs than one scan reads in a
  transaction of the file (its four-job scenarios create them one by one, so
  they replay). The test of `workqueue-lease` over a value driver of the
  test's went with the TypeScript engine, whose alone a driver written in
  TypeScript was; its twin over the engine's own driver is recorded.
- `dialect: "sqlite"`: three scenarios of `workqueue-queue` make a copy of
  Queue's stale with `storage.run`, besides the one of `workqueue-filters`.
  The `storage` reads in other scenarios (steps marked `dialect: "sqlite"`)
  name the package's tables and columns, `bhv_lease__directives`,
  `bhv_budget__meters`, `engine_instances.bhv_queue__status`, which a second
  implementation keeps, as `../sqlite` holds it to.

## The commerce package's files

The `commerce-*.json` files are the commerce package's suite (D70),
`runtime/engine-commerce/typescript/test`, recorded by the same recorder:
its `test/helpers.ts` opens every engine through `recordOpen` with the
package's behaviors named and calls `recordPackage('commerce-')`, and its
`test/corpus.test.ts` holds them to what the recorder writes, through the
same check, which rewrites only its own package's files:

```
cd runtime/engine-commerce/typescript && bun run build   # after the engine's
UPDATE_ENGINE_CORPUS=1 node --test test/corpus.test.ts     # rewrite scenarios/commerce-*.json; review the diff
```

`open`'s `behaviors` names the package's behaviors, `Stock`, `Calendar`
and `Holds`, which a replayer registers by name after the core's; the
files define no test behaviors. Every scenario runs on a fixed clock, and
`commerce-holds-timers` runs the runner (`runner.runDue`), whose Timers
deadline moves a checkout out of `held`. `commerce-calendar` holds a
desk's time in Europe/London, so a replayer's wall times come from the
same IANA database (2026e, which the crate bundles).

## A file

```json
{
  "name": "core-comments",
  "description": "Comments, the core's comments on an instance: ...",
  "source": "runtime/engine/typescript/test/core-comments.test.ts",
  "omitted": ["..."],
  "shared": {"s1": {...}},
  "scenarios": [{"name": "...", "behaviors": [...], "principals": {...}, "steps": [...]}]
}
```

| Member | What it holds |
|---|---|
| `name` | The test file's name without `.test.ts`, and the file's. |
| `description` | The test file's leading comment: what its tests cover. |
| `source` | The test file, from the repository's root. |
| `omitted` | The names of the scenarios past 100,000 characters, in the test file's order; absent when none. |
| `shared` | Values that appear more than once in the file's scenarios, each written once (below); absent when none. |
| `scenarios` | One per test that called the engine, in the order the tests ran. |

### Shared values

A value inside a step (an argument, an expect, one of their members, at any
depth) whose JSON is at least 400 characters long and that appears more than once
in the file is written once, in `shared`, and in each place as
`{"$shared": "s<N>"}`, numbered by first appearance: a schema document each
test defines, a describe document a test reads twice. Before anything else, a
replayer replaces every such object with `shared[N]`; a shared value holds no
reference itself.

## A scenario

| Member | What it holds |
|---|---|
| `name` | The test's full name (`describe > test`), as `node:test` gives it. Names need not be unique: a test written in a loop records once per pass. |
| `behaviors` | The behaviors the schemas it defines compose, by name, sorted; absent when none. |
| `testBehaviors` | The behaviors the test defined in TypeScript (`test/behavior-fixtures.ts`, `test/runner-fixtures.ts`, `test/reach-fixtures.ts` and the tests' own `defineBehavior` calls) that its answers depend on: one a schema composes or a step registers, or every one the engine registered when a step reads the registered behaviors (`tools.listBehaviors`, `tools.describeBehavior`, `behaviors.names`, `behaviors.has`, `behaviors.declaration`). Sorted; absent when none. A replayer that has not implemented each one skips the scenario; one that has implements each as it acts with none of its hooks set (`js` below). |
| `js` | Why only the recording's own host, `@superschematic/engine`'s suite with the functions and behaviors its tests wrote in TypeScript, can replay it, sorted; absent when nothing stops another (below). |
| `dialect` | `"sqlite"` for a scenario that writes the file with SQL of its own (`storage.run`, `storage.exec`, `storage.transaction`): a replayer on Postgres skips it. |
| `principals` | The principals the scenario acts as, by name. |
| `steps` | Its steps, in the order the test made them. |

A principal's name is its subject; another principal with the same subject
(other permissions, a service beside it) is `<subject>#2`, `#3`, and on, in
order of first appearance. A step's `principal`, and a principal in a policy
question, is such a name.

`js` lists:

| Reason | What it marks |
|---|---|
| `args` | An argument JSON cannot hold (a `$` value below): a test of what the engine refuses. |
| `result` | An answer JSON cannot hold: `schemas.validator`'s validator, `events.watch`'s unwatch function. |
| `options` | An open option JSON cannot hold. |
| `policy` | A policy whose answer is not a boolean, or whose answer to one question changed. |
| `error`, `caller` | An error's detail, or an HTTP caller, JSON cannot hold. |
| `permissionMatcher` | The engine's `permissionMatcher` option, a function. |
| `values.driver` | The value store's driver, a function. |
| `storage.transaction` | A step that runs a function in a transaction of the file. |
| `events.watch` | A step that watches the log with functions. |
| `testHook.<name>.<member>` | A test behavior read a hook the test had set (below): `testHook.probe.react` for a closure test.Ledger's reactions ran, `testHook.keep.ref` for the ref test.Keep's guard compared a discard with. |

A step marked `js: true` is one of those, or a step during which a behavior
read a hook the test had set.

A hook is state a test behavior reads that the test sets as it runs, which
the recording does not carry: `test/runner-fixtures.ts`'s `probe`, whose
closures test.Ledger's reactions and schedule and test.Pacer's schedule run,
or a value a test's own behavior compares a request with. A fixture declares
its hooks with the recorder's `testHooks(name, hooks)`, and what it declares
them holding is what the behavior does when the test sets nothing, which is
what a replayer's own implementation of the behavior does. When a behavior
reads a hook whose value is not the declared one (a closure the test put
there, a value it changed; a function compares by identity, anything else by
its JSON), the scenario is marked. A hook the test set but no behavior read,
because nothing ran it, marks nothing. `test/recorder.test.ts` holds the
recorder to this.

## Steps

```json
{"call": "open", "as": "e1", "args": [{"path": "{{dir:1}}/engine.db", "policy": "allowAll", "ids": "seeded", "clock": "fixed"}], "now": 1000},
{"call": "instances.invoke", "principal": "alice", "args": ["Review", "r1", "comment", {"body": "Looks right."}], "expect": {"id": 1, "body": "Looks right.", "createdBy": "alice", "createdAt": 1000}},
{"call": "instances.invoke", "principal": "bob", "args": ["Review", "r9", "comment", {}], "error": {"class": "EngineError", "code": "not_found", "message": "..."}}
```

| Member | What it holds |
|---|---|
| `call` | `open`, or the method called: `<component>.<method>` (`instances.create`, `schemas.define`, `runner.runDue`) or the engine's own `search` and `close`. |
| `engine` | The engine called, by its `as`; present only in a scenario that opened more than one. |
| `as` | On `open`: the engine's name, `e1`, `e2`, and on. |
| `principal` | For a method whose first parameter is the principal: the principal's name in `principals`, or `{"$given": value}` for a value that is not an object with a subject (a test of the engine's refusal). |
| `args` | The arguments after the principal, as JSON (values JSON cannot hold below), without trailing absent ones; absent when none. |
| `expect` | What the call returned, as JSON. A step with neither `expect` nor `error` returned nothing (`undefined`). |
| `error` | What it threw: `class` (the error's name: `EngineError`, `SchemaDocumentError`, `BehaviorVetoError`, `TypeError`, ...), `code` (an `EngineErrorCode`, absent for an error without one), `message`, and `details`, the error's other own members (`issues`, `changes`, `fields`, `behavior`, `action`, `reason`, `vetoCode`, `vetoDetails`, `after`, `floor`, `head`, `path`, `bytes`, `maxBytes`), absent when none. |
| `now` | For an engine with a fixed clock: the time the clock read during the step, in epoch milliseconds, when it read one time and that time differs from the last `now` of the engine. A replayer sets the engine's clock to it before the step. |
| `clock` | For a fixed clock that read several times during the step (a test clock that counts up each time it is read): the times, in order. A replayer's clock returns them in order, then the last. |
| `ids` | For an engine opened with `ids`: the ids the engine drew during the step, in order. A replayer's id source returns them in order. |
| `dialect` | `"sqlite"` for a step whose answer is SQLite's: a call of Search's `search` or `similar` operations, the search across schemas (`search`), or a tool call of one, which rank with FTS5's bm25; and a read through the file's own SQL (`storage.get`, `storage.all`). On Postgres, a replayer makes the call but does not compare its answer, and skips a `storage` read. |
| `js` | `true` for a step only the recording's own host can run. |

The methods a step calls are the components' of `Engine` (`typescript/src/native/engine.ts`):
`namespaces`, `schemas`, `instances`, `events`, `tools`, `values`, `runner`,
`behaviors` (`register`, `has`, `names` and `declaration`) and `storage` (`exec`,
`run`, `get`, `all` and `transaction`, each `dialect: "sqlite"`). A call the
engine makes inside another, a call that a function the test put in place of
an engine method makes (the call of the method itself is a step), and a call
an app makes while it serves a request are not steps. `behaviors.register`'s argument is
`{"behavior": "<name>"}`, the implementation's declared name.

### Open

`open` opens an engine. Its one argument is the options the test gave, with
what is not JSON named:

| Option | Written as |
|---|---|
| `path` | The file, its temporary directory as `{{dir:N}}`. Two opens of one path are the same file: the second reopens what the first wrote. |
| `policy` | `"allowAll"`; `"permission"` for `permissionPolicy()`; or `{"answers": [[question, answer], ...]}` for a policy the test wrote: every question the engine asked it, once, in the order first asked, with its answer. A question is the engine's `AccessRequest` with its principal by name. A replayer's policy answers a question from the table, and fails the scenario on one the table does not hold. Absent when the test gave none (a test of that refusal). |
| `metaSchema` | `"open"` for the core meta-schema with behaviors let through (`openMetaSchema()` in `test/behavior-fixtures.ts`: `$defs.TypeDef.properties.behaviors.maxItems`, `$defs.BehaviorRef.properties.name.enum` and `$defs.BehaviorRef.allOf` removed from `ir/typescript/schema-file.json`); otherwise the meta-schema itself. Absent for the core registry's. |
| `behaviors` | The names of the implementations the test registered beside the core's. |
| `ids` | `"seeded"` when the test gave none, `"fixed"` when it gave its own (each step lists what it drew). |
| `clock` | `"wall"` when the test gave none, `"fixed"` when it gave its own (steps carry `now` or `clock`). |
| `permissionMatcher` | `"custom"`. |
| `values` | As given, its `driver` as `"custom"`. |
| `namespaces`, `runner`, `retention`, `tools`, `busyTimeoutMs` | As given. |

The SQLite adapter (`driver`) is not recorded.

## Symbols

What differs from run to run is written as a symbol, a string in a place the
recorded value took, or inside a string that held it:

| Symbol | Stands for |
|---|---|
| `{{id:N}}` | An id the seeded generator drew, hyphenated: the N-th distinct one to appear in the scenario. |
| `{{id:N:62}}` | The same id in the version graph's base62 form (Branches' refs and commits). |
| `{{dir:N}}` | The N-th temporary directory to appear. |
| `{{opaque:N}}` | An opaque cursor (base64url) that encodes an id or a time of this run: the N-th distinct one. |
| `{{time:N}}` | In a scenario that read the wall clock: the N-th distinct instant of that clock to appear, read from it or computed from it (a deadline, a retry, a schedule's next run). As a JSON string in place of a number it is that instant in epoch milliseconds; inside a string or in place of one, an RFC 3339 date-time of it. A fixed clock's times are never symbols. |
| `{{time:N:us}}` | The same instant in epoch microseconds. |

A replayer binds a symbol at its first appearance in an `expect` or an
`error`, to what its own engine returned there, and from then on writes the
bound value wherever the symbol appears, in arguments too, and requires it
wherever it is expected. A symbol inside a longer string (`"behavior Links
vetoes create of Task {{id:1}}: ..."`) matches the text it stands for. Two
different symbols of one kind never stand for one value.

**Ids.** The recorder replaces `crypto.randomUUID`, which the version graph
and the HTTP runtime draw from, and the id generator of an engine opened
without `ids`, by one generator seeded at the start of each test: the n-th id
a test draws, from 1, is the version-4 UUID whose 16 bytes are the first 16
of SHA-256(`superschematic-engine-scenario/<n>`) with the version nibble set
to 4 and the variant bits to `10`, written hyphenated in lowercase. So a
recording is the same every time, content hashes over generated keys
included. A replayer that draws its ids from the same generator in the same
order gets the same ids and hashes; one that does not still binds the
symbols, but a hash computed over an id it drew differently will differ.

**Times.** An engine opened without a clock reads the wall clock, but never
the same millisecond twice: the recorder opens it on the engine's strict
clock, each reading `max(now, last + 1)`, every write reading its own, as a
replayer opens one. Across a scenario's engines the recorder keeps the
readings apart as one clock would: a step on another engine than the last
waits until the wall clock is past every instant the scenario has shown.
So two readings are two instants, and an instant that appears twice in a
scenario is one reading, which the symbols keep. In a scenario that read the
wall clock, a number is written `{{time:N}}` when it is an integer in the
wall clock's span, from a day before its first reading in the scenario to
400 days after its last, in epoch milliseconds, or in epoch microseconds
(`{{time:N:us}}`), and the test did not pass that number in an argument; an
RFC 3339 date-time in that span that the test did not pass is written
`{{time:N}}` too, wherever it appears in a string. The span is made of the
wall clock's readings alone, so it begins a day before the scenario was
recorded, at an epoch millisecond count of thirteen digits: no sequence
number, count, version or other small integer falls in it.

A fixed clock's times are never symbols. Its readings are the steps' `now`
and `clock`, written as the clock read them, and they and every time the
engine computes from them are written as recorded, in a scenario that also
opens an engine on the wall clock as in one that does not. So that the two
clocks' times cannot be taken for each other, the recorder refuses a
scenario in which a fixed clock's span (from a day before its first reading
to 400 days after its last) overlaps the wall clock's: the test fails. A
test that opens engines on both gives its fixed clock times far from today
(`7`, `1000`). `test/recorder.test.ts` holds the recorder to this.

## Values JSON cannot hold

An argument or an answer that JSON cannot hold is written as an object with
one `$` member, and marks the step `js`:

| Value | Written |
|---|---|
| An absent argument before a given one, or `undefined` in an array | `{"$undefined": true}` |
| `NaN`, `Infinity`, `-Infinity` | `{"$number": "NaN"}` |
| A `bigint` | `{"$bigint": "<decimal>"}` |
| A function | `{"$function": "<name or anonymous>"}` |
| A `Date` | `{"$date": "<ISO 8601>"}` |
| An instance of another class, a promise, an error | `{"$object": "<class name>"}` |
| A circular reference | `{"$cycle": true}` |

A member whose value is `undefined` is left out, as JSON writes it. A
`Uint8Array`, which a `storage` read returns for a BLOB, is `{"$bytes":
"<hex>"}`; it does not mark the step.

## Replaying

1. Resolve `shared` references.
2. Skip a scenario with `js` (unless the replayer is the recording's own host), a
   `testBehaviors` behavior it lacks, or, on Postgres, `dialect: "sqlite"`.
3. For each step in order: `open` opens an engine on a fresh file per
   `{{dir:N}}` (and reopens it for a path already opened), with the options
   above; a call step calls the method on the engine named by `engine`, or
   the scenario's only one, as the named principal, with the arguments, its
   symbols replaced by their bound values, after setting the clock (`now`,
   `clock`) and the id source (`ids`).
4. Compare: a step with `expect` must return it, a step with `error` must
   throw it (class, code, message and details), after binding symbols. JSON
   objects compare by member, regardless of order; arrays in order; numbers
   by value.
