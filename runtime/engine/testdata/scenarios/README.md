# Engine scenarios

What the engine (D58) did, recorded as data: each file is the recording of
one test file of the engine's suite, or of the work queue's
(`workqueue-<file>.json`) or the commerce package's (`commerce-<file>.json`,
D70), and each scenario in it is one test, the calls the test made on the
engines it opened and what each call returned or threw. The engine held its
bindings and servers to them; the behaviors' core (D103) holds itself to
them now. Cases of the core that the engine recorded none of are written by
hand in this format in `../rules/scenarios` (D104).

The engine went (D103, #587), and with it the recorder that wrote these
files, so they are frozen: never re-recorded, and never edited by hand. The
files left are the ones a rules host replays: the reference host's replayer
(`runtime/engine/rust/rules-testkit`, which also writes each recording's
transcript, `../rules/<name>.transcript.json`, that every adapter replays),
development mode's access replay (`runtime/engine/rust/rules-dev/tests/access.rs`,
`access.json`), and `core-reactions-holds-revised.json`, the answers of
Reactions' holds and revised that the core has yet to port. The recordings
of what the engine alone ran (its namespaces, registry, events, unique
keys, list filters and compatibility rules, the behaviors D103 folds into
schema features, Links, Search, Queue and Branches, and the work queue's
filters and package) went with it.

`open`'s `behaviors` names the behaviors a recording's engine registered
beyond the core's, the work queue's (`Lease`, `Assignment`, `Queue`,
`Presence`, `Blueprint`, `Budget` and `Retries`) or the commerce package's
(`Stock`, `Calendar`, `Holds` and `Payments`), in the order the test passed
them; a replayer registers its own implementations of them by name, and a
name the scenario's `testBehaviors` lists is a test behavior instead. The
test behaviors are the test files' (`test.Signal`, `test.Gate`, `test.Lock`
and the like), which the replayer ports (`rules-testkit/src/fixtures.rs`).
`commerce-calendar` holds a desk's time in Europe/London, so a replayer's
wall times come from the same IANA database (2026e, which the crate
bundles).

A scenario whose JSON was longer than 200,000 characters (one that creates
hundreds of instances to reach a limit) was not written; its file names it
in `omitted`.

## A file

```json
{
  "name": "core-comments",
  "description": "Comments, the core's conversation on an instance (D77, batch 1): ...",
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
| `omitted` | The names of the scenarios past 200,000 characters, in the test file's order; absent when none. |
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
{"call": "instances.invoke", "principal": "alice", "args": ["Review", "r1", "getMessage", {"message": 1}], "expect": {"id": 1, "author": "alice", "state": "posted", "body": "Looks right.", ...}},
{"call": "instances.invoke", "principal": "bob", "args": ["Review", "r9", "post", {"body": "x"}], "error": {"class": "EngineError", "code": "not_found", "message": "..."}}
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
| `dialect` | `"sqlite"` for a step whose answer is SQLite's: a call of Search's `search` or `similar` operations, the search across schemas (`search`), or a tool call of one, which rank with FTS5's bm25; a call of Comments' `searchMessages` (`behaviors.invoke`, or its tool), whose order and snippets are each database's own; and a read through the file's own SQL (`storage.get`, `storage.all`). On Postgres, a replayer makes the call but does not compare its answer, and skips a `storage` read. |
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
| `{{time:N}}` | In a scenario that read the wall clock: the N-th distinct instant of that clock to appear (of an engine's database, when its engines opened more than one), read from it or computed from it (a deadline, a retry, a schedule's next run). As a JSON string in place of a number it is that instant in epoch milliseconds; inside a string or in place of one, an RFC 3339 date-time of it. A fixed clock's times are never symbols. |
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
readings apart as one clock would: engines on different databases number
their instants apart, so two engines that read one millisecond have two
symbols for it, and a step on another engine than the last waits until the
wall clock is past every instant the scenario has shown, which keeps apart
the readings of engines on one database (one reopened on another's file).
So two readings are two instants, and an instant that appears twice in a
scenario is one reading, which the symbols keep. A replayer whose engines
each read a strict clock of their own waits the same way, before a step on
another engine than the last and before it opens one, whatever their
databases, until the wall clock is past every instant the last one read:
without the wait a second engine can read an instant the first has read,
and two symbols stand for one value. In a scenario that read the
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
