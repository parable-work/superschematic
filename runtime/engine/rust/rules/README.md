# The core

`superschematic-rules` is the pure core of the behaviors (D103 in
`docs/DECISIONS.md`). A behavior adds fields, sub-tables, operations and
rules to a table that stays a table; its rules are written once, in Rust,
here, and every language embeds them through a binding. The core does no
I/O: it opens no connection and no file, reads no clock and makes no id. It
takes a type described as data, a command with its caller, its time and the
host's id for the request, and the answers the host has read for it so far,
and returns one step: the reads it needs, the lock keys it needs, or its
outcome, a refusal or a change set. The host applies a change set in the
caller's own transaction, beside its other writes.

It builds for `wasm32-unknown-unknown` as for any host target; its only
dependencies are `serde` and `serde_json`, with the engine's JSON features (a
number keeps its text, an object its members' order). Every value that
crosses a binding (`TypeSpec`, `Command`, `Answers`, `Step`) reads and writes
as JSON.

`superschematic-rules-behaviors` (`../rules-behaviors`) holds the behaviors
ported onto it; `superschematic-rules-testkit` (`../rules-testkit`) is its
reference host, an adapter over memory, and the replayer that holds the core
to the recorded scenarios (`runtime/engine/testdata/scenarios`). The engine
(`../engine`, D58) stays beside them until the order of the work in D103
removes it.

```rust
use superschematic_rules::{Action, Caller, Command, Core, TypeSpec, UseSpec};

let core = Core::new().with(Workflow);           // the behaviors a project composes
let order = TypeSpec::new("Order")
    .with(UseSpec::on_field("status", "Workflow", status_config));
let ship = Command::new(
    Caller::new("sam", ["orders.ship"]),
    now_ms,
    Action::Operation {
        key: "o1".into(),
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({ "to": "shipped" }),
    },
)
.with_request(request_id);                       // the host's, kept for every run
let outcome = core.drive(&order, &ship, &mut source);   // or the host's own loop, below
```

## The type

A type is its name and its behavior uses in list order (`TypeSpec`). A use
(`UseSpec`) is a behavior's name, the config the schema gives it (`None`
when it gives none, which the core checks as `{}`; a config given as JSON
null is given), and its anchor: the whole type (`Anchor::Type`, written
`implements X<Type, Config>`) or one field (`Anchor::Field`, the field's
type is the behavior, `status: Workflow<{ ... }>`). One type may carry a
behavior once on the type and once on each of any number of fields. A use's
id is the behavior's name on a type anchor and the field's name on a field
anchor; the core addresses a use's fields, sub-tables, operations and create
parameters by it, and refuses a type whose ids repeat. A field anchor's field
is the behavior's value (Workflow's `status`), which its declaration names
(`Declaration::value`); the core refuses a field anchor on a behavior that
names none, and refuses a create or an update whose own fields name a field
anchor's field, to set it or to clear it, since only the behavior's
operations change it (`invalid_instance`, rule `owned`). A type anchor's
fields sit apart from the own fields, which may take any name.

## The host loop

1. Build the command (`Command::new`, since `Command` may gain members):
   the caller (`Caller`: subject, permissions, and `stands_in` for a service
   standing in for an end user, which holds none), the time in epoch
   milliseconds, the action, and the host's id for the request
   (`with_request`), unique per command and the same for every run of it,
   which the keys a behavior makes are derived from. A create carries the id
   the host will write, the caller's or one the host made; `None` leaves the
   key to the host's layout.
2. Start with no answers (`Answers::new()`) and call `Core::run(type,
   command, answers)`.
3. `Step::Read(requests)`: read each through the caller's transaction, add
   it (`answers.answer(request, answer)`), and run again.
4. `Step::Lock(keys)`: take each lock, record it (`answers.hold(key)`), and
   run again. Each answer records how many locks the host held when it made
   the read (`Answered::held`), and the core decides on none made before the
   latest lock: it asks for those reads again, so every read a rule across
   rows decides on is made under its locks, and a host has nothing to
   forget. A host that writes the answers as JSON writes `held` with each;
   one it leaves out is 0, an answer made before any lock.
5. `Step::Done(Outcome::Refused(refusal))`: answer the caller with it.
6. `Step::Done(Outcome::Changed(changes))`: apply the change set in the
   transaction, all of it or none. When any row it writes is not at the
   version the change set expects, roll back, clear the answers and run the
   command again from fresh state, with the same request id, a bounded
   number of times, then refuse it as `busy` (D58's code, `Code::Busy`). A
   deadline command runs only while its deadline is still armed at the time
   the command names (`Action::Deadline`'s `at`), checked before each run
   and in the apply, which spends it. Return `changes.result`.

The core is a pure function of its inputs: the same type, command and
answers give the same step, and running again from the top is how a run
resumes. An asynchronous host (Node.js's pg, Bun) and a synchronous one
answer alike. `Core::drive(type, command, source)` is steps 2 to 6 but the
apply, for a host whose reads return at once (`Source`: `answer(request)`
and `lock(key)`, each of which may refuse the command instead, a host's
own failure as `Refusal::internal`); it refuses a run still asking after
`MAX_ROUNDS` runs as `internal`.

A host that meets a kind of read or write it does not take (the enums are
open, below) or cannot take a lock refuses the command itself:
`Refusal::internal(..)`
(`internal`, 500, the class `Error`) for a failure of its own, `busy` for a
lock it waited too long on. Neither is a behavior's defect nor the caller's
refusal.

## Commands

`Action` is one of the following. The list is open: an event from the outbox
joins it with the behaviors that react to one (D103's order of the work, step
3), so a host matches it with a fallback arm.

- `Define`: every use names a behavior the core runs, the ids are distinct,
  and each behavior parses its config (the one given, or `{}`). The check
  reports the uses and the configs' shapes first, and the configs' rules
  only when every use and shape is sound, as a schema document's
  validation runs before its behaviors' checks. Every other command makes
  the same check first, so a type whose configs fail is refused whatever it
  is asked.
- `Publish { from }`: the define check, then each use's config change from
  the published version `from` (`None` for a first version), paired by use
  id and behavior. A changed config asks the behavior's `config_change`; an
  added or a removed use asks it only when the type has rows (one
  `HasRows` read, asked when first needed). A use whose id the old version
  gave another behavior is a removal and an addition. A config given as
  `{}` and one not given are one config, and a refusal prints each config
  with its members sorted, as the engine printed its canonical document's,
  however the host ordered them.
- `Create { id, own, params }`: own fields (the author's), each use's create
  parameters by use id, and the key. An entry names a use whose behavior's
  declaration takes create parameters; an entry for any other use is
  refused (`behavior Workflow takes no create parameters`), `{}` included.
  An own field that names a field anchor's field is refused.
- `Update { key, patch }`: a merge patch (RFC 7396, `merge_patch`) of own
  fields. A member that names a field anchor's field is refused, null
  included.
- `Delete { key }`.
- `Operation { key, use_id, operation, params }`: one use's declared
  operation.
- `Deadline { key, use_id, name, at, payload }`: a deadline a use armed that
  fell due, with the time it was armed to fall due at and the payload it was
  armed with. The host runs it only while the deadline is still armed at
  `at`, so one cleared or moved since does not fall due on its old arming.

A command on a row reads it first (`ReadRequest::Row`); a key the type has
none of is `not_found`. Then the uses compose in list order:

| Command | In order |
|---------|----------|
| create | the create parameters (an entry naming no use, an entry for a behavior that takes none, and each `check_create_params` of one that takes them, with its entry or `{}`), the own fields that name a field anchor's field and every `validate`, every `guard`, every `initialize`, every `after_change` |
| update | the patch's members that name a field anchor's field and every `validate` (own fields before and after the patch), every `guard`, every `after_change` |
| delete | every `guard`, every `after_change` |
| operation | the use's `check_params`, every `guard`, its handler; for a writing one, every `after_change` |
| deadline | the use's `deadline` handler, every `after_change` |

Validation issues from every use refuse together (`invalid_instance`); the
first veto refuses (`vetoed`); the first refusal of any kind ends the run.

## The read exchange

A `ReadRequest` names logical targets: `Row { type, key }` (answered
`Answer::Row(Option<Row>)`, a row's own fields, its owned fields by use id
and field, and its version), `HasRows { type }` (`Answer::HasRows(bool)`)
and `Spec { type }` (`Answer::Spec(Option<TypeSpec>)`, a type's live version,
which a call on another type's row binds and a behavior that reads another
type's config reads; the core never asks it of the command's own type). The
enum is open: sub-table rows by a condition and counts join it as behaviors
need them, so a host matches it with a fallback arm, which refuses the
command as `internal`. An answer of another kind than its request asks for
is refused as `invalid_argument`.

A hook reads through its context, `ctx.read_row(type, key)?`,
`ctx.has_rows(type)?`, `ctx.read_spec(type)?`, `ctx.config_of(type,
use_id)?` (a use's config as the type holds it, `{}` when it gives none),
`ctx.read(&request)?` or `ctx.read_many(&requests)?` for several at once. With the answer in hand it returns it; without, it
stops the run with the request (`Stop::Read`), which the run returns as its
step. The run returns every read it asked for and was not given even when a
hook went on past the stop, so a hook that handles a call's error cannot
lose one. A read returns the state the host read; the command's own row as
the run changes it is the context's `own()`, `get(field)` and
`get_of(use_id, field)`.

Before the reads a rule across rows decides on (the last unit of stock, two
bookings of one room), a hook calls `ctx.lock(key)?`. A `LockKey` is the
behavior's logical name for what it serializes (`Stock/sku-1`); the host maps
it to its locks: on Postgres a transaction's advisory lock. SQLite and D1
take none, and a host's reads there come before the one batch that writes,
so the host checks in that batch that each read made under a lock still
answers as it did, and runs the command again when one does not: of two
commands that take one key, the second then decides over the first's
writes. A run asks for locks before reads, and decides on no answer made
before the latest lock (`Answers::get`).

## The change set

`ChangeSet` holds, each entry naming its row as a `RowRef { type, key }`
(the key `None` only for the row a create without an id makes, which the
host keys and gives to every entry that names it so):

- `rows`: every row the command writes, each held to its own version: the
  command's own row first, when it writes it, then each other row in the
  order the run reached it through `call_on` or `create_row`.
  `RowWrite::Create { type, key, own, owned }`, `RowWrite::Update { type,
  key, expected_version, own, owned }` (`own` the command's patch, empty for
  an operation, a deadline and a row reached by a call; the host moves the
  version on) or `RowWrite::Delete { type, key, expected_version }` (the
  row's sub-table rows and deadlines go with it); empty for a read-only
  operation and the define and publish checks. `owned` maps use id to field
  to value; null clears a field.
- `sub_rows`: `SubRowWrite { row, use_id, table, key, change }`, the change
  `Insert { values }`, `Update { values }` or `Delete`, in order.
- `events`: `OutboxEvent { row, kind, use_id, payload }`, the outbox rows the
  behaviors emitted, which the host writes in the same transaction whatever
  rows the change set writes.
- `deadlines`: `DeadlineChange::Arm { row, use_id, name, at, payload }` or
  `Clear { row, use_id, name }`, in order; a later entry for the same row,
  use and name replaces an earlier one.
- `result`: an operation's result; null for the rest.

`RowWrite`, `SubRowChange` and `DeadlineChange` are open, so a host matches
them with a fallback arm. `changes.row(type, key)` finds a row's write, and
`changes.owned(use_id, field)` an owned field the first, the command's own,
sets.

## Refusals

A `Refusal` is a `Code` (`as_str()`, `status()`), its HTTP `status`, the
engine's `class` for it, a `message`, and `details`, the class's members as
the scenario corpus records them; `error_json()` renders the engine's error
JSON (`{ class, code, message, details }`, no code for a defect or an
internal failure). The shapes:

| Case | Code | Status | Class | Details |
|------|------|--------|-------|---------|
| a veto | `vetoed` | 409 | `BehaviorVetoError` | `behavior`, `use` (a field anchor's), `action`, `reason`, `vetoCode`, `vetoDetails` |
| a use or config refused at define | `invalid_schema` | 422 | `SchemaDocumentError` | `issues: [{ use, index, behavior, kind, path, message }]` |
| a config change refused at publish | `incompatible_change` | 409 | `IncompatibleChangeError` | `changes: [{ path, message }]`, path `<Type>.behaviors.<use id>` |
| own fields a validation refuses, or that name a field anchor's field (rule `owned`) | `invalid_instance` | 422 | `InstanceValidationError` | `issues: [{ path, rule, message }]` |
| operation parameters | `invalid_argument` | 400 | `OperationParamsError` | `use` (a field anchor's, also named in the message: `operation transition of behavior Workflow on field fulfilment: ...`), `issues: [{ path, message }]` |
| create parameters | `invalid_argument` | 400 | `CreateParamsError` | `issues: [{ path, message }]` |
| a row, type, use or operation that does not exist | `not_found` | 404 | `EngineError` | none |
| a row `create_row` makes that the run reached already | `conflict` | 409 | `EngineError` | none |
| a permission the caller lacks | `forbidden` | 403 | `EngineError` | none |
| a defect in a behavior's code | `behavior_error` | 500 | `BehaviorError` | `behavior` |
| a host's own failure, a run that never settles | `internal` | 500 | `Error` | none |

A define issue's `kind` says what it is about and whose words it is in:
`use`, the use itself (a behavior the core does not run, a repeated id), at
the entry in the core's words; `schema`, the config's shape, which the
behavior's config schema gives, in the schema validator's words, at the
entry for a config the use must give and does not (`must have required
property 'config'`) or below `/config` for a member that breaks it
(`/config/states`: `must NOT have fewer than 1 items`); `rule`, a rule the
behavior's config breaks, at `/config`, in the behavior's words prefixed
with the type (and field) and the behavior (`type Order: behavior Workflow
config: the initial state "lost" is not one of its states (draft, open)`).
`path` is a JSON pointer into the use's entry; a host whose schema source
holds the entry at `/types/Order/behaviors/0` writes the issue there, and a
host whose validator checks configs against their schemas writes a
`schema` issue as its validator's (the engine's also failed the entry's
`then` branch, `must match "then" schema`).

The core knows no namespace and no version number, so where the engine's
message names them it leaves them out, and a host writes the engine's from
the details: `Order o1 does not exist` for `Order o1 does not exist in
namespace default`; `Order: <issues>` for `Order in namespace default
(version 1): <issues>`; `type Order: <changes>` for `schema Order in
namespace default: a new version cannot replace version 1: <changes>.
<advice>`; a define issue's use index and path for a path into the schema's
source (`/types/Order/behaviors/0/config`); and a create parameter's path
into the create's parameters (`/Workflow`) for one into its request
(`/behaviors/Workflow`). A veto's, a forbidden refusal's and the parameter
refusals' messages are the engine's as they are.

## Behaviors

A behavior implements `Behavior`:

- `type Config`, and `parse_config(raw, target)`, which checks a use's
  config (the one given, or `{}`) and returns it typed, or a `ConfigError`:
  `Schema(issues)`, what the config breaks of its config schema, each at a
  JSON pointer into the config in the schema validator's words (the core
  runs no JSON Schema, so a behavior writes its schema's checks and holds
  them to its declaration), or `Rule(reason)`, a reason the core prefixes
  with the type (and field) and the behavior; a `String` converts to a
  rule's reason. A use that gives no config and whose `{}` the behavior
  refuses for its shape is refused for the config it lacks.
  `ConfigTarget` gives the type's name and every use, for a config that
  names another (`status: 'fulfilment'`).
- `declaration()`: its name, owned fields and the one that holds a field
  anchor's value, sub-tables, operations (each writing or not), veto codes
  and whether a create gives it parameters
  (`Declaration::new(..).value(..).field(..).table(..).operation(name,
  writes).veto(code).create_params()`; the struct may gain members, so it is
  built so). The core holds a behavior to it: a field, a sub-table or a veto
  code it does not declare is a defect, an entry of create parameters for a
  behavior that takes none is refused, and so is a field anchor on one that
  names no value field.

and the hooks it needs, each with a default:

- `config_change(cx, before, after)`: a reason to refuse the change, or
  `None`. `before` is `None` for an added use, `after` for a removed one;
  `cx.has_rows()?` reads whether the type has rows. The default allows only
  an identical config and refuses an added or a removed use on a type with
  rows.
- `check_create_params(config, params)` and `check_params(config, operation,
  params)`: issues by JSON pointer, before any guard, as a createParamsSchema
  and a paramsSchema would. The core asks `check_create_params` only of a
  behavior whose declaration takes create parameters.
- `initialize(cx, params)`: the use's owned fields on a new row, from the
  config and its entry of the create's parameters (`{}` without one).
- `guard(view, request)`: `Some(Veto)` refuses a `GuardRequest` (`Create`,
  `Update`, `Delete`, or any use's `Operation`, a caller's or a call's,
  `caller` naming the calling use and `caller_type` its type for a call from
  another row); `request.operation_of(use_id, name)` matches the use's own
  operation. A guard refuses otherwise through its error
  (`view.forbidden(..)`, `Refusal::operation_params(..)`).
- `validate(view, request)`: issues with the own fields a write would store.
- `operation(cx, name, params)`: a declared operation's handler and its
  result. The default is a defect.
- `deadline(cx, name, payload)`: a deadline the use armed. The default is a
  defect.
- `after_change(cx, change)`: after a create, an update, a delete, a writing
  operation a caller called, or a deadline (`Change`); on another row, after
  a `create_row` of it or a writing operation of it a `call_on` called.

`GuardRequest`, `Change` and `ValidationRequest` are open, so a hook matches
the cases it handles with a fallback arm.

Every hook returns `Hook<T>`, a `Result<T, Stop>`, and passes on with `?`
every `Stop` it does not make itself. A hook runs on one row, the command's
or one a call reached. A `View` (guards, validation) reads: `config()`,
`type_name()`, `use_id()`, `behavior()`, `anchor()`, `field()`, `action()`,
`key()`, `now()`, `caller()`, `can(permission)?`, `own()`,
`own_field(name)`, `get(field)`, `get_of(use_id, field)`, `version()`, the
reads and `lock(key)?`, and makes refusals: `veto(..)`, `forbidden(..)`,
`defect(..)`. A `Cx` (initializers, handlers, deadlines, after-change hooks)
reads as a view and writes, on its row: `set(field, value)?`, `insert_row`,
`update_row`, `delete_row`, `emit(kind, payload)?`, `arm(name, at,
payload)?`, `clear(name)?`; makes keys, `new_key(label)?`; and reaches other
state through operations, `call(use_id, operation, params)?`,
`call_on(type, key, use_id, operation, params)?` and `create_row(type, key,
own, params)?`. A read-only operation's context refuses every write as a
defect.

`new_key(label)` derives a key from the command's request id, the use, the
label and a count (`r1:Comments:comment:1`), so every key a run makes is new
and a run again makes the same ones; a command with no request id refuses it
as a defect.

`can(permission)` asks the core's matcher whether the caller's permissions
satisfy one: by default `DottedPermissions`, the HTTP runtime's
`hasAnyPermission`, where a granted permission covers itself and every
permission nested under it and there is no root permission; a deployment's
own with `Core::with_matcher`. A caller standing in for an end user holds
none.

A use writes only its own state. It changes another use's state only
through that use's operations: `cx.call(use_id, operation, params)` runs the
operation's parameter check, every use's guard in list order (each told the
call is the calling use's) and its handler, and merges its effects into the
run's change set. `cx.call_on(type, key, use_id, operation, params)` does the
same on another row of any type, as a Holds use calls Supply on a stock row:
the core binds that row's type (`ReadRequest::Spec`, unless it is the
command's), reads the row, runs that type's parameter check, guards (told
the calling use and its type), handler and, for a writing operation, its
after-change hooks, and writes the row's update held to the version it read;
a second call on the row sees the first's writes. `cx.create_row(type, key,
own, params)` creates a row of any type as a caller's create runs, its type's
create parameters, validation, guards, initializers and after-change hooks;
a row the run creates stays a create whatever calls on it follow. A refused
call or create undoes its own effects and returns the refusal, which the
caller passes on with `?` or goes on past (`Stop::is_refusal`). Calls and
creates nest at most `MAX_CALL_DEPTH` (16) deep.

## Logical names and a host's layout

The core never sees a table or a column. A host's adapter reads its layout
to map each logical name, by rules every adapter and generator derives from
the declarations, so a behavior adds no code to any of them (D103):

- a type to its table, a row's key to the key column, a row's version to its
  `@optimistic` column, and a soft-deletable table's rows to its live ones,
  whose `deletedAt` column is NULL;
- an owned field `(use id, field)` to a column of the type's table: on a
  field anchor `f`, the declaration's value field to `f`'s own column, `f`
  in snake case, and every other field to `<f in snake case>_<field in
  snake case>` (Workflow on `status`: `status`, `status_entered_at`; on
  `paymentStatus`: `payment_status`, `payment_status_entered_at`); on a
  type anchor, every field to `<storage key>_<field in snake case>`, the
  storage key being the behavior's name lowercased with each run of other
  characters one `_` (Workflow: `workflow_status`, `workflow_entered_at`),
  so an own field of the same name sits beside it, as `core-workflow.json`
  records an own `status` beside Workflow's. Snake case is the
  generators' own: a `_` before each capital that follows a lowercase
  letter or a digit, each `-` a `_`, and the whole lowercased, so a run of
  capitals stays one word (`lastIPAddress`: `last_ipaddress`);
- a sub-table `(use id, table)` to a table with a foreign key to the row;
- an outbox event and a deadline to the rows of the outbox and the deadline
  sub-table;
- a lock key to an advisory lock, or, on SQLite and D1, to a check in the
  change set's batch of each read made under it.

A build-time project's layout is the type's own table (D103, "A build-time
project"); a runtime project's is its fixed tables, an instance's own fields
in a JSON column and each behavior's rows in a shared sub-table keyed by
schema and id ("A runtime project"). The core and the adapter are the same
for both; only the layout differs.

## The adapter

Each language has one adapter (D103): TypeScript's is the HTTP runtime's
`src/rules.ts` (exported from its `./postgres` and `./d1` entries), over
`@superschematic/rules` (`runtime/rules/typescript`),
the core built for `wasm32-unknown-unknown` from `../rules-wasm`, whose C
ABI takes and returns JSON as `runtime/versiongraph`'s does. An adapter
holds no behavior's name; it reads the layout document (below) and runs the
host loop above inside the caller's transaction, beside the caller's other
writes. For each command:

1. It builds the command with the caller, the time in epoch milliseconds
   and a request id of its own, unique per command and kept for every run,
   and gives a create that names no key one of its own: a UUID's wire form
   for a UUID key and a UUID's text for a string key. It refuses a create
   with no key of any other type (`invalid_argument`), since the change set
   names every row it writes by its key.
2. It runs the core. For a `row` read it selects the row by its key: its
   own columns, as the core's own fields, and each use's owned columns, as
   that use's owned fields by declared name, each converted to the core's
   JSON (a timestamp to epoch milliseconds), a NULL column absent, and its
   version. For `hasRows` it asks whether the table has a row; for `spec`
   it answers the layout's spec of the type, or none for a type the layout
   lacks. A soft-deletable table's reads see its live rows alone (the
   layout's `live`), so a row the repository holds deleted is `not_found`
   to the core. It records each answer with the locks it held when it read.
3. For a lock key it takes the lock, on Postgres
   `pg_advisory_xact_lock(hashtextextended(key, 0))`, which the
   transaction holds until it ends, before the reads it guards. SQLite and
   D1 take none (the store's locks are `fenced`): their reads run outside
   the batch that writes, so the batch begins with a check of each read the
   command made under a lock, a row still at the version read or still
   absent, a table that still has rows or still none, and a check that
   fails moves the command as a fenced write does. It records the key and
   runs again, and the core asks again for the reads made before it.
4. A refusal it throws whole: class, code, status, message and details.
5. A change set it applies as one unit: on Postgres in a savepoint of the
   caller's transaction, which a moved row or a failed statement rolls back
   to, leaving the transaction going on; on SQLite and D1 as one batch,
   with a guard after each fenced statement that fails the batch when the
   statement changed another number of rows. A create inserts its key, the
   own fields and the owned columns, and is refused as `conflict` when the
   table holds the key, as the reference host refuses it; an update sets the
   patch's own columns (a null for a field the table has no column for
   clears nothing, as a merge patch's null for a member it lacks), the owned
   columns and `_version = _version + 1` `WHERE key = k AND _version =
   expected`; a delete deletes `WHERE key = k AND _version = expected`; a
   soft-deletable table's update and delete hold the row live as well, so a
   soft delete made under a command moves the row. A caller that encodes the command's own row itself (the generated
   repository's UUIDs, envelopes and audit stamps) passes its own columns,
   which bind in place of the change set's own fields, and a soft-deletable
   table's repository the columns a delete of the row sets in place of
   deleting it (deletedAt, deletedBy), fenced alike. A fenced statement
   that changes no row, or a check that fails, means the row moved: the
   adapter undoes the unit, forgets its answers and runs the command again
   from fresh state, with the same request id, up to eight runs, then
   refuses it as `busy`. A
   sub-table row, an outbox event or a deadline it refuses as `internal`
   until the change that gives each a table (D103's order of the work, step
   3): Workflow writes none.
6. It returns `changes.result`.

An upsert's insert applies no change set of the core's, so it binds what
the core's create of the row gives it: the adapter runs the create to its
change set and applies nothing (`prepareCreate`), and the repository
inserts the row with the owned fields that change set holds and updates a
row the unique key already holds by its own fields alone, as the
repository's `updateMany` does.

A row's version is the column `@optimistic` keeps, `_version`, which a
trigger bumps on every update (`BEFORE UPDATE` on Postgres, `AFTER
UPDATE` on SQLite and D1); a table that composes a behavior gets it.
The version is what the core holds each written row to, so a write that
another transaction made since the read, an ORM write or a raw one, moves
the row and runs the command again. A lock key is the behavior's logical
name for what a rule across rows serializes; the adapter never parses it.

When deadlines come (step 3), an adapter runs a deadline command only while
the deadline is still armed at the time the command names, checked before
each run and again in the apply, which spends it: the core does not check
it yet.

Every adapter replays the recorded scenarios as transcripts:
`superschematic-rules-testkit` writes `core-workflow.json` out as
`runtime/engine/testdata/rules/core-workflow.transcript.json` (its
`transcript` module is the format), the core's commands and what each must
answer, through the replayer's own mapping, and an adapter's tests replay it
against tables of their own on SQLite and on Postgres. The testkit's
`tests/transcript.rs` fails when the checked-in file is not what the writer
writes (`UPDATE_TRANSCRIPTS=1` rewrites it), and holds it to a floor of 96
steps: 61 define and publish checks, 34 commands and one raw write, in the
14 scenarios a core with no test fixtures runs. TypeScript's replay is the
HTTP runtime's `src/rules-transcript.test.ts`, held to the same floor on
bun:sqlite and on Postgres.

## The layout document

The generator writes, for each table that composes field-anchored
behaviors, where the core's names live
(`internal/generator/behaviorlayout`, which holds the layout rule in Go),
and the generated ORM embeds it: the table, its key column and IR type, the
version column, a soft-deletable table's `deletedAt` column (`live`), its
own fields' columns, each use's id, behavior, anchor and
owned columns (each declared field by its declared name, its IR field, its
column, its IR type and how it converts), each use's operations, and the
type as the core runs it, in `TypeSpec`'s JSON. For an `Order` whose
`status` is a `Workflow`:

```json
{
  "types": {
    "Order": {
      "type": "Order",
      "table": "order",
      "key": { "field": "id", "column": "id", "type": "Identity.UUID" },
      "version": "_version",
      "own": [{ "field": "title", "column": "title", "type": "string" }],
      "uses": [
        {
          "id": "status",
          "behavior": "Workflow",
          "anchor": "status",
          "fields": [
            { "name": "status", "field": "status", "column": "status", "type": "string", "value": true },
            { "name": "enteredAt", "field": "statusEnteredAt", "column": "status_entered_at", "type": "Temporal.DateTime", "convert": "epochMillis" }
          ],
          "operations": [{ "name": "transition", "writes": true }]
        }
      ],
      "spec": {
        "name": "Order",
        "uses": [{ "behavior": "Workflow", "config": { "states": ["draft", "paid"], "transitions": [{ "from": "draft", "to": "paid" }] }, "anchor": { "field": "status" } }]
      }
    }
  }
}
```

A declared field's column type is its declaration's `type` (Workflow's
`status` a `string`, `enteredAt` a `Temporal.DateTime`), and a date-time is
epoch milliseconds in the core: the adapter binds and reads a `Date` on
Postgres and the text `Date.prototype.toISOString` writes on SQLite. The
declaration's `value` names the field that takes the anchor field's own
name and column.

The own columns are the table's columns but its key, its version, its
owned fields, its audit fields and its internal metadata; a to-one
relation's column holds its target's key and is named as a generated row
holds it, `<field>Id`, with the target key's type. The generated `orm.ts`
embeds the document as `<database>Rules` and passes it to the HTTP
runtime's ORM in its definition's `rules`.

## Checks

```
cd runtime/engine/rust
cargo fmt --all --check
cargo clippy -p superschematic-rules --all-targets -- -D warnings
cargo test -p superschematic-rules
cargo build -p superschematic-rules -p superschematic-rules-behaviors --target wasm32-unknown-unknown
cargo test -p superschematic-rules-wasm
cargo build -p superschematic-rules-wasm --target wasm32-unknown-unknown
```

with `RUSTUP_TOOLCHAIN` set to the toolchain `tools.env` pins.
`tests/core.rs` runs the core through its public API with toy behaviors: the
read exchange, vetoes, a call between two behaviors, two uses of one
behavior on two fields and the own writes their fields refuse, a field
anchor on a behavior with no value field, a config change that reads
whether rows exist, the define check's uses, shapes and rules, create
parameters, locks and the answers made before them, deadlines, a call on
another type's row and a create of one, the keys a behavior makes, a source
that refuses, a run that never settles and the defects the core holds a
behavior to.

`superschematic-rules-testkit`'s `tests/workflow_scenarios.rs` replays
`core-workflow.json` on Workflow through the reference host and fails on any
step that answers otherwise than the engine, or when fewer steps are
checked than its floor; `cargo test -p superschematic-rules-testkit` runs
it, and `tests/transcript.rs` with it.

TypeScript's binding and adapter:

```
cd runtime/rules/typescript && bun install && bun run test
cd runtime/http/typescript && bun test src/rules*.test.ts
```

The first builds the module with the workspace's `dist` profile (one
codegen unit, LTO, no symbol table: about 530 KB, 180 KB gzipped, against
785 KB from a release build), copies it into `dist/`, and runs the loader's
and the contract's tests under Bun and a smoke under Node. The second runs
the adapter over an in-memory transaction, over bun:sqlite and Postgres, and
the transcript's replay on both; its Postgres cases read
`SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` and are skipped without it, and its
tests build the module first whenever cargo is on `PATH`, so they never run
one older than the crates (cargo skips a build of crates that have not
changed, and `scripts/build-wasm.mjs` leaves `dist/` alone when the module
is the one it holds). The Go tests that build the package
(`internal/testpaths`' `BuildRulesPackage`) rebuild it when a file of these
crates, of `behaviors/declarations` or the workspace's lock file changes.

The generator's layout is held to the adapter by
`go test ./internal/generator/tsgen -run TestWorkflowTablesEndToEnd`: it
generates `fixture-workflow-db`'s package from its `.schema.ts`, applies
the generated DDL on bun:sqlite and on the Postgres
`SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL` names, and runs the same cases
through the adapter over the generated layout on SQLite and through the
generated repository on Postgres.
