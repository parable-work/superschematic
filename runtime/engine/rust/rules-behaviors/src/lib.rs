//! The behaviors ported onto the pure core (D103): each one a
//! [`superschematic_rules::Behavior`] that reads and writes only logical
//! names, its owned fields and its sub-tables, and does no I/O. A host's
//! core is a [`superschematic_rules::Core`] built with the ones the project
//! composes.
//!
//! Each behavior reads its declaration from the file the compiler registers
//! and the engine embeds (`../behaviors/declarations`), in place, so the
//! engine and the core read one declaration while both exist. The core
//! runs no JSON Schema, so a behavior holds its config and its operations'
//! parameters to the declaration's schemas in its own code, in the words
//! the engine gave, which its tests hold to the file.
//!
//! Ported so far, in step 1 of D103's order of the work: [`Workflow`]. The
//! engine's behaviors (`superschematic-engine-behaviors`) stay beside these
//! until each is ported and the engine goes.
//!
//! A host builds its core with [`core`], as it passed the engine's
//! `behaviors::core()`, and adds its own behaviors:
//!
//! ```
//! use serde_json::json;
//! use superschematic_rules::{Action, Caller, Command, Outcome, Step, TypeSpec, UseSpec};
//!
//! let core = superschematic_rules_behaviors::core();
//! let order = TypeSpec::new("Order").with(UseSpec::on_type(
//!     "Workflow",
//!     json!({
//!         "states": ["draft", "open"],
//!         "transitions": [{ "from": "draft", "to": "open" }]
//!     }),
//! ));
//! let define = Command::new(Caller::new("alice", ["schemas"]), 1_000_000, Action::Define);
//! let step = core.run(&order, &define, &Default::default());
//! assert!(matches!(step, Step::Done(Outcome::Changed(_))));
//! ```

mod declared;
mod schema;
pub mod workflow;

pub use workflow::{
    entered_at_of, is_terminal_state, state_outcome, status_of, Workflow, WorkflowConfig,
    WorkflowOutcome, WorkflowTransition,
};

use superschematic_rules::Core;

/// The behaviors [`core`] registers, in its order: the engine's order
/// (`behaviors::NAMES`) of the ones ported so far.
pub const NAMES: [&str; 1] = [workflow::NAME];

/// The declaration file of each behavior [`core`] registers, in its order:
/// the text the compiler registers and each behavior reads in place, which
/// a binding hands its host whole (D103), each field's column type and the
/// value field with the rest.
pub const DECLARATIONS: [&str; 1] = [workflow::DECLARATION];

/// A core with every behavior ported so far, in the engine's order, and
/// the default permission matcher: what a host runs, extended with its own
/// behaviors (`.with(..)`) and a deployment's matcher (`.with_matcher(..)`).
pub fn core() -> Core {
    Core::new().with(Workflow)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// core() registers what NAMES lists, in its order.
    #[test]
    fn core_registers_every_name_in_order() {
        assert_eq!(core().names(), NAMES);
    }

    /// DECLARATIONS holds each registered behavior's file, in NAMES' order.
    #[test]
    fn declarations_are_the_registered_behaviors_files() {
        let named: Vec<String> = DECLARATIONS
            .iter()
            .map(|text| {
                let file: serde_json::Value = serde_json::from_str(text).expect("a file parses");
                file["name"].as_str().unwrap_or_default().to_owned()
            })
            .collect();
        assert_eq!(named, NAMES);
    }
}
