//! The core's behaviors (D16) for the Rust engine (D58): one module per
//! behavior, each the port of `src/behaviors/core/<name>.ts` with its
//! guidance, its migrations on both databases, its config, guard, fields,
//! operations and veto codes, so a schema that composes one runs as it does
//! on the TypeScript engine.
//!
//! Each behavior embeds its declaration from `declarations/`, the copy
//! `make behaviors` writes from the core registry
//! (`internal/registry/behaviors`) and CI checks (`make behaviors-check`),
//! so the engine and the compiler read one declaration. A behavior reaches
//! the engine only through the plug-in API an extension's behavior uses,
//! and another behavior only through its operations.
//!
//! The TypeScript engine registers every core behavior when it opens; the
//! Rust engine cannot depend on this crate, so a host passes [`core`] in
//! its options, ahead of its own behaviors:
//!
//! ```no_run
//! use superschematic_engine::{Engine, EngineOptions, Policy};
//!
//! let mut options = EngineOptions::sqlite("engine.db");
//! options.policy = Some(Policy::allow_all());
//! options.behaviors = superschematic_engine_behaviors::core();
//! let engine = Engine::open(options)?;
//! # Ok::<(), superschematic_engine::Error>(())
//! ```
//!
//! Ported so far: wave 1 (milestone 8), [`Workflow`], [`Comments`],
//! [`Constants`] and [`Variants`]; wave 2 (milestone 9), [`Revisions`],
//! [`Dependencies`], [`Links`] (with D56's many-valued links, and
//! [`revised`], what a move of a link's target moves on), [`Numbering`],
//! and D56's [`StateRules`], [`Approvals`] and [`Timers`]; wave 3
//! (milestone 10), [`Rollups`] and [`Reactions`] (with D56's changes
//! trigger), and [`Search`], over the engine's own full-text search; and
//! milestone 11, [`Branches`], the version graph's Rust engine over its
//! fixed layout in the call's transaction ([`graph`]): all fifteen, in
//! [`NAMES`], which [`core`] registers.

pub mod approvals;
pub mod branches;
pub mod comments;
pub mod constants;
pub mod dependencies;
pub mod graph;
mod guidance;
pub mod links;
pub mod numbering;
pub mod reactions;
pub mod revised;
pub mod revisions;
pub mod rollups;
pub mod search;
pub mod staterules;
pub mod timers;
pub mod variants;
pub mod workflow;

pub use approvals::{Approvals, ApprovalsConfig, ApprovalsGate};
pub use branches::{
    uuid_v5, Branches, BranchesConfig, BranchesKind, BranchesParent, BranchesSweep, BranchesUnit,
    ACTOR_NAMESPACE, DEFAULT_PRIMARY, ROOT_NAMESPACE,
};
pub use comments::{CommentRecord, Comments};
pub use constants::{Constants, ConstantsConfig};
pub use dependencies::{BlockerRecord, Dependencies, DependenciesConfig};
pub use links::{link_many, link_pin, LinkPin, LinkSpec, Links, LinksConfig};
pub use numbering::{Numbering, NumberingConfig};
pub use reactions::{
    Reactions, ReactionsChanges, ReactionsConfig, ReactionsField, ReactionsRevisedLink,
    ReactionsRollup, ReactionsRule, ReactionsTerminal, ReactionsThen, ReactionsWhen,
};
pub use revised::{linked_to, moved_on, target_move, TargetMove};
pub use revisions::{Revisions, RevisionsConfig, RevisionsReview};
pub use rollups::{RollupFunction, RollupSpec, Rollups, RollupsConfig, MAX_ROLLUP_READ};
pub use search::{Search, SearchConfig, SearchVectors, RRF_K, RRF_WINDOW, SIMILAR_TERMS};
pub use staterules::{StateRule, StateRules, StateRulesConfig};
pub use timers::{
    parse_date_time, Timers, TimersConfig, TimersDeadline, TimersStart, TimersThen, TimersUntil,
};
pub use variants::{Variants, VariantsConfig};
pub use workflow::{
    is_terminal_state, state_outcome, Workflow, WorkflowConfig, WorkflowOutcome, WorkflowTransition,
};

use superschematic_engine::BehaviorSet;

/// The core behaviors [`core`] registers, in its order: the TypeScript
/// engine's order (`coreBehaviors`), all fifteen.
pub const NAMES: [&str; 15] = [
    "Workflow",
    "Comments",
    "Revisions",
    "Dependencies",
    "Links",
    "Rollups",
    "Search",
    "Reactions",
    "Constants",
    "Variants",
    "Branches",
    "StateRules",
    "Approvals",
    "Numbering",
    "Timers",
];

/// The core's behaviors, in the order the TypeScript engine registers
/// them: what a host passes as `EngineOptions::behaviors`, extended with
/// its own.
pub fn core() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Dependencies)
        .with(Links)
        .with(Rollups)
        .with(Search)
        .with(Reactions)
        .with(Constants)
        .with(Variants)
        .with(Branches)
        .with(StateRules)
        .with(Approvals)
        .with(Numbering)
        .with(Timers)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// core() registers what NAMES lists, in its order: what the Node-API
    /// addon reads to tell the core's of its behaviors from the rest.
    #[test]
    fn core_registers_every_name_in_order() {
        assert_eq!(core().names(), NAMES);
    }

    /// A selection keeps the set's order, whatever order the names come in.
    #[test]
    fn a_selection_keeps_the_sets_order() {
        let picked = core().select(|name| name == "Timers" || name == "Comments");
        assert_eq!(picked.names(), ["Comments", "Timers"]);
        assert!(core().select(|_| false).is_empty());
    }
}
