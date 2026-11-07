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
ported onto it, in the engine's order: Workflow, Reactions, Constants,
StateRules, Approvals, Timers, Lease, Presence, Budget and Retries, then
Seats, Reconcile, Schedules and Pool, which no engine implements (D104),
the calendar Schedules reads its cron lines with, in a zone of the IANA
database jiff bundles (`calendar`), and the contract Capacity, which
Budget implements and Pool asks (`capacity`);
`superschematic-rules-testkit` (`../rules-testkit`) is its
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

A type may list its own fields too (`fields`, each a `FieldSpec`: its name,
its IR type and `dateTime`, whether its values are instants, epoch
milliseconds from a table's adapter or the ISO 8601 text an engine schema's
date-time holds), which `ConfigTarget::fields()` gives a behavior whose
config names a field of the type: a Timers deadline that starts from a
field naming none of them, or one that holds no instant, is refused. The
layout's `spec.fields` gives a table's; a host that gives none refuses such
a config, `(its fields: none)`. An own field that holds the key of a
to-one relation names it (`relation`: the relation's name and its target
type, `FieldSpec::relation(name, type, relation, target)`;
`TypeSpec::relation(name)` finds it by the relation's name), which the core
follows from a write of the row to the uses of the target type that listen
to it (a change command, below).

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
   the command names (`Action::Deadline`'s `at`), which the host checks and
   the apply spends (The adapter, below); the core does not. Return
   `changes.result`.

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

`Action` is one of the following. The list is open, so a host matches it
with a fallback arm.

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
- `TableOperation { use_id, operation, params }` (D104): one use's
  operation of the table as a whole, a declaration's `"scope": "schema"`
  (Pool's `pick`, Lease's `expireHolder`), a command without a row: no key
  and no preconditions. It runs on the table's placeholder, whose own and
  owned fields are empty: the use's `check_params` and `settle`, every
  use's `guard` in list order with `GuardRequest::Table`, then its handler,
  and no `validate` or `after_change`. It reads rows through the reads
  across a table, the linked and the holding reads, and writes them only
  through `call_on` and `create_row`, each row's guards, version and hooks
  running as for any call; a write of its own state is a defect. It takes
  no lock of its own. Its change set holds the rows its calls wrote, none
  when they wrote nothing, and its result; a read-only one (`writes:
  false`) writes nothing. An `Operation` that names one is `not_found`, and
  so is a `TableOperation` that names an operation of a row.

  ```json
  {"kind": "tableOperation", "useId": "Pool", "operation": "pick", "params": {"exclude": ["a1"]}}
  ```
- `Deadline { key, use_id, name, at, payload }`: a deadline a use armed that
  fell due, with the time it was armed to fall due at and the payload it was
  armed with, run at the later of the host's clock and `at`. The host, never
  the core, checks that it is still armed at `at`, so one cleared or moved
  since does not fall due on its old arming (The adapter, below).
- `Change { key, change }`: a write of the row that the table's change queue
  hands the core (D103), whoever made it, the core, a repository's bulk or
  hard write, or a raw SQL one. `change` is a `RowChange`: what the write did
  (`WriteKind`: `insert`, `update` or `delete`), when (`at`, epoch
  milliseconds by the database's clock), and the row before it (none for an
  insert) and after it (none for a delete), each a `RowImage`, its own fields
  by name (`own`) and its uses' owned fields by use id and declared name
  (`owned`), a field with no value absent. Its result is null.

  ```json
  {"kind": "change", "key": "t1", "change": {
    "write": "update", "at": 1001000,
    "before": {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "todo", "enteredAt": 1000000}}},
    "after":  {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "doing", "enteredAt": 1001000}}}}}
  ```

A `Command` carries `preconditions` beside its action (D16, D104): the
caller's assertions of the row, each use's entry by its use id (an
implements use's id is its behavior's name, so `{"Lease": {"token": 3}}`),
written only when there are any. An update, a delete and an operation hold
them to the type's uses before the row is read: an entry for a use the type
lacks (`/Ghost: Job has no behavior use Ghost (its uses that take
preconditions: Lease)`), for a use whose behavior's declaration takes none
(`/Workflow: behavior Workflow takes no precondition`), and each issue a
behavior's `check_precondition` finds, under its entry (`/Lease/token: must
be >= 0`), refuse the command together as `PreconditionsError`, in the
command's order. Any other action that carries them is refused so (`a create
takes no preconditions: an update, a delete or an operation of a row
presents them`). A guard asked about the command's own update, delete or
operation reads its use's entry (`view.precondition()`), and so does one
asked about a call a behavior makes as the caller's own request
(`cx.call_as_caller(..)`); one asked about a create, any other `call()` or
a `call_on()` reads none, so a request a behavior's code makes for itself
presents nothing.

A command on a row reads it first (`ReadRequest::Row`); a key the type has
none of is `not_found`. Then the uses compose in list order:

| Command | In order |
|---------|----------|
| create | the create parameters (an entry naming no use, an entry for a behavior that takes none, and each `check_create_params` of one that takes them, with its entry or `{}`), the own fields that name a field anchor's field and every `validate`, every `guard`, every `initialize`, every `after_change` |
| update | the patch's members that name a field anchor's field and every `validate` (own fields before and after the patch), every `guard`, every `after_change` |
| delete | every `guard`, every `after_change` |
| operation | the use's `check_params`, every `guard`, its handler; for a writing one, every `after_change` |
| deadline | the use's `deadline` handler, every `after_change` |
| change | on a row that is there, every hearing use's `heard` (`Heard::Own`); then, for each own field that holds a to-one relation, the listening uses of its target type, each `heard` (`Heard::Linked`) on each row the relation holds the key of; after each, when it wrote its row, every `after_change` of that row (`Change::Heard`) |

Validation issues from every use refuse together (`invalid_instance`); the
first veto refuses (`vetoed`); the first refusal of any kind ends the run.

A change command runs on the row as it is now; the images say what moved.
It reads the row first: a row that is gone runs no hook of it and writes
`RowWrite::Gone` (The change set, below). On a row that is there, each use
whose declaration hears changes (`Declaration::hears_changes`, the file's
`"hearsChanges": true`: Timers and Reactions) runs `heard` with
`Heard::Own { change }`, in list order, in a writable scope whose action is
`change`. Then, for each own field that holds a to-one relation, in field
order, the core binds its target type (one the host has no spec of is
passed over) and, for each of its uses whose `listens` names the command's
type and the relation (`Listen { type, link }`), runs `heard` with
`Heard::Linked { type_name, key, link, change }` on each row the relation
holds the key of, in the row now, after the write and before it, each once,
a key read as a command names a row (`key_of`: a string, or a whole number's
decimal text, the form a table keyed by a number has its keys in); a target
not found is passed over, and any other refusal ends the run. A
row a hook writes is held to the version it was read at, and its
after-change hooks run with `Change::Heard`. A change whose hooks write
nothing writes no row (an empty `rows`, or a `Gone` alone), so applying it
moves no version. Every hearing rule is idempotent against a write the core
already applied: a write through the core is heard too, by its change after
the commit, and Timers' reconcile then finds its deadlines as the write left
them and Reactions' rules act only where they can, so it changes nothing
more than the rules it sets off.

## The read exchange

A `ReadRequest` names logical targets: `Row { type, key }` (answered
`Answer::Row(Option<Row>)`, a row's own fields, its owned fields by use id
and field, and its version), `HasRows { type }` (`Answer::HasRows(bool)`),
`Spec { type }` (`Answer::Spec(Option<TypeSpec>)`, a type's live version,
which a call on another type's row binds and a behavior that reads another
type's config reads; the core never asks it of the command's own type) and
`SubRows { type, key, use_id, table }` (`Answer::SubRows(Vec<SubRow>)`, the
rows of one sub-table of one use on one row, each its `key` and its
`values` by declared field, in key order by code point, the key's field
left out and a NULL column absent) and `Linked { type, field, key, after,
limit }` (`Answer::Linked(LinkedRows)`: `rows`, each a `KeyedRow` of its key
and its `Row`, and `more`, whether rows follow), the rows of a type whose own
field `field` holds `key`, in its key column's order, after the key `after`
when given, at most `limit` (`LINKED_PAGE`, 500, Links' page), a
soft-deletable table's live rows alone, and none for a type the host does
not have: what a rule over the rows that link to a row reads (Reactions'
allTerminal); and `Holding { type, use_id, field, value, after, limit }`
(`Answer::Linked(LinkedRows)` too), the rows of a type whose use's owned
field holds `value`, compared as the column holds it, converted as a
command's value of the field is, in its key column's order, paged as a
linked read is: what a rule over the rows a principal holds reads
(Presence's releaseLeases over Lease's holder, D104); and `Rows { type,
equal, tables, after, limit }` (`Answer::Linked(LinkedRows)` too, D104),
the live rows of a type, each own field `equal` names holding its value (a
null: the column is NULL), bound as a command's value of the field binds,
in its key column's order, paged as a linked read is, each row's `KeyedRow`
with `tables`: the rows of each sub-table the request names (`TableRef {
use_id, table }`), by use id and table name, each table's rows in key order,
`[]` for a row that has none, read with one query per sub-table per page.
What a rule over a table's rows reads (Pool's pick): equality over own
columns is the one rule a host learns of it, and the core applies every
other (a behavior's predicate, its order, the contracts it asks) to the
rows it is answered. A field that is no own column of the type, or a
sub-table the layout does not give its use, is refused as `internal`, and
so is the read under a lock on SQLite or D1. The enum is open: sub-table rows by a condition and counts
join it as behaviors need them, so a host matches it with a fallback arm,
which refuses the command as `internal`. An answer of
another kind than its request asks for is refused as `invalid_argument`.

A hook reads through its context, `ctx.read_row(type, key)?`,
`ctx.has_rows(type)?`, `ctx.read_spec(type)?`, `ctx.config_of(type,
use_id)?` (a use's config as the type holds it, `{}` when it gives none),
`ctx.read_sub_rows(type, key, use_id, table)?` (any row's, as the host read
them), `ctx.sub_rows(table)?` (the use's own rows of one of its sub-tables
on its row, with the run's own writes so far applied: an insert adds, an
update merges, a delete removes; a row the run creates has none and reads
no host; a table the declaration does not declare is a defect),
`ctx.read_linked(type, field, key, after)?` (one page of a linked read),
`ctx.read_holding(type, use_id, field, &value, after)?` (one page of a
holding read), `ctx.read_rows(type, &equal, &tables, after)?` (one page of
a read across a table, D104), `ctx.implementers(type, contract)?` and
`ctx.contract_tables(type, contract)?` (the uses of a type that implement a
contract, by their declarations, and the sub-tables a read across it takes
for them), `ctx.ask(type, &keyed_row, &ContractQuery::new(contract, query,
&params))?` (each implementer's answer about a row a read across the table
gave, by `Behavior::answer`; a contract the asking use's declaration does
not list in `asks` is its defect, and a query an implementer answers `None`
to is the implementer's), `ctx.sub_rows_of(type, key, use_id, table)?` (any row's
rows of a sub-table as the run has them: the host's with the run's writes
of them applied, none for a row the run creates or deleted; a use the type
lacks, or a table its behavior does not declare, is a defect),
`ctx.row_of(type, key)?` (a row of any type as the run has it: its writes so
far applied when the run reached it, else the host's; `None` when there is
none of the key or the run deleted it),
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
  row's sub-table rows and deadlines go with it), or `RowWrite::Gone { type,
  key }` (`{"kind": "gone", "type": "Task", "key": "t1"}`), a row a change
  command found gone: no write of it, but its deadlines go, and its
  sub-table rows when no row of the key is left at all (a soft-deleted row
  keeps them, as a soft delete through the core does); empty for a
  read-only operation and the define and publish checks. `owned` maps use
  id to field to value; null clears a field.
- `sub_rows`: `SubRowWrite { row, use_id, table, key, change }`, the change
  `Insert { values }`, `Update { values }` or `Delete`, in order.
- `events`: `OutboxEvent { row, kind, use_id, payload }`, the events a
  behavior emitted of its own about a row (`Cx::emit`), each of a kind its
  declaration's `events` lists (D104: an emit of another kind is a defect,
  `emit(x): it declares no event x (its events: run)`), which the host
  writes to the event queue of its row's table in the change set's
  transaction, and the table's behavior worker hands to a method of the
  API's Workers. Schedules' fire emits one (`run`); a row's writes reach
  the behaviors that hear them through the table's change queue, not
  these. The core holds no payload to its kind's `payloadSchema`: each
  behavior's tests do.
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
| a command's preconditions | `invalid_argument` | 400 | `PreconditionsError` | `issues: [{ path, message }]`, each path under its use's entry (`/Lease/token`) |
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
  names another (`status: 'fulfilment'`), its own fields (`fields()`), and,
  when the core builds it, the declaration of a behavior the core runs
  (`declaration(behavior)`), so a config that names another use's field
  reads what that behavior declares (`ConfigTarget::new`, a host's, has
  none).
- `declaration()`: its name, owned fields and the one that holds a field
  anchor's value, sub-tables, operations (each writing or not), veto codes,
  whether a create gives it parameters, whether it arms deadlines, whether
  it hears its row's writes (a table that composes it gets a change queue,
  D103) and the fields it computes at a read, which no column holds and no
  write carries (Timers' `deadlines`)
  (`Declaration::new(..).value(..).field(..).sub_table(TableDecl::new(name,
  key, fields)).arms_deadlines().hears_changes().computed(name).operation(name,
  writes).veto(code).create_params().preconditions()`, the last for a
  behavior a caller presents a precondition, as a preconditionSchema says); the struct may gain members, so it is
  built so; `.table(name)` declares a sub-table of no key and no fields,
  whose writes the core does not check). The core holds a behavior to it: a
  field, a sub-table or a veto code it does not declare is a defect; so is
  an `arm` or a `clear` by a behavior that arms no deadlines (`arm(respond):
  it declares no deadlines (armsDeadlines)`), a value a sub-table declared
  with fields does not hold (`insert_row: deadlines has no field x (its
  fields: ...)`, and `update_row`'s), and the key's field among a write's
  values (`insert_row: name is the row's key, which the write names`). An
  entry of create parameters for a behavior that takes none is refused, and
  so is a field anchor on one that names no value field.

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
- `check_precondition(config, entry)`: issues by JSON pointer into the use's
  entry of a command's preconditions, before the row is read, as a
  preconditionSchema would (D104). The core asks it only of a behavior whose
  declaration takes preconditions.
- `settle(view, operation, params)`: an answer to an operation of the
  use's own before any use's guard is asked (D104). The core asks it after
  the parameter check, for a caller's operation, a `call()` and a
  `call_on()` alike. `Some(result)` ends the call with that result: no
  guard and no handler runs, nothing is written and no after-change hook
  runs, so no other use's gate refuses a call that changes nothing. For the
  command's own operation the change set then holds no row, sub-table row,
  event or deadline, only `result`, which every host applies as a unit
  that writes nothing (the version, `enteredAt` and the audit stamps stay,
  and the change queue hears nothing); a settled `call_on` neither marks
  the other row written nor runs its after-change hooks. It may refuse
  instead, a veto among them, before any guard: a check of the caller's
  own read of the row. `None`, the default, goes on to the guards and the
  handler. Workflow's settle answers a repeat (`repeat: "unchanged"`) and
  refuses a stale `from`.
- `initialize(cx, params)`: the use's owned fields on a new row, from the
  config and its entry of the create's parameters (`{}` without one).
- `guard(view, request)`: `Some(Veto)` refuses a `GuardRequest` (`Create`,
  `Update`, `Delete`, or any use's `Operation`, a caller's or a call's,
  `caller` naming the calling use, `caller_type` its type for a call from
  another row, `fired` a call a deadline's handler makes on its row,
  the request of the worker that runs the deadline, as the engine's runner
  made one: Lease holds it to an active lease as a caller's, D104, and
  `as_caller` a call a behavior makes as the command's caller's own request,
  `cx.call_as_caller(..)`: Reconcile's steps toward an observed state, which
  Lease holds so too);
  `GuardRequest::Table` asks about an operation of the table as a whole,
  with no row, which no guard of the behaviors so far refuses;
  `request.operation_of(use_id, name)` matches the use's own operation. A
  guard refuses otherwise through its error (`view.forbidden(..)`,
  `Refusal::operation_params(..)`).
- `validate(view, request)`: issues with the own fields a write would store.
- `operation(cx, name, params)`: a declared operation's handler and its
  result. The default is a defect.
- `deadline(cx, name, payload)`: a deadline the use armed. The default is a
  defect.
- `after_change(cx, change)`: after a create, an update, a delete, a writing
  operation a caller called, or a deadline (`Change`); on another row, after
  a `create_row` of it or a writing operation of it a `call_on` called; and
  on a row a `heard` hook wrote (`Change::Heard`).
- `listens(config)`: the types whose rows' writes the use hears beside its
  own row's, each a `Listen { type, link }`, the type and its to-one
  relation to the use's type (Reactions' allTerminal over Task's `project`:
  `{ type: Task, link: project }`). None by default. `Core::listeners(spec)`
  gives a type's, each once, in list order; a host that keeps no layout
  (the reference host) reads from it which types are heard.
- `heard(cx, heard)`: a write the use hears: of its own row (`Heard::Own`),
  for a behavior whose declaration hears changes, on the row as it is now;
  or of a row of a type it listens to (`Heard::Linked`), on the row the
  relation holds, or held, the key of. Nothing by default.
- `read_fields(config, row, tables)`: the fields a read of a row shows for
  the use beyond its owned columns, computed from its owned fields and its
  sub-table rows by table (the engine's computed fields: Timers'
  `deadlines`). None by default. `Core::read_fields(spec, use_id, row,
  tables)` computes them; the replayer compares them, and no adapter serves
  them yet (D103's Left for later).
- `read_fields_at(config, row, tables, now)`: `read_fields` at the time the
  read is made at, for a field that turns on the clock (Lease's `active`,
  D104); the default is `read_fields`'. `Core::read_fields_at(spec, use_id,
  row, tables, now)` computes them, and the replayer reads at the step's
  time.
- `answer(config, query, row, tables, now)`: the use's answer to a query of
  a contract its declaration implements (`Declaration::contracts`, the
  file's `"contracts": [{ "name", "role": "implements" }]`, D104) about a
  row as a read across the table gave it, with the use's sub-tables by
  name: Budget answers Capacity's `hasRoom` (`{ room, until? }`) over its
  meters. Pure, as `read_fields_at` is. `None`, the default, answers no
  query, which the core refuses as the implementer's defect when one is
  asked. A behavior that asks a contract lists it (`"role": "asks"`,
  `Declaration::asks`): Pool asks Capacity of each member at a pick.

`GuardRequest`, `Change`, `Heard` and `ValidationRequest` are open, so a hook matches
the cases it handles with a fallback arm.

Every hook returns `Hook<T>`, a `Result<T, Stop>`, and passes on with `?`
every `Stop` it does not make itself. A hook runs on one row, the command's
or one a call reached. A `View` (guards, validation) reads: `config()`,
`type_name()`, `use_id()`, `behavior()`, `anchor()`, `field()`, `action()`,
`key()`, `now()`, `caller()`, `precondition()`, `can(permission)?`, `own()`,
`own_field(name)`, `get(field)`, `get_of(use_id, field)`, `version()`, the
reads and `lock(key)?`, and makes refusals: `veto(..)`, `forbidden(..)`,
`defect(..)`. A `Cx` (initializers, handlers, deadlines, after-change hooks)
reads as a view and writes, on its row: `set(field, value)?`, `insert_row`,
`update_row`, `delete_row`, `emit(kind, payload)?`, `arm(name, at,
payload)?`, `clear(name)?`; makes keys, `new_key(label)?`; and reaches other
state through operations, `call(use_id, operation, params)?`,
`call_as_caller(use_id, operation, params)?`, `call_on(type, key, use_id,
operation, params)?` and `create_row(type, key, own, params)?`. A read-only operation's context refuses every write as a
defect. `unchanged()?` says the command's operation writes nothing: its
change set holds no write, the row's version stays and no after-change hook
runs, as the engine's `unchanged()` kept an idempotent call out of the log
(Timers' `fireTimer` of a deadline not due). It is honored for the
command's own operation and does nothing in a call, a deadline, an
initializer or an after-change hook; a write in the same run, before it or
after it, is a defect (`unchanged() and a write in one run: <what>`). A
handler says it after every guard allowed the call; a use that answers its
own operation as no change before any guard is asked says so in `settle`.

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

A field that holds a subject names the caller by its value
(`superschematic_rules_behaviors::subjects`, D104), as Workflow's `by`
reads one: a field of type `string`, `Identity.UUID` or `Identity.UserID`
(`SUBJECT_TYPES`) holds a subject. A `string` field names the caller whose
subject is its text, exactly. A UUID field names the caller whose subject
is the same UUID by value, in either of its forms, hyphenated (either case)
or base62, so the base62 every adapter reads a UUID column as matches a
subject a custom authenticator gives hyphenated, with no adapter
normalizing the caller; a text that is no UUID compares as text. An absent,
null, empty or non-string value names no one, and an empty subject is
named by no field.

A use writes only its own state. It changes another use's state only
through that use's operations: `cx.call(use_id, operation, params)` runs the
operation's parameter check, its use's `settle`, every use's guard in list
order (each told the call is the calling use's) and its handler, and merges
its effects into the run's change set. `cx.call_as_caller(use_id,
operation, params)` is a `call` made as the command's caller's own request
(D104): each guard is told so (`as_caller`) and handed its use's entry of
the command's preconditions, as for the command's own operation, so a rule
that lets a behavior's request through holds this one as the caller's
(Lease's); a deadline's handler's is `fired` as well. `cx.call_on(type, key, use_id, operation, params)` does the
same on another row of any type, as a Holds use calls Supply on a stock row:
the core binds that row's type (`ReadRequest::Spec`, unless it is the
command's), reads the row, runs that type's parameter check, settle, guards
(told the calling use and its type), handler and, for a writing operation
its use did not settle, its after-change hooks, and writes the row's update
held to the version it read;
a second call on the row sees the first's writes. `cx.create_row(type, key,
own, params)` creates a row of any type as a caller's create runs, its type's
create parameters, validation, guards, initializers and after-change hooks;
a row the run creates stays a create whatever calls on it follow. A refused
call or create undoes its own effects and returns the refusal, which the
caller passes on with `?` or goes on past (`Stop::is_refusal`). One that
waits on a read or a lock the host has not answered is undone too, and a
behavior that calls on many rows goes on past it to the next row and
returns the wait at the end (`Stop::is_waiting`, D104): the run asks the
host for every read its hooks asked for, so one round of the host loop
answers every row's next read. Calls and creates nest at most
`MAX_CALL_DEPTH` (16) deep.

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
- a sub-table `(use id, table)` to a table with a foreign key to the row,
  deleted with it: `<row's type><member><table>` in Pascal case
  (`TicketTimersDeadlines`, table `ticket_timers_deadlines`), the member being
  a field use's field and an implements use's storage key in camel case
  (`timers`), with a column holding the row's key (`ticket_id`), the
  declared key's column (`name`), each declared field's, a surrogate `id`,
  and one row per (row, key);
- a deadline `(row, use id, name)` to a message of the table's deadline
  queue, `<row's type>Deadline` (`TicketDeadline`, table
  `ticket_deadline_queue`), whose `row_key`, `use_id`, `name`, `at` (epoch
  milliseconds) and `payload` columns name it and whose `due_at` is `at`, one
  live message (ready or claimed) per row, use and name;
- the audit field `createdBy` (D87), when the table has one that is a
  plain scalar column, to that column, which a row read holds among the
  row's own fields and no command writes (the layout's `audit`, D104),
  when a Workflow transition's `by` or a StateRules state's `by` names it,
  or an Approvals gate excludes the creator, which reads it;
- a row's writes to messages of the table's change queue, `<row's
  type>Change` (`TicketChange`, table `ticket_change_queue`), which a
  trigger on the table writes for every insert, update and delete, whoever
  makes it: the row's key (`row_key`), the write (`write`), its time (`at`,
  epoch milliseconds by the database's clock) and the row's columns before
  and after it as JSON (`before`, `after`), which the adapter maps to the
  core's names as it maps a row read, and the table's behavior worker runs
  as a change command; a table gets one when one of its behaviors hears
  changes, or another table's listens to it;
- an event a behavior emits about a row (`Cx::emit`, a kind its
  declaration's `events` lists, D104) to a message of its row's table's
  event queue (`<table>_event_queue`, the layout's `events`: the row's key,
  the use, the kind, the command's time and the payload), written in the
  change set's transaction after the sub-table rows and kept when the row
  is deleted, which the table's behavior worker hands to a method of the
  API's Workers;
- a lock key to an advisory lock, or, on SQLite and D1, to a check in the
  change set's batch of each read made under it.

A build-time project's layout is the type's own table (D103, "A build-time
project"); a runtime project's is its fixed tables, an instance's own fields
in a JSON column and each behavior's rows in a shared sub-table keyed by
schema and id ("A runtime project"). The core and the adapter are the same
for both; only the layout differs.

## The adapter

Each language has one adapter (D103). TypeScript's is the HTTP runtime's
`src/rules.ts` (exported from its `./postgres` and `./d1` entries), over
`@superschematic/rules` (`runtime/rules/typescript`),
the core built for `wasm32-unknown-unknown` from `../rules-wasm`, whose C
ABI takes and returns JSON as `runtime/versiongraph`'s does. Go's is the
module `runtime/rules/go` (its `README.md`): package `rules` binds the
same crate's native build, the static archive `libsuperschematic_rules.a`
that `scripts/rules-archive.sh` stages, through cgo over the same C ABI,
and holds the adapter, with a store over pgx (`postgres`) and one over
`database/sql` (`sqlite`); the generated Go ORM runs over it. Rust's is
`../rules-adapter` (`superschematic-rules-adapter`, its `README.md`), over
the crates directly with no C ABI, for a hand-written server, with a
store over tokio-postgres and one over rusqlite. An adapter holds no
behavior's name; it reads the layout document (below) and runs the
host loop above inside the caller's transaction, beside the caller's other
writes. For each command:

1. It builds the command with the caller, the time in epoch milliseconds
   and a request id of its own, unique per command and kept for every run,
   and gives a create that names no key one of its own: a UUID's wire form
   for a UUID key and a UUID's text for a string key. It refuses a create
   with no key of any other type (`invalid_argument`), since the change set
   names every row it writes by its key.
2. It runs the core. For a `row` read it selects the row by its key: its
   own columns, as the core's own fields, then its audit columns (the
   layout's `audit`, D104) among the own fields by their field names, and
   each use's owned columns, as that use's owned fields by declared name,
   each converted to the core's
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
   refuses it as `busy`. A unit runs in this order: the checks of the reads
   made under locks (fenced stores); every row write, each fenced on its
   version, and a `Gone`'s deletes; for a deadline command the spend of its
   arming, and for a change command with a claim the spend of its message
   (below); the
   sub-table rows, in order, skipping a deleted row's (an insert `ON
   CONFLICT (row, key) DO NOTHING`, an update of the values present and a
   delete, each held to one row, so one a raw write moved runs the command
   again; an update with no values writes nothing); the deadline changes, in
   order, skipping a deleted row's (an arm deletes the key's ready and
   claimed messages and inserts one due at its time, a clear deletes them);
   and, for each row a write deletes, every message of the row and, on a
   hard delete, each of its uses' sub-table rows (SQLite enforces no foreign
   key unless the connection turned it on). A soft-deleted row keeps its
   sub-table rows. A row a change command found gone (`Gone`) it applies in
   the row writes' place, with no fence and no count expected: the row's
   deadline messages are deleted unless a live row of its key exists, and
   its sub-table rows unless any row of its key does, so a soft-deleted
   row keeps its sub-table rows and a row that came back keeps everything.
   It writes each event of the change set to its row's table's event queue
   (the layout's `events`), in the change set's order, after the sub-table
   rows and before the deadline changes, with the command's time, a deleted
   row's included (D104); it refuses as `internal`, with nothing applied, an
   event of a row whose table keeps no event queue.
   No change set writes an audit column: one that names `createdBy` among
   a write's own fields is refused as `internal`, as any field the layout
   holds no own column for is. A change set with no writes, an operation
   its use settled or one that said `unchanged()`, applies nothing.
6. It returns `changes.result`.

For a `subRows` read it selects the use's sub-table rows of the row by the
column that holds the row's key, the key's column and then the layout's
fields, in key order (`COLLATE "C"` on Postgres), each converted as an
owned column is. On a store whose locks are fenced (SQLite, D1) a
`subRows` read made while the command holds a lock is refused as
`internal` (`a read of sub-table rows under a lock has no check on SQLite
or D1 yet (D103)`): the batch has no check of it yet.

For a `linked` read it selects the type's rows whose own column for `field`
holds the key (bound as that column binds a value, a UUID hyphenated), the
key column first, live rows alone, after `after` when given (bound as the
key column binds a key), ordered by the key column, `LIMIT limit + 1`; each
row converts as a row read converts it, and `more` is whether the extra row
came back. A type the layout lacks answers no rows; a field that is no own
field of it is refused as `internal` (`rules: <Type> has no own field
<field> in the behaviors' layout`). On SQLite and D1 one made under a lock is
refused as `internal`, as a `subRows` read is.

A `holding` read is a linked read by a use's owned column: the layout's
`uses[id].fields[name].column` for `useId` and `field`, `WHERE <column> =
$1 [AND <key> > $2] ORDER BY <key> LIMIT limit + 1`, the value bound as a
command's value of that field binds, live rows alone. A type the layout
lacks answers no rows; a use or a field the layout lacks is refused as
`internal`, and so is one made under a lock on SQLite and D1.

A command's `preconditions` pass through the adapter as the command's
member, from the repository's option to the core: the adapter maps no
name and checks nothing of them.

An upsert's insert applies no change set of the core's, so it binds what
the core's create of the row gives it: the adapter runs the create to its
change set and applies nothing (`prepareCreate`), and the repository
inserts the row with the owned fields that change set holds. A row the
unique key already holds the statement leaves as it is, and the
repository then updates it through the core's update, as its `updateOne`
does, so every use's guards hold an upsert's update (D104).

A row's version is the column `@optimistic` keeps, `_version`, which a
trigger bumps on every update (`BEFORE UPDATE` on Postgres, `AFTER
UPDATE` on SQLite and D1); a table that composes a behavior gets it.
The version is what the core holds each written row to, so a write that
another transaction made since the read, an ORM write or a raw one, moves
the row and runs the command again. A lock key is the behavior's logical
name for what a rule across rows serializes; the adapter never parses it.

Whether a deadline is still armed is the host's to check, never the
core's (D103): the queue message is the only record of an arming, and the
core holds no deadline state. A deadline command runs at the later of the
host's clock and `at`, so a worker whose clock lags the database's never
runs one before it falls due in the core. With a worker's claim of the
message, the unit's spend is the check: right after the command's own row
write and before the change set's deadline changes, it deletes the claimed
message by its id and claim token, held to one row. An arm or a clear that
another command made since deleted it, so the spend finds no row and the
unit applies nothing; the adapter then reads the claim, and a claim no
longer held is lost (the run reports `claim lost`), while one still held
runs again. An arming the fired command makes again (a snooze of a fired
deadline, a `then` that re-enters a state) is inserted after the spend and
survives it. Without a claim (the reference host, a transcript), the
adapter reads the arming before each run: none, or one armed at another
time, refuses the command `not_found` (`Ticket <key> has no deadline
Timers.respond armed at <at>`), and one whose `at` falls after the
command's time `invalid_argument` (`deadline Timers.respond of Ticket <key>
falls due at <at>, after <now>`), in the reference host's words; the unit
then spends the live message armed at `at`, held to one row. A deadline
whose row or use the core finds gone (`not_found`) has its arming spent in
a unit of its own (the run reports `gone`). Each adapter's deadline entry
(`runDeadline`, `RunDeadline`, `run_deadline`) runs a claimed message so,
in the transaction that completes it.

With a claim, a deadline command a guard vetoes (`vetoed`: a guard holding
the fire) releases its message rather than failing it: ready again, due in
60 seconds (`VETOED_RETRY_SECONDS`), its claim cleared and the attempt the
claim counted given back, in the store's transaction, with nothing of the
run applied, so it comes back every minute for as long as the guard holds
it, as the engine's sweep tried it at every run, and its sub-table row stays
pending. The run reports `vetoed` with the refusal, or `claim lost` when the
release finds the claim gone, and the handler logs it as it logs a lost
claim. Any other refusal, `busy` and a failure of the database among them,
fails the message as before: due again after D68's backoff, dead after its
last attempt. Without a claim (a transcript) the veto comes back as it is.

Each adapter's change entry (`runChange`, `RunChange`, `run_change`) runs a
message of a table's change queue as its trigger wrote it: the row's key, in
the core's form or its column's (a UUID's base62 or hyphenated text); the
write, whatever else than `insert`, `update` or `delete` refused as
`internal`; its time; and the images, each a JSON object by column or null,
a SQLite TEXT parsed first, each column the layout maps converted as a row
read converts it (on Postgres the JSON value as a `to_jsonb` cell is; on
SQLite a string as TEXT, an integer as INTEGER, any other number as REAL,
`true` and `false` as 1 and 0), other members ignored and a null absent. It
runs at the host's clock. With a worker's claim, the unit deletes the
claimed message by its id and token, held to one row, after the fenced row
writes and before the sub-table rows and deadlines: a unit that does not
apply because the claim no longer holds the message runs nothing (`claim
lost`), and one that does not apply for a row that moved runs again.
Without a claim nothing is spent. A refusal comes back whole, nothing of it
applied, and the worker's claim loop fails the message (D68).

Every adapter replays the recorded scenarios as transcripts:
`superschematic-rules-testkit` writes `core-workflow.json`,
`core-timers.json`, `core-reactions.json`, `core-reactions-changes.json`,
`core-constants.json`, `core-staterules.json` and `core-approvals.json`,
and the hand-written `core-workflow-principals.json` and
`core-freezes-and-sign-offs.json`
(`runtime/engine/testdata/rules/scenarios`, D104: cases the engine
recorded none of, written in the recorded format and never re-recorded),
out as `runtime/engine/testdata/rules/<name>.transcript.json` (its
`transcript` module is the format), the core's commands and what each must
answer, through the replayer's own mapping, and an adapter's tests replay
them against tables of their own on SQLite and on Postgres. The testkit's
`tests/transcript.rs` fails when a checked-in file is not what the writer
writes (`UPDATE_TRANSCRIPTS=1` rewrites them), and holds each to a floor:
`core-workflow` to 96 steps, 61 define and publish checks, 34 commands and
one raw write, in the 14 scenarios a core with no test fixtures runs;
`core-timers` to 139 steps, 54 checks and 85 commands, 12 of them deadlines
and 29 changes a runner ran, in 13 scenarios, the one whose fire a
StateRules guard vetoes among them; `core-reactions` to 586
steps, 110 checks and 476 commands, 252 of them changes, in 26 scenarios;
`core-reactions-changes` to 60 steps, 12 checks and 48 commands, 24 of
them changes, in 4 scenarios; `core-constants` to 39 steps, 19 checks and
20 commands, in 4 scenarios; `core-staterules` to 76 steps, 33 checks and
43 commands, in 7 scenarios; `core-approvals` to 122 steps, 29 checks and
93 commands, 71 of them operations and 5 changes, in 9 scenarios;
`core-workflow-principals` to 92 steps,
34 define and publish checks, twelve of them refused, and 58 commands, five
of them repeats answered unchanged, in 9 scenarios (its tenth, test.Gate's,
runs on the reference host alone); `core-freezes-and-sign-offs` to 115
steps, 40 checks and 75 commands, in 12 scenarios; and, of the behaviors
only the core implements (D104, step 3), `core-seats` to 88 steps, 22
checks and 66 commands, four of them repeats, in 7 scenarios, and
`core-reconcile` to 58 steps, 22 checks and 36 commands, two of them
repeats and two the stale deadline, in 7 scenarios whose Workflow and
Reconcile are anchored on fields, and `core-schedules` to 161 steps, 29
checks, 13 of them refused defines, 131 commands, 10 of them repeats and
late reports answered unchanged, 19 deadlines and 10 changes a runner
ran, and one raw write, in 8 scenarios, each fire with the events it
wrote (`expect.events`). Every transcript's `behaviors` lists the
behaviors the core runs, in its order (`superschematic_rules_behaviors::NAMES`). A hand-written file's transcript holds
each command's row version after the step (`expect.version`), which an
adapter compares with the version it reads back, so a repeat is held to
writing nothing; a recording's holds none. A change a runner's sweep ran is a command
step whose action is `change`, under the step of the `runner.runDue` that
ran it. A transcript's `expect` holds the command's row's sub-table rows
(`subRows`, by use id and table, each table's rows in key order), its armed
deadlines (`deadlines`, by use id and name), and the other rows its change
set wrote (`others`, each its type, key and row as read back after the
step, absent when it was deleted), which an adapter compares on every
command step. A type's `fields` list an engine Links link the replay maps to
a to-one relation as its `<link>Id` field, and its versions' specs carry the
relation. An adapter adds a sub-table and a deadline queue to each type's
tables as the transcript module says, and runs each command over a layout
of every type live in its database, each at the version the scenario last
made live (a check whose spec equals one of the type's versions, or a
command on it), since a step names its own type's version alone.
TypeScript's replay is the HTTP runtime's `src/rules-transcript.test.ts`,
held to the same floors on bun:sqlite and on Postgres; Go's is
`runtime/rules/go`'s `transcript_test.go`, on SQLite and on Postgres, and
Rust's `../rules-adapter/tests/transcript.rs`, on rusqlite and on Postgres,
each replaying the five files. A change step runs as the transcript writes
it, never from an adapter's own change queue.

## The layout document

The generator writes, for each table that composes behaviors (by
`implements` or on a field), where the core's names live
(`internal/generator/behaviorlayout`, which holds the layout rule in Go),
and the generated ORM embeds it: the table, its key column and IR type, the
version column, a soft-deletable table's `deletedAt` column (`live`), its
own fields' columns, each use's id, behavior, member (the field of a field
use, the storage key in camel case of an implements use: the name its
operations sit under), anchor (a field use's field) and owned columns
(each declared field by its declared name, its IR field, its column, its
IR type and how it converts), each use's operations and sub-tables
(`tables`), the table's deadline queue (`deadlines`) when its behaviors
arm deadlines, its change queue (`changes`) when its writes are heard, and
the type as the core runs it, in `TypeSpec`'s JSON, its own fields among it
(`spec.fields`), a to-one relation's with its `relation`. For an `Order`
whose `status` is a `Workflow`:

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
          "member": "status",
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
        "uses": [{ "behavior": "Workflow", "config": { "states": ["draft", "paid"], "transitions": [{ "from": "draft", "to": "paid" }] }, "anchor": { "field": "status" } }],
        "fields": [{ "name": "title", "type": "string" }]
      }
    }
  }
}
```

A table that implements Timers (`Ticket implements Timers<Ticket, { ... }>`)
has a use with no anchor and a sub-table, and its deadline queue:

```json
{
  "id": "Timers",
  "behavior": "Timers",
  "member": "timers",
  "fields": [],
  "operations": [{ "name": "fireTimer", "writes": true }, { "name": "resetTimer", "writes": true }, { "name": "snoozeTimer", "writes": true }],
  "tables": [
    {
      "name": "deadlines",
      "table": "ticket_timers_deadlines",
      "row": "ticket_id",
      "key": { "name": "name", "field": "name", "column": "name", "type": "string" },
      "fields": [
        { "name": "baseAt", "field": "baseAt", "column": "base_at", "type": "Temporal.DateTime", "convert": "epochMillis" },
        { "name": "armedFrom", "field": "armedFrom", "column": "armed_from", "type": "string" },
        { "name": "dueAt", "field": "dueAt", "column": "due_at", "type": "Temporal.DateTime", "convert": "epochMillis" },
        { "name": "fired", "field": "fired", "column": "fired", "type": "boolean" },
        { "name": "firedAt", "field": "firedAt", "column": "fired_at", "type": "Temporal.DateTime", "convert": "epochMillis" },
        { "name": "outcome", "field": "outcome", "column": "outcome", "type": "string" }
      ]
    }
  ]
}
```

```json
"deadlines": { "queue": "TicketDeadline", "table": "ticket_deadline_queue", "rowKey": "row_key", "use": "use_id", "name": "name", "at": "at", "payload": "payload" }
```

A table whose writes are heard (Timers and Reactions hear them, and so does
another table's Reactions through a to-one relation of this one) has its
change queue's columns:

```json
"changes": { "queue": "TicketChange", "table": "ticket_change_queue", "rowKey": "row_key", "write": "write", "at": "at", "before": "before", "after": "after" }
```

and a to-one relation's own field names its relation in `spec.fields`:

```json
{ "name": "projectId", "type": "Identity.UUID", "relation": { "name": "project", "type": "Project" } }
```

A table with a `createdBy` that is a plain scalar column (D87) has it in
`audit`, after `own`, and listed in `spec.fields` after the own fields,
when a Workflow transition's `by` or a StateRules state's `by` names it,
or an Approvals gate excludes the creator (`excludeCreator` absent or
true), whose `creator_cannot_sign` reads it (D104):

```json
"audit": [{ "field": "createdBy", "column": "created_by", "type": "Identity.UUID" }]
```

An adapter reads each audit column into a row's own fields under its
field, converted as an own column is (a UUID to base62, NULL absent), in
every row read, the linked read and a change command's included, and never
writes one: a create's own fields, an update's patch and a change set carry
none, and a write's image (`ImageColumns`) leaves it out, so no hearing
rule hears it. `behaviorlayout.AuditFields` is the Go form. A relation, a
list or another audit field (`updatedBy`, `deletedBy`, the audit times) is
not in it.

A layout of a table with neither is as it was. A sub-table's `row` is the
column holding the row's key, which binds as the row table's key binds; its
`fields` leave the key out. `spec.fields` lists
the own fields with `dateTime` set for one whose column converts as
`epochMillis`, and an implements use's spec entry is `{ "behavior":
"Timers", "config": { ... }, "anchor": "type" }`.

A declared field's column type is its declaration's `type` (Workflow's
`status` a `string`, `enteredAt` a `Temporal.DateTime`), and a date-time is
epoch milliseconds in the core: the adapter binds and reads a `Date` on
Postgres and the text `Date.prototype.toISOString` writes on SQLite. The
declaration's `value` names the field that takes the anchor field's own
name and column.

The own columns are the table's columns but its key, its version, its
owned fields, its audit fields (`createdBy` is read as `audit`, above) and
its internal metadata; a to-one
relation's column holds its target's key and is named as a generated row
holds it, `<field>Id`, with the target key's type. The generated `orm.ts`
embeds the document as `<database>Rules` and passes it to the HTTP
runtime's ORM in its definition's `rules`; the generated Go ORM embeds it
in `rules.go`, parsed once as `RulesLayout`, which its repositories run
the adapter over. A list or a map has no `convert`: no adapter converts
its elements, so a list of date-times reaches the core as its column holds
it.

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
`tests/core.rs` (53 cases) runs the core through its public API with toy behaviors: the
read exchange, vetoes, a call between two behaviors, two uses of one
behavior on two fields and the own writes their fields refuse, a field
anchor on a behavior with no value field, a config change that reads
whether rows exist, the define check's uses, shapes and rules, create
parameters, locks and the answers made before them, deadlines, a call on
another type's row and a create of one, the keys a behavior makes, a source
that refuses, a run that never settles and the defects the core holds a
behavior to; and the sub-table rows a run reads with its own writes applied,
`unchanged()` (no write, no after-change hook, a defect beside a write,
nothing in a call), an arm by a behavior that arms none, a sub-row value or
key a table does not declare, and `read_fields`; and the change command:
the hearing uses on the row as it is, a gone row's `Gone` with no hook run,
a listener reached through a relation by the row now, after and before, a
gone target passed over, a linked read paging by key, a heard write fenced
with its after-change hooks, a change that writes nothing, `Core::listeners`
and the JSON of the action, the read and `Gone`; and `settle` answering an
operation before any guard with nothing written, refusing before any
guard, and a settled `call_on` writing nothing on the other row; and a
command's preconditions (held to the uses before the row is read, refused
on any other action, read by a guard of the command's own write and by no
call), a holding read, `read_fields_at`, and another row's sub-table rows as
the run has them (D104); and a call made as the caller's own request, told
so and presented the command's preconditions, from a handler and from a
deadline's (D104, step 3); and an emit of a kind the declaration lists
landing in the change set with its row, use and payload, and one of a
kind it does not list, by a behavior that declares events and by one that
declares none, a defect (D104, step 3's second part).
`rules-behaviors`' `tests/workflow.rs` (34), `tests/timers.rs` (18),
`tests/reactions.rs` (16), `tests/constants.rs` (11), `tests/staterules.rs`
(11), `tests/approvals.rs` (14), `tests/lease.rs` (16) and
`tests/retries.rs` (14), and `tests/seats.rs` (19), `tests/reconcile.rs`
(12) and `tests/schedules.rs` (18), hold each behavior to its declaration's schemas and its rules: Workflow's `by` on a string, a UUID in either form and a
field with no value, `by` with a permission, `overridePermission`, a call
held to `by`, `repeat` and `from`, and its config checks; Timers'
starts, its `then`'s four outcomes, `until` in both forms, reset, snooze,
the Workflow it reads (implied, named, several, none), a field start as a
number and as text, a heard write the core applied changing nothing, a raw
write of a field start arming it again, a raw insert arming a create
deadline from its `at`, and a deadline reconciling its row before it fires;
Reactions' when kinds firing and not, each `then` that leaves its target as
it is, a `forbidden` move refusing the command, each config message, the
cycle check and `listens`; Constants' `oneWay` up and down for a boolean, a
number and a date-time, no value below every value, the permission past it,
a create and a value of another shape passing; StateRules' `by` on a string
field and on `createdBy`, a patch naming the caller not counting, required
held for the subject, a move its Workflow refuses left to Workflow, and the
`status` of a type with two Workflows; and Approvals' `resetOn` changed,
unchanged and `[]`, `resetOnChange`, `pinned` with a matching, stale and
missing `version`, a version on a gate that is not pinned, the creator read
from `createdBy`, entering a gate's state, a move by `Cx::call`, a removed
gate's sign-offs consumed at the next write, `listSignOffs`' cursor, and a
Timers `then` vetoed by `missing_fields` and by `approvals_missing`; Lease's
acquire, heartbeat (renewed by the last renewal's length, capped by the
longest hold), release, abandon, expire's reasons, its expiry's deadline
armed, moved, fired and vetoed, directives, the fencing token presented as
the command's precondition (stale on a free row, passed for a read-only
operation, required, absent in a call), a use's call of its own operation
held as the caller's, a raised hold after a cut renewal, `active` at a
read's time and a row with no token; and Retries' caps, limitsField,
keepBest and stuckAfter, a backoff's wait, backing_off on a move into
retryInto and on Lease's acquire, then's deadline (armed again at a
status's entry, a veto falling due again, another principal's lease, the
window), giveUpAfterMs, the window and its until, keyField read lazily and
reset by an update, the Workflow among two, and its composition with
Lease; Seats' take and assign, a full seat refused and displaced (ties by
key, never the fromField's subject), a repeat and a leave of a seat not
held settled before any guard, held_by_field, a subject kept in base62, a
UUID fromField in either form, listSeats' order, a dropped seat and a
lowered max, a leased row, and a Workflow transition's and a StateRules
state's `by` naming a seat; and Reconcile's config along transitions, the
fewest transitions and their tie, an older and an equal observation
settled before any guard, drift on each use's veto and on `forbidden`,
each step the observer's own request, `no_path` and `no_status`, the stale
deadline armed, moved, cleared, fired, armed again past a raw write and
vetoed, the worker's stale moves, a Workflow on the type and one of two
fields; and Schedules' config checks in order, its parameters, each
settle refusal in its order, a set of each kind with its deadline, a
repeat, a replace and a finished name set again, pause, resume past its
time and cancel, the fire of each kind (an overdue every once at its grid,
an after finishing, a cron line with no next run), a stale fire, a due a
raw write moved, a retry before and after the next run, dropped and
cancelled, a revived after and its refusal at max, an older run and a run
not had, a name's runs counted on across a replace and a set again and a
late report of a replaced schedule's run or of an earlier attempt
changing nothing, endOn through a transition, a heard write and a fire,
pruning, and every `run` payload held to the declaration's payloadSchema.
`tests/calendar.rs` (17) holds the calendar to Vixie cron's fields, names,
steps and day rule, its macros and each refusal, the spring gap, its
first minute, and the autumn fold in America/New_York and Europe/London,
a gap's first minute in four zones more, a leap day, a month without the
day, a line that never runs, its closest runs around the clock and across
the dates its day fields name, the zones' names, and a source that reads
no clock; `tests/calendar_zones.rs` (1) holds a zone to the bundled
database whatever `TZDIR` holds, which CI runs again with jiff's
`tzdb-zoneinfo` on, as a project's Cargo graph may build it.

`superschematic-rules-testkit`'s `tests/workflow_scenarios.rs`,
`tests/timers_scenarios.rs`, `tests/reactions_scenarios.rs`,
`tests/constants_scenarios.rs`, `tests/staterules_scenarios.rs` and
`tests/approvals_scenarios.rs` replay `core-workflow.json` on Workflow,
`core-timers.json` on Workflow, StateRules and Timers,
`core-reactions.json` and `core-reactions-changes.json` on Workflow,
Reactions and Timers, and `core-constants.json`, `core-staterules.json`
and `core-approvals.json` on the six through the reference host, and fail
on any step that answers otherwise than the engine, or when fewer steps
are checked than their floors (154, 346, 443, 65, 45, 88 and 151).
`tests/lease_scenarios.rs` replays the work queue's `workqueue-lease.json`,
`workqueue-lease-timers.json` and `workqueue-lease-approvals.json` (floors
610, 24 and 25; `workqueue-lease-direct-on.json` is skipped whole, for
Branches and Revisions), and `tests/retries_scenarios.rs`
`workqueue-retries.json` (290), with the configs' sweepMs left out, Comments
incidental to the Lease file, and the kit's `test.Gate` and `test.Signal`;
`tests/retries_waits_scenarios.rs` the hand-written `core-retries-waits.json`
(D104, step 2) with a floor of 159.
`tests/workflow_principals_scenarios.rs` replays the hand-written
`core-workflow-principals.json` (`runtime/engine/testdata/rules/scenarios`,
D104) on Workflow and the test behavior `test.Gate`, whose guard vetoes
every Workflow transition, with a floor of 122 steps, its opens and reads
among them, and `tests/freezes_and_sign_offs_scenarios.rs` the
hand-written `core-freezes-and-sign-offs.json` (D104, step 2) with a floor
of 138. A recorded type that lists Approvals and has no `createdBy` field
is given one, which each create sets to its caller's subject and which no
comparison reads, since the engine kept an instance's creator beside it. The reference host keeps the outbox:
every write of a type whose writes are heard, the core's and a raw one,
queues a change, and the replay's `runner.runDue` drains the changes through
the core, runs the due deadlines and drains again. An engine Links link with
one target is a to-one relation, its link and unlink updates of it; what a
table cannot express (a link that holds a list, holds and revised, the value
store, a runner's depth limit) is skipped, each with its reason, and
`core-reactions-holds-revised.json` waits for step 4. `tests/changes.rs`
holds the outbox to the core. The reference host keeps each event a
change set writes with its command's time (`Outboxed.at`), the replayer
holds every step, of every file, to the events its commands wrote (a
step's `events`, D104), and a transcript holds them as each command's
`expect.events`; `tests/schedules_scenarios.rs` replays the hand-written
`core-schedules.json` (D104, step 3) with a floor of 159, its one raw write
applied and not compared. `cargo test -p superschematic-rules-testkit`
runs them, and `tests/transcript.rs` with them.

TypeScript's binding and adapter:

```
cd runtime/rules/typescript && bun install && bun run test
cd runtime/http/typescript && bun test src/rules*.test.ts
```

The first builds the module with the workspace's `dist` profile (one
codegen unit, LTO, no symbol table: about 2.1 MB, 640 KB gzipped, since
Schedules' calendar bundles the IANA time zone database, D104; 1.66 MB
before), copies it into `dist/`, and runs the loader's
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

Go's binding and adapter:

```
make rules-archive
cd runtime/rules/go && go vet ./... && go test -race -count=1 ./...
scripts/engine-link-check.sh
```

The first stages the archive (`scripts/rules-archive.sh`, cargo's release
build of `../rules-wasm` as a staticlib). The second runs the binding from
many goroutines at once, the adapter's and the stores' tests and the
transcript's replay on SQLite, and on Postgres as well when
`SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` names one, each in a schema it
creates and drops. The third links the four archives a Go server links,
superscalar's, the version graph's, the engine's and this one, into one
binary, after checking that each defines its own C functions alone. The
generated Go ORM is held to the adapter by
`go test ./internal/generator/ormgen -run TestWorkflowTablesOnPostgres`:
it generates `fixture-workflow-db`'s Go types, ORM and DDL, and builds and
tests the ORM module over the staged archive on the Postgres
`SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL` names, and
`TestTimersTablesOnPostgres` does the same for `fixture-timers-db`'s
Timers: its sub-table rows, its deadline queue, `RunDeadline` and
`RunChange` over the table's real change trigger.

Rust's adapter:

```
cd runtime/engine/rust
cargo clippy -p superschematic-rules-adapter --all-targets --all-features -- -D warnings
cargo test -p superschematic-rules-adapter --all-features
```

runs its tests over a store in memory, its stores' tests and the
transcript's replay on rusqlite, and on Postgres as well when
`SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` names one.
