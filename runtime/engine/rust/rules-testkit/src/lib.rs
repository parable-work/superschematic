//! The pure core's reference host (D103): the adapter every language writes
//! once, here over memory instead of a database, which answers the core's
//! reads, takes its locks, applies its change sets and runs the version
//! loop, and the replayer that holds the core and the ported behaviors to
//! the recorded scenarios (`runtime/engine/testdata/scenarios`).
//!
//! [`MemoryHost`] is the host: a [`superschematic_rules::Core`] and the
//! [`Store`] it answers from and applies to, the store keeping each type's
//! defined and published versions, its rows, their sub-table rows, the
//! outbox, the armed deadlines and the change log. It knows no behavior.
//!
//! [`replay`] maps a recording's engine calls onto a host and compares what
//! the core and its behaviors decide: a record's own and behavior fields,
//! an operation's result, an event's change, and every refusal whole, the
//! define and publish refusals of a behavior's config with them, written
//! as the engine's error. It reads a file through the engine's test kit
//! ([`superschematic_engine_testkit::load`]), runs what a test wrote in
//! TypeScript where [`fixtures`] ports it, skips what only the engine's
//! store answers with its reason, and returns a [`Report`]. [`ported`] is
//! the core it runs the corpus on.
//!
//! ```
//! use serde_json::json;
//! use superschematic_rules::{Action, Caller, Command, Core, TypeSpec};
//! use superschematic_rules_testkit::MemoryHost;
//!
//! let alice = Caller::new("alice", Vec::<String>::new());
//! let mut host = MemoryHost::new(Core::new());
//! host.define(TypeSpec::new("Note"), &alice, 1_000).unwrap();
//! host.publish("Note", &alice, 1_000).unwrap();
//! let created = host
//!     .execute(
//!         "Note",
//!         Command::new(
//!             alice.clone(),
//!             1_000,
//!             Action::Create {
//!                 id: Some("n1".into()),
//!                 own: json!({"body": "hi"}).as_object().cloned().unwrap(),
//!                 params: Default::default(),
//!             },
//!         ),
//!     )
//!     .unwrap();
//! assert_eq!(created.version, Some(1));
//! assert_eq!(host.store().row("Note", "n1").map(|row| row.version), Some(1));
//! ```

pub mod fixtures;
pub mod host;
pub mod replay;
pub mod shape;
mod symbols;

pub use host::{
    Applied, ApplyError, Armed, DeadlineKey, Logged, MemoryHost, Outboxed, Store, StoredRow,
    SubRowKey, TypeVersions, MAX_ATTEMPTS,
};
pub use replay::{replay, replay_path, scenario_path, Mismatch, Report, ScenarioReport, Skipped};

use superschematic_rules::Core;

/// The core the replayer runs the recorded scenarios on: every behavior
/// ported onto the core so far, as `superschematic-rules-behaviors`
/// registers them. A scenario that composes a behavior it does not run is
/// skipped, and the replay tests hold the number of checked steps to a
/// floor, so a behavior missing here fails them rather than passing them
/// by skipping.
pub fn ported() -> Core {
    superschematic_rules_behaviors::core()
}
