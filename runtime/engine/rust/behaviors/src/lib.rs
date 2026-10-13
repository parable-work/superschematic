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
//! [`Constants`] and [`Variants`]; wave 2a (milestone 9a), [`Revisions`],
//! [`Dependencies`], [`Links`] (with D56's many-valued links, and
//! [`revised`], what a move of a link's target moves on) and [`Numbering`].
//! The other core behaviors come in the waves after ([`NAMES`] lists the
//! ones [`core`] registers).

pub mod comments;
pub mod constants;
pub mod dependencies;
mod guidance;
pub mod links;
pub mod numbering;
pub mod revised;
pub mod revisions;
pub mod variants;
pub mod workflow;

pub use comments::{CommentRecord, Comments};
pub use constants::{Constants, ConstantsConfig};
pub use dependencies::{BlockerRecord, Dependencies, DependenciesConfig};
pub use links::{link_many, link_pin, LinkPin, LinkSpec, Links, LinksConfig};
pub use numbering::{Numbering, NumberingConfig};
pub use revised::{linked_to, moved_on, target_move, TargetMove};
pub use revisions::{Revisions, RevisionsConfig, RevisionsReview};
pub use variants::{Variants, VariantsConfig};
pub use workflow::{
    is_terminal_state, state_outcome, Workflow, WorkflowConfig, WorkflowOutcome, WorkflowTransition,
};

use superschematic_engine::BehaviorSet;

/// The core behaviors [`core`] registers, in its order: the TypeScript
/// engine's order (`coreBehaviors`), without the ones not ported yet.
pub const NAMES: [&str; 8] = [
    "Workflow",
    "Comments",
    "Revisions",
    "Dependencies",
    "Links",
    "Constants",
    "Variants",
    "Numbering",
];

/// The core's behaviors this crate has ported, in the order the TypeScript
/// engine registers them: what a host passes as `EngineOptions::behaviors`,
/// extended with its own.
pub fn core() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Dependencies)
        .with(Links)
        .with(Constants)
        .with(Variants)
        .with(Numbering)
}
