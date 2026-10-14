//! The work queue's behaviors (D16) for the Rust engine (D58): one module
//! per behavior, each the port of `runtime/engine-workqueue/typescript/src/<name>.ts`
//! with its guidance, its migrations on both databases, its config, guard,
//! fields, operations, schedules and veto codes, so a schema that composes
//! one runs as it does on the TypeScript engine with
//! `@superschematic/engine-workqueue` registered.
//!
//! Each behavior embeds its declaration from `declarations/`, the copy
//! `make behaviors` writes from the core registry
//! (`internal/registry/behaviors`, `--package
//! @superschematic/engine-workqueue`) and CI checks (`make
//! behaviors-check`), so the engine and the compiler read one declaration.
//! The behaviors reach the engine only through the plug-in API an
//! extension's behavior uses, and the core's behaviors only through their
//! operations and the helpers the TypeScript package imports from
//! `@superschematic/engine` (`superschematic_engine_behaviors`: Workflow's
//! terminal states, Links' pins, `revised`).
//!
//! The core declares them under bare names, so every binary's meta-schema
//! admits a schema that composes them, but an engine runs them only once a
//! host registers them, after the core's:
//!
//! ```no_run
//! use superschematic_engine::{Engine, EngineOptions, Policy};
//!
//! let mut options = EngineOptions::sqlite("jobs.db");
//! options.policy = Some(Policy::allow_all());
//! options.behaviors =
//!     superschematic_engine_behaviors::core().extend(superschematic_engine_workqueue::workqueue());
//! let engine = Engine::open(options)?;
//! # Ok::<(), superschematic_engine::Error>(())
//! ```
//!
//! Milestone 12 ports them in two parts, which [`behavior`] answers for as
//! each lands; [`workqueue`] registers the ones ported, in [`NAMES`]'
//! order.

mod guidance;

// Milestone 12a: Lease, Assignment, Presence and Retries.

// Milestone 12b: Queue, Budget and Blueprint.

use superschematic_engine::BehaviorSet;

/// Every work-queue behavior, in the order the TypeScript package's
/// `workQueueBehaviors` registers them, which [`workqueue`] keeps.
pub const NAMES: [&str; 7] = [
    "Lease",
    "Assignment",
    "Queue",
    "Presence",
    "Blueprint",
    "Budget",
    "Retries",
];

/// Registers one behavior in a set.
type Register = fn(BehaviorSet) -> BehaviorSet;

/// The behaviors ported so far, by name, in any order: [`workqueue`]
/// registers them in [`NAMES`]' order.
const PORTED: &[(&str, Register)] = &[
    // Milestone 12a.

    // Milestone 12b.
];

/// One work-queue behavior by name, for a host that registers only some
/// (the TypeScript package exports each one by one); `None` for a name
/// that is none of [`NAMES`], or one not ported yet.
pub fn behavior(name: &str) -> Option<BehaviorSet> {
    PORTED
        .iter()
        .find(|(known, _)| *known == name)
        .map(|(_, register)| register(BehaviorSet::new()))
}

/// Every work-queue behavior ported, in [`NAMES`]' order: what a host
/// passes in `EngineOptions::behaviors` after the core's
/// (`superschematic_engine_behaviors::core`).
pub fn workqueue() -> BehaviorSet {
    NAMES
        .iter()
        .filter_map(|name| behavior(name))
        .fold(BehaviorSet::new(), BehaviorSet::extend)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_name_is_a_declaration_of_the_package() {
        for name in NAMES {
            let path = format!(
                "{}/declarations/{name}.behavior.json",
                env!("CARGO_MANIFEST_DIR")
            );
            assert!(std::path::Path::new(&path).is_file(), "{path}");
        }
        assert!(behavior("Workflow").is_none());
        assert!(workqueue().len() <= NAMES.len());
    }
}
