//! The checked-in transcripts of the recorded scenarios the core runs
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, D103),
//! Workflow's, Timers', Reactions' two, Constants', StateRules',
//! Approvals', and Lease's, Presence's, Budget's and Retries' (`core-lease`,
//! `core-presence`, `core-budget` and `core-retries`, of the work queue's
//! `workqueue-lease.json`, `workqueue-presence.json`, `workqueue-budget.json`
//! and `workqueue-retries.json`):
//! the core's commands and what each must answer, which every language's adapter replays against tables of
//! its own, Lease's expireHolder among them as a command without a row
//! (D104). The test fails when a file is not what the writer writes from
//! its recording, so a change to the core, a behavior or the replayer that
//! changes an answer changes the file in the same commit.
//! `UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test
//! transcript` rewrites them; review the diff before keeping them. So are
//! the transcripts of the hand-written scenario files
//! (`runtime/engine/testdata/rules/scenarios`, D104), which hold each
//! command's row version beside what it answers, Seats', Reconcile's,
//! Schedules' and Pool's (`core-seats`, `core-reconcile`, `core-schedules`,
//! `core-pool`) among them, behaviors no engine implements, Schedules' and
//! Pool's holding the events each command wrote, and Pool's its picks, each
//! a command without a row (D104), Aliased's (`core-aliased`, D147), its
//! resolves commands without a row, and Recurrence's (`core-recurrence`,
//! #601), its occurrences as sub-rows and its deadlines' events.

use std::collections::BTreeMap;
use std::fs;

use superschematic_engine_testkit::{load, ScenarioFile};
use superschematic_rules::Core;
use superschematic_rules_testkit::{
    declarations_of, hand_written_path, hand_written_transcript, hand_written_transcript_declaring,
    ported, scenario_path, transcript, transcript_path, Transcript, TranscriptStep,
};

/// The steps `core-workflow.transcript.json` holds, of `core-workflow.json`'s
/// 159: each one the reference host maps onto the core (61 define and
/// publish checks, 34 creates, updates, deletes and invokes, and the test's
/// raw write of Workflow's column) in the 14 scenarios the core an adapter
/// embeds runs. The two scenarios that need a fixture (test.Dispatch, and a
/// permission matcher of the test's own) hold none; the rest of the 63 left
/// out are opens, reads of the host's, the outbox's reads, the two writes
/// the store's own-field validation refuses, and the define the engine
/// refuses for `finished`, a table's Workflow's alone, which the core takes
/// (D113). An adapter's replay holds its own count to this one, so a
/// regression cannot pass by skipping.
const WORKFLOW_FLOOR: usize = 96;

/// The steps `core-timers.transcript.json` holds, of `core-timers.json`'s
/// 420: 54 define and publish checks and 85 commands, 44 creates, updates,
/// invokes and a delete, the 12 deadlines the runner's sweeps ran and the
/// 29 changes they ran, each a write of a row of a type Timers hears, each
/// under the step number of the `runner.runDue` that ran it, in the 13
/// scenarios the core an adapter embeds runs, the one that composes
/// StateRules among them (D104, step 2): a fire whose move StateRules
/// vetoes is recorded vetoed. A change runs Timers' reconcile on the row
/// as it is, which writes nothing for a write the core made, deletes
/// nothing but the deadlines of a row that is gone, and, in one scenario,
/// cancels a deadline whose until a version published after the write
/// added, before it falls due. Five scenarios hold none: two compose a test
/// behavior (test.Bounce, test.Hold), which the reference host runs as a
/// fixture; one's Reactions rule names a field Timers computes at a read,
/// which a table's rule waits for a later step to hear; one's access policy
/// is the test's own; and one names namespaces, which the reference host
/// does not keep. The rest of the 307 left out are opens, reads of rows, of
/// the change log and of the runner's status, the sweeps that ran nothing,
/// the describes, and the steps from a publish whose Timers change the
/// engine rewrote rows for. An adapter's replay holds its own count to this
/// one.
const TIMERS_FLOOR: usize = 139;

/// The steps `core-reactions.transcript.json` holds, of
/// `core-reactions.json`'s 601: 114 define and publish checks and 504
/// commands, 238 creates, updates, invokes and a delete, the engine's link
/// and unlink among them as updates of a relation's column, and the 266
/// changes the runner's sweeps ran, each a write of a row of a type
/// Reactions hears or listens to, each under the step number of the
/// `runner.runDue` that ran it, with the rows a rule moved as its `others`,
/// the scenario that composes Dependencies among them since the core runs
/// it (D104's step 4). The rest of the 194 left out are opens, reads of rows
/// and of the event log, the sweeps that ran nothing, and what the replay
/// skips: the defines that name Links, the runner's status, and the steps
/// after a sweep whose recording failed. An adapter's replay holds its own
/// count to this one.
const REACTIONS_FLOOR: usize = 618;

/// The steps `core-reactions-changes.transcript.json` holds, of
/// `core-reactions-changes.json`'s 151: 14 define and publish checks and 48
/// commands, 24 creates, updates, invokes and a delete and the 24 changes
/// the runner's sweeps ran. The scenario beside Revisions holds its first
/// define and publish, and stops at the define of a rule on
/// Revisions.pendingProposals, which a table computes at a read. The rest
/// of the 102 left out are what the replay skips (a test behavior, the
/// value store, a runner's depth limit), opens, reads of rows and of the
/// event log. An adapter's replay holds its own count to this one.
const REACTIONS_CHANGES_FLOOR: usize = 62;

/// The steps `core-workflow-principals.transcript.json` holds, of the
/// hand-written `core-workflow-principals.json`'s 133 (D104, D113): 38
/// define and publish checks, fourteen of them refused, and 63 commands,
/// creates and transitions, five of them repeats answered unchanged, each
/// holding the version of its row's command before it, in the 10 scenarios
/// the core an adapter embeds runs, the one of a state `finished` names
/// among them (D113). test.Gate's scenario holds none, since the reference
/// host runs test.Gate as a fixture; the rest of the 32 left out are the
/// opens, the reads of rows and the read of the event log. An adapter's
/// replay holds its own count to this one.
const WORKFLOW_PRINCIPALS_FLOOR: usize = 101;

/// The steps `core-constants.transcript.json` holds, of
/// `core-constants.json`'s 65: 23 define and publish checks and 26 creates,
/// updates and operations, in the four scenarios that compose Constants
/// alone or with Workflow and the one that composes Comments and Revisions,
/// whose operation's update() Constants holds (D103's step 4). The one that
/// composes Variants, which the core does not run, holds none; the rest of
/// the 16 left out are opens and reads of rows and of the types the host
/// keeps. An adapter's replay holds its own count to this one.
const CONSTANTS_FLOOR: usize = 49;

/// The steps `core-staterules.transcript.json` holds, of
/// `core-staterules.json`'s 126: 39 define and publish checks and 49
/// creates, updates, transitions and operations, in its 11 scenarios, the
/// type named apart from its schema (schema `invoices`, type `Invoice`)
/// checked under the type's name for a define's refusal. The two that
/// compose test.Recorder hold its operation, which writes the row's own
/// fields through `update()` and may move the status in the same call, and
/// the two that compose Revisions hold its writes beside StateRules' (D103,
/// step 4); the 38 left out are opens, reads of rows and of the event log,
/// the describes, and the define the engine's requires check refused. An
/// adapter's replay holds its own count to this one.
const STATERULES_FLOOR: usize = 88;

/// The steps `core-approvals.transcript.json` holds, of
/// `core-approvals.json`'s 198: 33 define and publish checks and 113
/// commands, 108 creates, updates, a delete, transitions and the three
/// operations, and the 5 changes the runner's sweep ran for the Reactions
/// rule that moves a row into a gated state, in ten scenarios, each
/// recorded type given the own field createdBy the engine kept beside an
/// instance. The scenario that composes Comments and Revisions runs since
/// the core runs Comments (D103's step 4), and the new-version one stops
/// at the publish that removes a gate on a type with rows, whose sign-offs
/// the engine consumed there and the core consumes at each row's next
/// write; the rest of the 52 left out are
/// opens, reads of rows and of the event log, and the define the engine's
/// requires check refused. An adapter's replay holds its own count to this
/// one.
const APPROVALS_FLOOR: usize = 146;

/// The steps `core-freezes-and-sign-offs.transcript.json` holds, of the
/// hand-written `core-freezes-and-sign-offs.json`'s 138 (D104): 40 define
/// and publish checks, fourteen of them refused, and 75 creates, updates,
/// transitions and sign-offs, each holding its row's version after the
/// step, in its 12 scenarios; none is a repeat. The 23 left out are the
/// opens and the reads of rows. An adapter's replay holds its own count to
/// this one.
const FREEZES_AND_SIGN_OFFS_FLOOR: usize = 115;

/// The steps `core-lease.transcript.json` holds, of `workqueue-lease.json`'s
/// 691 (D104, step 2): 120 define and publish checks and 354 commands,
/// creates, updates, deletes and invokes, 86 of them presenting Lease's
/// token as the command's precondition, the 3 expiries the runner's runDue
/// ran as deadlines, each under the step number of the `runner.runDue` that
/// ran it, and expireHolder's 8 calls, each a command without a row
/// (`tableOperation`, D104) with the rows it expired as its `others`, in
/// the 44 scenarios the core an adapter embeds runs, the configs with
/// sweepMs left out, the scenario that composes Dependencies among them
/// since the core runs it (D104's step 4), and Comments' posts beside the
/// row since the core runs Comments (D103's step 4). Of the 13 steps
/// expireHolder's runs brought in: in "expire says why" its call and the
/// acquire after it; in "a lapsed holder's" its call; in "notRenewedAfter
/// spares" its two calls and the refused expire after them; in
/// "expireHolder expires every lease" its refused call, its call, its call
/// that expires none, the second engine's define and publish, the create
/// and the refused call. The three that compose test.Gate or test.Signal,
/// which the reference host runs as fixtures, hold none (31 steps); the
/// rest of the 217 left out are opens, reads of rows and of the event log,
/// the runDues that ran nothing, and what the replay skips: the engines
/// with a value store, the reads of the engine's store and the options the
/// reference host does not take. An adapter's replay holds its own count to
/// this one.
const LEASE_FLOOR: usize = 474;

/// The steps `core-presence.transcript.json` holds, of the engine's
/// `workqueue-presence.json`'s 226 (D104): 52 define and publish checks,
/// eight of them refused, and 89 commands, 30 creates, 48 operations
/// (beats, misses, transitions and Lease's acquires and heartbeats), 2
/// updates, a delete and the 8 deadlines the runner's sweeps ran, each a
/// miss under the step number of the `runner.runDue` that ran it, three of
/// them expiring the principal's leases through Lease's expire with the
/// Job rows as their others, in 17 of its 18 scenarios. The one whose
/// defines name releaseLeases' schemas holds none, since the engine refused
/// it for another schema's state, which the build holds. The rest of the
/// 85 left out are opens, reads of rows, of the event log and of the
/// runner's status, the sweeps that ran nothing, and the steps after a
/// sweep whose recording failed. An adapter's replay holds its own count to
/// this one.
const PRESENCE_FLOOR: usize = 141;

/// The steps `core-budget.transcript.json` holds, of the engine's
/// `workqueue-budget.json`'s 796 (D104): 164 define and publish checks,
/// seven of them refused, and 331 commands, 99 creates, 162 operations, 68
/// updates (the engine's link and unlink among them, as updates of a
/// relation's column) and 2 deletes, 46 of them writing the scopes they
/// reach as their others, in the 31 scenarios that compose no Queue, with
/// each row's meters, holds and buckets as its sub-rows. The five claimed
/// through Queue hold none, and the two limitField scenarios stop at their
/// setLimit of a meter whose limit an own field holds; the rest of the 301
/// left out are opens, reads of rows, of the event log and of the engine's
/// store, Links' own validation and the defines that name Links. An
/// adapter's replay holds its own count to this one.
const BUDGET_FLOOR: usize = 495;

/// The steps `core-retries.transcript.json` holds, of
/// `workqueue-retries.json`'s 379 (D104, step 2): 54 define and publish
/// checks and 196 commands, creates, updates and invokes, one of them
/// presenting Lease's token, in the 14 scenarios the core an adapter embeds
/// runs. The rest of the 129 left out are the scenarios that compose Queue,
/// the five whose config gives resultField from their define on, the define
/// the engine's requires check refused, opens, and reads of rows and of the
/// event log. An adapter's replay holds its own count to this one.
const RETRIES_FLOOR: usize = 250;

/// The steps `core-retries-waits.transcript.json` holds, of the
/// hand-written `core-retries-waits.json`'s 159 (D104): 36 define and
/// publish checks, seventeen of them refused, and 98 commands, creates,
/// updates, deletes, transitions, attempts and Lease's operations, 23 of
/// them presenting a precondition, and the 3 deadlines the runner's runDue
/// ran (two of then's moves, a lease's expiry), each holding its row's
/// version after the step, in its 10 scenarios; none is a repeat. The 26
/// left out are the opens, the reads of rows and the runDues that ran
/// nothing. An adapter's replay holds its own count to this one.
const RETRIES_WAITS_FLOOR: usize = 134;

/// The steps `core-periods-and-presence.transcript.json` holds, of the
/// hand-written `core-periods-and-presence.json`'s 118 (D104): 25 define
/// and publish checks, eleven of them refused, and 52 commands (creates,
/// updates, Budget's operations, Presence's beats and miss, transitions and
/// the one deadline the runner's sweeps ran), each holding its row's
/// version after the step, with Budget's meters and buckets as their
/// sub-rows and Presence's miss as its armed deadline, in its 8 scenarios;
/// none is a repeat. The 41 left out are the opens, the reads of rows and
/// the sweeps that ran nothing. An adapter's replay holds its own count to
/// this one.
const PERIODS_AND_PRESENCE_FLOOR: usize = 77;

/// The steps `core-seats.transcript.json` holds, of the hand-written
/// `core-seats.json`'s 97 (D104, step 3): 22 define and publish checks,
/// nine of them refused, and 66 commands, creates, updates (one a
/// fromField's refused seat_full), transitions and Seats' operations, four
/// of them repeats answered unchanged, each holding its row's version after
/// the step, with its occupants as its sub-rows, in its 7 scenarios. The 9
/// left out are the opens and the reads of rows. An adapter's replay holds
/// its own count to this one.
const SEATS_FLOOR: usize = 88;

/// The steps `core-reconcile.transcript.json` holds, of the hand-written
/// `core-reconcile.json`'s 76 (D104, step 3): 22 define and publish checks,
/// nine of them refused, and 36 commands, creates, an update, Lease's
/// acquire, observations and the 2 deadlines the runner's runDue ran (a
/// stale move to unreachable, and one a guard vetoed), two of them repeats
/// answered unchanged, each holding its row's version after the step, with
/// its stale deadline as its armed deadline, in its 7 scenarios, whose
/// Workflow and Reconcile are anchored on fields. The 18 left out are the
/// opens, the reads of rows and the runDues that ran nothing. An adapter's
/// replay holds its own count to this one.
const RECONCILE_FLOOR: usize = 58;

/// The steps `core-schedules.transcript.json` holds, of the hand-written
/// `core-schedules.json`'s 160 (D104, step 3): 29 define and publish
/// checks, thirteen of them refused, and 131 commands: 102 creates,
/// transitions and Schedules' operations, 27 of them refused and ten
/// repeats and late reports answered unchanged, and the 19 deadlines and 10
/// changes the runner's runDues ran, each fire with the event run it wrote
/// as its `events`, each holding its row's version after the step, with its
/// schedules as its sub-rows and its armed deadlines, and the raw write of
/// a status its change queue carries, in its 8 scenarios. The 10 left out
/// are the opens and the two runDues that ran nothing. An adapter's replay
/// holds its own count to this one.
const SCHEDULES_FLOOR: usize = 161;

/// The steps `core-pool.transcript.json` holds, of the hand-written
/// `core-pool.json`'s 91 (D104, step 3): 17 define and publish checks,
/// seven of them refused (the last a removal of Pool from a type that has
/// rows, which the reference host refuses at its define), and 65 commands:
/// 12 creates, 22 picks, each a command without a row (`tableOperation`)
/// whose `expect` holds its result alone, 27 of Pool's and Budget's
/// operations of a row, one of them markOut's repeat answered unchanged,
/// and the 4 rechecks the runner's runDues ran, each with the event
/// recheck it wrote as its `events`, each command on a row holding its
/// version after the step, in its 5 scenarios. The 9 left out are the
/// opens, the read of a row and the three runDues that ran nothing. An
/// adapter's replay holds its own count to this one.
const POOL_FLOOR: usize = 82;

/// The steps `core-recurrence.transcript.json` holds, of the hand-written
/// `core-recurrence.json`'s 100 (#601, #729): its define and publish checks, eleven
/// of them refused, and its commands: creates, updates, Recurrence's
/// operations, refusals and repeats answered unchanged among them, and the
/// deadlines the runner's runDues ran, each fire with the events it wrote
/// as its `events`, each holding its row's version after the step, with its
/// occurrences as its sub-rows and its armed deadlines, in its 5 scenarios.
/// The ones left out are the opens and the runDues that ran nothing. An
/// adapter's replay holds its own count to this one.
const RECURRENCE_FLOOR: usize = 101;

/// The steps `core-rollups.transcript.json` holds, of `core-rollups.json`'s
/// 286 (D104's step 4): 72 define and publish checks and 108 creates,
/// updates (the engine's link and unlink as updates of a relation's
/// column), transitions, 21 of them refused, among them the gate's
/// not_held, and a delete, in the 12 scenarios the core an adapter embeds
/// runs. The rest of the 106 left out are opens, reads of rows, which an
/// adapter's replay does not compute values for, and what the replay skips:
/// the scenario whose access policy is the test's own, the two that stop at
/// a latest rollup, and the defines that read another schema. An adapter's
/// replay holds its own count to this one.
const ROLLUPS_FLOOR: usize = 180;

/// The steps `core-dependencies.transcript.json` holds, of
/// `core-dependencies.json`'s 395 (D104's step 4): 107 define and publish
/// checks and 159 commands, 85 creates (a create's blockers among them), 70
/// operations (addBlocker, removeBlocker, listBlockers and transitions the
/// gate holds), 35 of the commands and checks refused, and 4 deletes, in
/// the 14 scenarios the core an adapter embeds runs. The rest of the 129
/// left out are opens, reads of rows and of the event log, and what the
/// replay skips: the scenarios whose access policy is the test's own, the
/// one of test behaviors, listDependents, and the requires check. An
/// adapter's replay holds its own count to this one.
const DEPENDENCIES_FLOOR: usize = 266;

/// The steps `core-numbering.transcript.json` holds, of
/// `core-numbering.json` (D103's step 4): its define and publish checks and
/// its creates, updates, deletes and assignNumbers, each create's number
/// taken from the use's shared table `counts`, which an adapter keeps as a
/// table of its own and holds each write of to its version. The rest left
/// out are what the replay skips (a scenario whose test batched its creates
/// in a function of its own, test.Refuser's own-field refusals, the defines
/// that name Links) and opens and reads. The scenario beside Revisions
/// holds its proposal and approval, a write of the row's own fields that
/// keeps the number. An adapter's replay holds its own count to this one.
const NUMBERING_FLOOR: usize = 104;

/// The steps `core-lease-numbering.transcript.json` holds, of
/// `workqueue-lease-numbering.json`: a create numbered under another
/// principal's lease, and assignNumber held by Lease's guard until its
/// `exempt` names it.
const LEASE_NUMBERING_FLOOR: usize = 14;

/// The steps `core-revisions.transcript.json` holds, of
/// `core-revisions.json`'s 210 (D103's step 4): 37 define and publish
/// checks and 75 creates, updates, deletes and invokes, in its 16
/// scenarios: each revision a row of the use's sub-table `revisions`, each
/// proposal one of `proposals`, and each approval an operation that writes
/// the row's own fields through an update's rules, held by test.Hold's
/// guard while the row is held. The 98 left out are what the replay skips
/// (the scenarios with an access policy, the proposes the store's own-field
/// validation refuses, a read of the event log after an update that changed
/// nothing), opens and reads. An adapter's replay holds its own count to
/// this one.
const REVISIONS_FLOOR: usize = 112;

/// The steps `core-comments.transcript.json` holds, of
/// `core-comments.json`'s 502 (D103's step 4): 45 define and publish checks
/// and 316 creates, deletes, invokes and deadlines, in its 24 scenarios:
/// each message, edit and reaction a row of a sub-table of the use, each
/// conversation's high-water marks a row of its shared table, and each
/// write beside the row, so the row's version stays; streamed replies, each
/// composing message's draft a sub-table of chunks and its idle time a
/// deadline whose fire is beside the row too (D77's primitive 2, 2 deadline
/// commands); read state, each reader's marker, follow and delivery a row
/// of a sub-table; and each message, edit, reaction and resolve recording
/// the subject its caller acted for. The 141 left out are what the replay
/// skips (from the first claim of deliveries or read of the inbox on,
/// search, anchors and attachments, the scenarios with an access policy,
/// the reads of the event log that hold a write beside the row), opens and
/// reads. An adapter's replay holds its own count to this one.
const COMMENTS_FLOOR: usize = 361;

/// The steps `core-comments-beside.transcript.json` holds, of the
/// hand-written `core-comments-beside.json`'s 29 (D103's step 4): 6 define
/// and publish checks and 19 commands, each with the row's version after
/// it, which a write beside the row leaves where the lease's acquire put
/// it. The 4 left out are the opens and the reads of the host.
const COMMENTS_BESIDE_FLOOR: usize = 25;

/// The steps `core-derived-provenance.transcript.json` holds, of the
/// hand-written `core-derived-provenance.json`'s 88 (D73 on the core,
/// D103's step 4): 26 define and publish checks, twelve of them refused,
/// and 49 commands: 12 creates and updates, each keeping the row's
/// derivations and records, 22 of Derived's pulls, settles and failure
/// lists, each a command without a row (`tableOperation`) whose settle
/// writes the rows it reaches as its `others`, and 15 operations of a row
/// (applyDerived, rederive, Provenance's accept and reject, and the
/// Workflow moves its gate holds), each holding its row's version after
/// the step, with its derivations and records as its sub-rows, in its 7
/// scenarios; none is a repeat. The 13 left out are the opens and the
/// reads of rows. An adapter's replay holds its own count to this one.
const DERIVED_PROVENANCE_FLOOR: usize = 75;

/// The steps `core-holds.transcript.json` holds, of the engine's
/// `commerce-holds.json` (D70 on the core, D103's step 4).
const HOLDS_FLOOR: usize = 71;

/// The steps `core-holds-timers.transcript.json` holds, of the engine's
/// `commerce-holds-timers.json` (D70 on the core, D103's step 4).
const HOLDS_TIMERS_FLOOR: usize = 14;

/// The steps `core-commerce.transcript.json` holds, of the hand-written
/// `core-commerce.json` (D70 on the core, D103's step 4).
const COMMERCE_FLOOR: usize = 66;

/// The steps `core-bookings.transcript.json` holds, of the engine's
/// `commerce-calendar.json` (D70 on the core, D103's step 4): Calendar's
/// time held through a booking's Holds.
const BOOKINGS_FLOOR: usize = 83;

/// The steps `core-calendar.transcript.json` holds, of the hand-written
/// `core-calendar.json` (D70 on the core, D103's step 4).
const CALENDAR_FLOOR: usize = 47;

/// The steps `core-refunds.transcript.json` holds, of the engine's
/// `commerce-payments-refunds.json` (D70 on the core, D103's step 4): a
/// refund row's refundFor and settleRefundFor, each a call on the other
/// row, beside Approvals and Reactions.
const REFUNDS_FLOOR: usize = 52;

/// The steps `core-payments.transcript.json` holds, of the hand-written
/// `core-payments.json` (D70 on the core, D103's step 4).
const PAYMENTS_FLOOR: usize = 73;

/// The steps `core-blueprint.transcript.json` holds, of the engine's
/// `workqueue-blueprint.json`'s 405 (D149): 101 define and publish checks
/// and 50 commands, creates whose after-change hook creates each child in
/// the create's change set, its blockers Dependencies' create parameters,
/// updates, and Dependencies' listBlockers on the children. The rest of the
/// 254 left out are opens, reads of rows and of the event log, and what the
/// replay skips: the scenarios whose links pin, the one that composes
/// Queue, the one whose access policy is the test's own, the defines the
/// engine refused for what the child schema lacks, and the refusals that
/// name Links. An adapter's replay holds its own count to this one.
const BLUEPRINT_FLOOR: usize = 151;
/// The steps `core-aliased.transcript.json` holds, of the hand-written
/// `core-aliased.json`'s 54 (D147): its define and publish checks, two of
/// them refused, and its commands, creates (four refused for a name
/// another row holds), Aliased's operations, four of them repeats
/// answered unchanged, resolve's commands without a row, and a delete, 36
/// commands and 10 checks, each command holding its row's version after
/// the step, with its aliases as its sub-rows, in its 5 scenarios. The 8
/// left out are the opens and the reads of rows. An adapter's replay holds
/// its own count to this one.
const ALIASED_FLOOR: usize = 46;
/// The steps `core-bundles.transcript.json` holds, of the hand-written
/// `core-bundles.json`'s 81 (D103's step 5): the 12 define and publish
/// checks of each bundle's table, whose uses are the parts the loader
/// expands the bundle into, and 78 commands, each holding its row's
/// version after the step, the deadlines the runner's runDues ran and the
/// changes of the tables Timers hears among them, in its 4 scenarios,
/// Reservation's, Barrier's (its gate's exemptions among them, D104),
/// Mailbox's and Assignment's; none is a repeat. The 11 left out are the
/// opens, the reads of rows and a runDue that ran nothing. An adapter's
/// replay holds its own count to this one.
const BUNDLES_FLOOR: usize = 90;

#[test]
fn the_checked_in_transcripts_are_what_the_writer_writes() {
    // Each transcript by its name, from the recording of its source's name.
    for (name, source, floor) in [
        ("core-workflow", "core-workflow", WORKFLOW_FLOOR),
        ("core-timers", "core-timers", TIMERS_FLOOR),
        ("core-reactions", "core-reactions", REACTIONS_FLOOR),
        (
            "core-reactions-changes",
            "core-reactions-changes",
            REACTIONS_CHANGES_FLOOR,
        ),
        ("core-constants", "core-constants", CONSTANTS_FLOOR),
        ("core-staterules", "core-staterules", STATERULES_FLOOR),
        ("core-approvals", "core-approvals", APPROVALS_FLOOR),
        ("core-lease", "workqueue-lease", LEASE_FLOOR),
        ("core-presence", "workqueue-presence", PRESENCE_FLOOR),
        ("core-budget", "workqueue-budget", BUDGET_FLOOR),
        ("core-retries", "workqueue-retries", RETRIES_FLOOR),
        ("core-rollups", "core-rollups", ROLLUPS_FLOOR),
        ("core-dependencies", "core-dependencies", DEPENDENCIES_FLOOR),
        ("core-numbering", "core-numbering", NUMBERING_FLOOR),
        (
            "core-lease-numbering",
            "workqueue-lease-numbering",
            LEASE_NUMBERING_FLOOR,
        ),
        ("core-revisions", "core-revisions", REVISIONS_FLOOR),
        ("core-comments", "core-comments", COMMENTS_FLOOR),
        ("core-holds", "commerce-holds", HOLDS_FLOOR),
        (
            "core-holds-timers",
            "commerce-holds-timers",
            HOLDS_TIMERS_FLOOR,
        ),
        ("core-bookings", "commerce-calendar", BOOKINGS_FLOOR),
        ("core-refunds", "commerce-payments-refunds", REFUNDS_FLOOR),
        ("core-blueprint", "workqueue-blueprint", BLUEPRINT_FLOOR),
    ] {
        let written = written(name, &scenario_path(source), &transcript);
        let kinds = |kind: fn(&TranscriptStep) -> bool| {
            written
                .scenarios
                .iter()
                .flat_map(|scenario| &scenario.steps)
                .filter(|step| kind(step))
                .count()
        };
        println!(
            "{}: {} scenarios, {} steps ({} commands, {} checks, {} raw writes), {} left out",
            written.file,
            written.scenarios.len(),
            written.steps(),
            kinds(|step| matches!(step, TranscriptStep::Command { .. })),
            kinds(|step| matches!(step, TranscriptStep::Check { .. })),
            kinds(|step| matches!(step, TranscriptStep::Raw { .. })),
            written.skipped()
        );
        assert!(
            written.steps() >= floor,
            "{name}'s transcript holds {} steps, under the floor of {floor}",
            written.steps()
        );
    }
}

/// A hand-written file's transcript holds each command's row version after
/// the step, and a command a repeat answered unchanged holds the version of
/// its row's command before it: it wrote nothing. A file holds repeats, or
/// holds none, as its entry says.
#[test]
fn the_hand_written_transcripts_are_what_the_writer_writes() {
    for (name, floor, holds_repeats) in [
        ("core-workflow-principals", WORKFLOW_PRINCIPALS_FLOOR, true),
        (
            "core-freezes-and-sign-offs",
            FREEZES_AND_SIGN_OFFS_FLOOR,
            false,
        ),
        ("core-retries-waits", RETRIES_WAITS_FLOOR, false),
        (
            "core-periods-and-presence",
            PERIODS_AND_PRESENCE_FLOOR,
            false,
        ),
        ("core-seats", SEATS_FLOOR, true),
        ("core-reconcile", RECONCILE_FLOOR, true),
        ("core-schedules", SCHEDULES_FLOOR, true),
        ("core-pool", POOL_FLOOR, true),
        ("core-recurrence", RECURRENCE_FLOOR, true),
        ("core-comments-beside", COMMENTS_BESIDE_FLOOR, false),
        ("core-derived-provenance", DERIVED_PROVENANCE_FLOOR, false),
        ("core-commerce", COMMERCE_FLOOR, false),
        ("core-calendar", CALENDAR_FLOOR, false),
        ("core-payments", PAYMENTS_FLOOR, false),
        ("core-aliased", ALIASED_FLOOR, true),
        ("core-bundles", BUNDLES_FLOOR, false),
    ] {
        let written = written(name, &hand_written_path(name), &hand_written_transcript);
        let mut repeats = 0;
        for scenario in &written.scenarios {
            // The version each row's last command left it at.
            let mut last: BTreeMap<(String, String), Option<i64>> = BTreeMap::new();
            for step in &scenario.steps {
                let TranscriptStep::Command {
                    type_name,
                    command,
                    expect,
                    ..
                } = step
                else {
                    continue;
                };
                let at = (
                    type_name.clone(),
                    command.action.key().unwrap_or_default().to_owned(),
                );
                assert!(
                    expect.version.is_some() || expect.row.is_none(),
                    "{}: a command on a row that is there holds its version",
                    scenario.name
                );
                if expect.result.get("unchanged") == Some(&serde_json::Value::Bool(true)) {
                    repeats += 1;
                    assert_eq!(
                        Some(&expect.version),
                        last.get(&at),
                        "{}: a repeat of {} {} moved its version",
                        scenario.name,
                        at.0,
                        at.1
                    );
                }
                last.insert(at, expect.version);
            }
        }
        let checks = written
            .scenarios
            .iter()
            .flat_map(|scenario| &scenario.steps)
            .filter(|step| matches!(step, TranscriptStep::Check { .. }))
            .count();
        println!(
            "{}: {} scenarios, {} steps ({} commands, {} checks, {repeats} repeats), {} left out",
            written.file,
            written.scenarios.len(),
            written.steps(),
            written.steps() - checks,
            checks,
            written.skipped()
        );
        assert_eq!(
            repeats > 0,
            holds_repeats,
            "{name}'s transcript holds {repeats} repeats"
        );
        assert!(
            written.steps() >= floor,
            "{name}'s transcript holds {} steps, under the floor of {floor}",
            written.steps()
        );
    }
}

/// The steps `core-declarative.transcript.json` holds, of the hand-written
/// `core-declarative.json`'s 52 (D103's step 5): its define and publish
/// checks, five of them refused, and its creates, votes, flags, accepts,
/// summaries and updates, with the changes the runner's sweeps ran for
/// test.Votes' reactions, each holding its row's version; the opens and the
/// reads of rows are left out.
const DECLARATIVE_FLOOR: usize = 49;

/// A transcript of declarative behaviors (D103's step 5) holds each one's
/// declaration file whole, which a host hands its core before it replays
/// it, and names it among its behaviors after the core's own.
#[test]
fn a_declarative_transcript_holds_its_declarations() {
    let name = "core-declarative";
    let source = hand_written_path(name);
    let declarations = declarations_of(&source).unwrap_or_else(|error| panic!("{error}"));
    assert_eq!(
        declarations.len(),
        1,
        "core-declarative declares test.Votes"
    );
    let write = |file: &ScenarioFile, make_core: &dyn Fn() -> Core| {
        hand_written_transcript_declaring(
            file,
            make_core,
            &declarations,
            "runtime/engine/testdata/rules/scenarios/core-declarative.json",
        )
    };
    let written = written(name, &source, &write);
    assert_eq!(written.declarations, declarations);
    assert_eq!(
        written.behaviors.last().map(String::as_str),
        Some("test.Votes")
    );
    println!(
        "{}: {} scenarios, {} steps, {} left out",
        written.file,
        written.scenarios.len(),
        written.steps(),
        written.skipped()
    );
    assert!(
        written.steps() >= DECLARATIVE_FLOOR,
        "{name}'s transcript holds {} steps, under the floor of {DECLARATIVE_FLOOR}",
        written.steps()
    );
}

/// The steps `acme-rating.transcript.json` holds, of the acme example's
/// hand-written `acme-rating.json` (D103's step 5): acme.Rating as a
/// declaration with no crate, its define and publish checks and its
/// creates, rates and summaries, each holding its row's version.
const ACME_RATING_FLOOR: usize = 26;

/// acme.Rating's transcript (D103's step 5), written from the acme
/// example's scenarios beside its declaration
/// (`examples/acme-schematic/testdata/rules/acme-rating.json`), holds the
/// extension's declaration file whole, so every host replays it with no
/// file of the example's: the core's own transcripts' directory keeps it.
#[test]
fn acme_ratings_transcript_holds_its_declaration() {
    let name = "acme-rating";
    let source = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../examples/acme-schematic/testdata/rules/acme-rating.json");
    let declarations = declarations_of(&source).unwrap_or_else(|error| panic!("{error}"));
    let write = |file: &ScenarioFile, make_core: &dyn Fn() -> Core| {
        hand_written_transcript_declaring(
            file,
            make_core,
            &declarations,
            "examples/acme-schematic/testdata/rules/acme-rating.json",
        )
    };
    let written = written(name, &source, &write);
    assert_eq!(written.declarations, declarations);
    assert_eq!(
        written.behaviors.last().map(String::as_str),
        Some("acme.Rating")
    );
    assert!(
        written.steps() >= ACME_RATING_FLOOR,
        "{name}'s transcript holds {} steps, under the floor of {ACME_RATING_FLOOR}",
        written.steps()
    );
}

// The writer of a scenario file's transcript.
type Writer<'w> = &'w dyn Fn(&ScenarioFile, &dyn Fn() -> Core) -> Result<Transcript, String>;

// written writes a scenario file's transcript under a name, which names the
// file it is written to and its `file`, rewrites the checked-in file when
// UPDATE_TRANSCRIPTS=1, and holds the checked-in file to it.
fn written(name: &str, source: &std::path::Path, write: Writer) -> Transcript {
    let file = load(source).unwrap_or_else(|error| panic!("{error}"));
    let mut written = write(&file, &ported).unwrap_or_else(|error| panic!("{error}"));
    written.file = name.to_owned();
    let text = written.render();
    let path = transcript_path(name);
    if std::env::var("UPDATE_TRANSCRIPTS").is_ok_and(|value| value == "1") {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).expect("create the transcripts' directory");
        }
        fs::write(&path, &text).expect("write the transcript");
    }
    let checked_in = fs::read_to_string(&path).unwrap_or_default();
    assert!(
        checked_in == text,
        "{} is not what the writer writes from {name}.json; run UPDATE_TRANSCRIPTS=1 cargo test -p superschematic-rules-testkit --test transcript and review the diff",
        path.display()
    );
    written
}
