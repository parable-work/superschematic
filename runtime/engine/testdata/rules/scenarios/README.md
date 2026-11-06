# Hand-written scenarios

Cases of the core's behaviors that the engine recorded none of, written by
hand in the recorded format (`../../scenarios/README.md`), which
`superschematic-rules-testkit` reads and replays as it replays the engine's
recordings. D103 makes the recordings the specification; a behavior's
upgrade that the engine does not run (D104) gets its cases here, since no
engine answered them. These files are never re-recorded: no recorder
writes them, and `test/corpus.test.ts` does not list them.

- `core-workflow-principals.json` (D104, step 1): who makes a Workflow
  transition (`by`, and the `overridePermission` past it), a move to the
  state a row is in answered as no change (`repeat: "unchanged"`), and the
  state a caller read (`from`), which an engine type refuses.
- `core-freezes-and-sign-offs.json` (D104, step 2): Constants' fields that
  move one way (`oneWay`: booleans, numbers and date-times, up and down,
  the permission past it, a value cleared and a create), who may change a
  field a StateRules state freezes (`by`), and Approvals' gates whose
  sign-offs a change of chosen fields consumes (`resetOn`) and that pin the
  version their signer read (`pinned`, and a `version` on a gate that is
  not pinned), with each one's define refusals. An engine type refuses each.
- `core-retries-waits.json` (D104, step 2): Retries' attempts that wait
  (a class's `backoff`, and `retryInto`, `then`, `giveUpAfterMs`, `window`
  and `keyField`; `then`'s move armed again at a status's entry, held by
  another principal's active lease and by the window), Lease's acquire
  while a row backs off, and Lease's
  fencing token presented as the command's precondition on a table
  (`token_required`, `token_stale`), its expiry a deadline; with each one's
  define refusals. An engine type refuses each.
- `core-periods-and-presence.json` (D104, step 2): Budget's meters that
  start again at each period (`reset`: hourly, daily, weekly, monthly and
  `{ everyMs }`, in UTC), count a rolling window (`window: "rolling"`) and
  take the usage a provider reports (`observe`, `observePermission`), with
  setLimit refused for a meter whose limit an own field holds; and
  Presence's `ttlByState`, the deadline a beat and a transition arm by the
  row's state, its miss falling due as a deadline; with each one's define
  refusals. An engine type refuses each.
- `core-seats.json` (D104, step 3): Seats' seats, takes and leaves, a full
  seat refused and displaced, a field's subject seated and an update of the
  field held to the seat's `max`, and `by` naming a seat on a Workflow
  transition and a StateRules state, with `overridePermission`; with each
  one's define refusals.
- `core-reconcile.json` (D104, step 3): Reconcile's observations and the
  fewest transitions they move a Workflow field along, an older one, drift
  on a refused step and with no path, other uses' guards holding each
  step, the stale deadline to unreachable and back, and each one's define
  refusals. Its Workflow and Reconcile are anchored on fields (`field`).
  No engine implements either.
- `core-schedules.json` (D104, step 3): Schedules' named schedules on a
  row: setSchedule after a delay, at an interval and on a cron line in a
  time zone, bounded by minMs and maxMs and held to max, a repeat answered
  unchanged and a live name replaced, a replace and a finished name set
  again counting the name's runs on; a due one firing once on the runner,
  its event run, an overdue every at the next point of its grid and an
  after finishing; a cron line's spring gap, its first minute among it,
  and autumn fold in America/New_York and Europe/London, and a schedule
  set just before the repeated hour; pause, resume past its time and
  cancel; reportRun's outcomes, an older run, no_run, the retry outcome
  trying a run again as its next attempt, an after revived for it, and a
  late report of an earlier attempt or of a replaced schedule's run;
  endOn through a transition and through a raw write of the status its
  change queue carries; max, the finished schedules beyond it pruned, and a
  new version's narrower config; with each one's define refusals. No
  engine implements Schedules.
- `core-pool.json` (D104, step 3): Pool's members, each open, near its
  limit or out until a time: `pick`, an operation of the table as a whole,
  through each tier (the open members `prefer` keeps, then its near ones,
  then the other open and near ones, by `order` and then the key), with
  `match`, `prefer` and `exclude`, a null member of `match` absent, and
  with none open, its `until`;
  `markOut` with and without `until`, the later kept and a repeat answered
  unchanged; `reportRoom` before and after `outUntil`, with and without
  room, near and back; the recheck's deadline at `outUntil`, its fire's
  event `recheck` and its arming again every `recheckAfterMs`; a member
  whose Budget meter has no room counted out through the contract Capacity
  until its period rolls over or a provider's figure lapses; each refusal;
  and each one's define refusals. No engine implements Pool. A call of the
  table as a whole is the engine's `instances.invokeSchema` (`args: [type,
  operation, params]`), which the replayer runs as a command without a row
  on the one use whose behavior declares the operation on the table, and
  a transcript writes as a `command` step that names no row.

A step's answer holds what the replayer compares: a record's id, `data`
and `behaviors`, an operation's result, an event's kind and change, and a
refusal whole (its class, code, message and details), in the core's words.
What only the engine's store answers (sequence numbers, versions,
timestamps, the schema document a define echoes back) is left out. A
behavior entry that names a `field` (`{ "name": "Reconcile", "field":
"observed", "config": { ... } }`, the IR's `BehaviorRef.field`) is a use
anchored on that field, whose owned fields a record holds under the
field's name (`behaviors.observed`); one without is anchored on the type.
A step may carry `events`, the events its command, or every command a
`runner.runDue` ran, wrote about rows, in order, each `{ schema, id, use,
kind, at, payload }` (D104). The replayer compares them for every step of
every file, the recordings' too; a step without the member wrote none.
A test behavior a file composes is the test kit's fixture
(`runtime/engine/rust/rules-testkit/src/fixtures.rs`), registered for the file by its name.

`runtime/engine/rust/rules-testkit/tests/workflow_principals_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/freezes_and_sign_offs_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/retries_waits_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/periods_and_presence_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/seats_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/reconcile_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/schedules_scenarios.rs` and
`runtime/engine/rust/rules-testkit/tests/pool_scenarios.rs` replay each file on
the reference host, and `runtime/engine/rust/rules-testkit/tests/transcript.rs` writes each
one's transcript to `../<name>.transcript.json` with
`hand_written_transcript`, which also holds each command's row version
after the step (`expect.version`), so an adapter holds a command that
writes nothing to leaving the row's `_version` where it was, and a raw
write to moving it on by one, as `@optimistic`'s trigger does; and each
command's events (`expect.events`), which an adapter reads back from its
event queues after the step. Rewrite the transcript after changing a file:

```
UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript
```
