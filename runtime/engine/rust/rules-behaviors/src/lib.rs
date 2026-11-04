//! The behaviors ported onto the pure core (D103): each one a
//! [`superschematic_rules::Behavior`] that reads and writes only logical
//! names, its owned fields and its sub-tables, and does no I/O. A host's
//! core is a [`superschematic_rules::Core`] built with the ones the project
//! composes.
//!
//! Each behavior reads its declaration from the file the compiler registers
//! and the engine embeds (`../behaviors/declarations`, and the work queue's
//! `../../engine-workqueue/rust/declarations`), in place, so the engine and
//! the core read one declaration while both exist. The core
//! runs no JSON Schema, so a behavior holds its config and its operations'
//! parameters to the declaration's schemas in its own code, in the words
//! the engine gave, which its tests hold to the file.
//!
//! Ported so far: [`Workflow`], in step 1 of D103's order of the work, and
//! [`Timers`] and [`Reactions`], in step 3: Timers' deadlines a table keeps
//! in a sub-table and arms through the core, and both hear their row's
//! writes, whoever makes them, from the table's change queue, Reactions the
//! writes of the rows that point at its row too. Workflow's transitions name
//! who makes them, and a move to the state a row is in may answer as no
//! change, in step 1 of D104's ([`subjects`] is how a field names a
//! subject). [`Constants`], [`StateRules`] and [`Approvals`], in step 2 of
//! D104's order of the work, each with its upgrade: fields that move one
//! way, who may change a frozen field, and gates whose sign-offs a change
//! of chosen fields consumes and that pin the version their signer read.
//! [`Lease`] and [`Retries`], in step 2 of D104's order of the work: a
//! table's lease with its fencing token, presented as the command's
//! precondition, its expiry a deadline; and retries that wait between
//! attempts, give up after a while, cap the attempts in a window and count
//! per value of a field.
//! [`Presence`] and [`Budget`], in the same step, each with its upgrade: a
//! heartbeat whose deadline follows the row's state, its miss a deadline
//! that expires the principal's leases through Lease's own expire; and
//! budgets that keep periods, count a rolling window and take the usage a
//! provider reports.
//! The engine's behaviors
//! (`superschematic-engine-behaviors`) stay beside these until each is
//! ported and the engine goes.
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

pub mod approvals;
pub mod budget;
pub mod constants;
mod declared;
mod flow;
pub mod lease;
mod moves;
mod paging;
mod periods;
pub mod presence;
pub mod reactions;
pub mod retries;
mod schema;
pub mod staterules;
pub mod subjects;
pub mod timers;
mod values;
pub mod workflow;

pub use approvals::{Approvals, ApprovalsConfig, ApprovalsGate};
pub use budget::{Budget, BudgetConfig, BudgetMeter, BudgetReset, BudgetScope, INTEGER_SEGMENTS};
pub use constants::{
    Constants, ConstantsConfig, ConstantsOneWay, OneWayDirection, OneWayKind, ONE_WAY_BOOLEANS,
    ONE_WAY_NUMBERS,
};
pub use lease::{Lease, LeaseConfig, HOLD_TYPES};
pub use moves::StatusMove;
pub use presence::{Presence, PresenceConfig};
pub use reactions::{
    Reactions, ReactionsChanges, ReactionsConfig, ReactionsField, ReactionsRule, ReactionsTerminal,
    ReactionsThen, ReactionsWhen,
};
pub use retries::{
    Retries, RetriesConfig, RetriesKeepBest, RetriesWindow, RetryBackoff, RetryClass, LIMITS_TYPES,
};
pub use staterules::{StateRule, StateRules, StateRulesConfig};
pub use timers::{
    deadlines_field, parse_date_time, Timers, TimersConfig, TimersDeadline, TimersStart,
    TimersThen, TimersUntil,
};
pub use workflow::{
    entered_at_of, is_terminal_state, state_outcome, status_of, Workflow, WorkflowConfig,
    WorkflowOutcome, WorkflowRepeat, WorkflowTransition,
};

use superschematic_rules::Core;

/// The behaviors [`core`] registers, in its order: the engine's order
/// (`behaviors::NAMES`) of the ones ported so far.
pub const NAMES: [&str; 10] = [
    workflow::NAME,
    reactions::NAME,
    constants::NAME,
    staterules::NAME,
    approvals::NAME,
    timers::NAME,
    lease::NAME,
    presence::NAME,
    budget::NAME,
    retries::NAME,
];

/// The declaration file of each behavior [`core`] registers, in its order:
/// the text the compiler registers and each behavior reads in place, which
/// a binding hands its host whole (D103), each field's column type, the
/// value field, the sub-tables, whether it arms deadlines and whether it
/// hears its row's writes with the rest.
pub const DECLARATIONS: [&str; 10] = [
    workflow::DECLARATION,
    reactions::DECLARATION,
    constants::DECLARATION,
    staterules::DECLARATION,
    approvals::DECLARATION,
    timers::DECLARATION,
    lease::DECLARATION,
    presence::DECLARATION,
    budget::DECLARATION,
    retries::DECLARATION,
];

/// A core with every behavior ported so far, in the engine's order, and
/// the default permission matcher: what a host runs, extended with its own
/// behaviors (`.with(..)`) and a deployment's matcher (`.with_matcher(..)`).
pub fn core() -> Core {
    Core::new()
        .with(Workflow)
        .with(Reactions)
        .with(Constants)
        .with(StateRules)
        .with(Approvals)
        .with(Timers)
        .with(Lease)
        .with(Presence)
        .with(Budget)
        .with(Retries)
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
