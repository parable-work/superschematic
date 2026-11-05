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

A step's answer holds what the replayer compares: a record's id, `data`
and `behaviors`, an operation's result, an event's kind and change, and a
refusal whole (its class, code, message and details), in the core's words.
What only the engine's store answers (sequence numbers, versions,
timestamps, the schema document a define echoes back) is left out. A
behavior entry that names a `field` (`{ "name": "Reconcile", "field":
"observed", "config": { ... } }`, the IR's `BehaviorRef.field`) is a use
anchored on that field, whose owned fields a record holds under the
field's name (`behaviors.observed`); one without is anchored on the type.
A test behavior a file composes is the test kit's fixture
(`runtime/engine/rust/rules-testkit/src/fixtures.rs`), registered for the file by its name.

`runtime/engine/rust/rules-testkit/tests/workflow_principals_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/freezes_and_sign_offs_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/retries_waits_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/periods_and_presence_scenarios.rs`,
`runtime/engine/rust/rules-testkit/tests/seats_scenarios.rs` and
`runtime/engine/rust/rules-testkit/tests/reconcile_scenarios.rs` replay each file on
the reference host, and `runtime/engine/rust/rules-testkit/tests/transcript.rs` writes each
one's transcript to `../<name>.transcript.json` with
`hand_written_transcript`, which also holds each command's row version
after the step (`expect.version`), so an adapter holds a command that
writes nothing to leaving the row's `_version` where it was. Rewrite the
transcript after changing a file:

```
UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript
```
