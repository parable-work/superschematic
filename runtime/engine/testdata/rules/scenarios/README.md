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

A step's answer holds what the replayer compares: a record's id, `data`
and `behaviors`, an operation's result, an event's kind and change, and a
refusal whole (its class, code, message and details), in the core's words.
What only the engine's store answers (sequence numbers, versions,
timestamps, the schema document a define echoes back) is left out. A
test behavior a file composes is the test kit's fixture
(`runtime/engine/rust/rules-testkit/src/fixtures.rs`), registered for the file by its name.

`runtime/engine/rust/rules-testkit/tests/workflow_principals_scenarios.rs` replays each file on
the reference host, and `runtime/engine/rust/rules-testkit/tests/transcript.rs` writes each
one's transcript to `../<name>.transcript.json` with
`hand_written_transcript`, which also holds each command's row version
after the step (`expect.version`), so an adapter holds a command that
writes nothing to leaving the row's `_version` where it was. Rewrite the
transcript after changing a file:

```
UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript
```
