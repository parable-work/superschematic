//! The behaviors ported onto the pure core (D103): each one a
//! [`superschematic_rules::Behavior`] that reads and writes only logical
//! names, its owned fields and its sub-tables, and does no I/O. A host's
//! core is a [`superschematic_rules::Core`] built with the ones the project
//! composes.
//!
//! Each behavior reads its declaration from the file the compiler registers
//! and the engine embeds (`../behaviors/declarations`, and the work queue's
//! `../../engine-workqueue/rust/declarations`), in place, so the engine and
//! the core read one declaration while both exist; a behavior no engine
//! implements (Seats, Reconcile, Schedules, Pool) reads the copy `make behaviors` writes into
//! this crate (`declarations`). The core
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
//! [`Seats`] and [`Reconcile`], in step 3 of D104's order of the work, the
//! first behaviors only the core implements (their declarations are
//! `rules-behaviors/declarations`', which `make behaviors` writes for the
//! package `@superschematic/rules`): named seats on a row, which a Workflow
//! transition's and a StateRules state's `by` name as `seat:<name>`; and the
//! state a row's provider last reported, which moves its Workflow there along
//! the transitions it allows.
//! [`Schedules`], the rest of step 3 for now: named schedules on a row,
//! each a run after a delay, at an interval or on a cron line in a time
//! zone, its next run a deadline whose fire emits the event `run`, which the
//! table's event queue carries to the project's handler, and whose outcome
//! a report records and tries again; [`calendar`] reads its cron lines and
//! time zones, on jiff and the IANA database it bundles alone.
//! [`Pool`], the last of step 3: a table's rows the members of a pool, each
//! open, near its limit or out until a time, among which `pick`, an
//! operation of the table as a whole run as a command without a row,
//! chooses one; an out member opens only through a check with room made
//! after its time, rechecked through the event `recheck` the project's
//! handler answers; and a member whose Budget has no room counts as out,
//! read through [`capacity`], the first contract between behaviors, which a
//! declaration names and the core resolves by declarations. Lease's
//! `expireHolder` runs as a command without a row too.
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
pub mod calendar;
pub mod capacity;
pub mod constants;
mod declared;
mod flow;
pub mod lease;
mod moves;
mod paging;
mod periods;
pub mod pool;
pub mod presence;
pub mod reactions;
pub mod reconcile;
pub mod retries;
pub mod schedules;
mod schema;
pub mod seats;
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
pub use pool::{Pool, PoolConfig};
pub use presence::{Presence, PresenceConfig};
pub use reactions::{
    Reactions, ReactionsChanges, ReactionsConfig, ReactionsField, ReactionsRule, ReactionsTerminal,
    ReactionsThen, ReactionsWhen,
};
pub use reconcile::{Reconcile, ReconcileConfig};
pub use retries::{
    Retries, RetriesConfig, RetriesKeepBest, RetriesWindow, RetryBackoff, RetryClass, LIMITS_TYPES,
};
pub use schedules::{Schedules, SchedulesConfig, SchedulesKind, SchedulesRetry};
pub use seats::{SeatRef, Seats, SeatsConfig, SeatsOnFull, SeatsSeat};
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
/// (`behaviors::NAMES`) of the ones ported so far, then the ones only the
/// core implements (D104).
pub const NAMES: [&str; 14] = [
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
    seats::NAME,
    reconcile::NAME,
    schedules::NAME,
    pool::NAME,
];

/// The declaration file of each behavior [`core`] registers, in its order:
/// the text the compiler registers and each behavior reads in place, which
/// a binding hands its host whole (D103), each field's column type, the
/// value field, the sub-tables, whether it arms deadlines and whether it
/// hears its row's writes with the rest.
pub const DECLARATIONS: [&str; 14] = [
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
    seats::DECLARATION,
    reconcile::DECLARATION,
    schedules::DECLARATION,
    pool::DECLARATION,
];

/// The contracts between behaviors the core defines (D104), each a name a
/// declaration's `contracts` may give: what one behavior asks of the uses
/// that implement it, resolved by their declarations.
pub const CONTRACTS: [&str; 1] = [capacity::NAME];

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
        .with(Seats)
        .with(Reconcile)
        .with(Schedules)
        .with(Pool)
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

    /// Every contract a declaration names is one the core defines, and each
    /// behavior that implements one answers each of its queries.
    #[test]
    fn every_declarations_contracts_are_the_cores() {
        let core = core();
        let mut implementers = Vec::new();
        for name in NAMES {
            let declaration = core.declaration(name).expect("registered");
            for contract in declaration.contracts.iter().chain(&declaration.asks) {
                assert!(
                    CONTRACTS.contains(&contract.as_str()),
                    "{name} names contract {contract}"
                );
            }
            if declaration.implements(capacity::NAME) {
                implementers.push(name);
            }
        }
        assert_eq!(implementers, [budget::NAME]);
        let config = BudgetConfig::default();
        let params = serde_json::json!({});
        let query =
            superschematic_rules::ContractQuery::new(capacity::NAME, capacity::HAS_ROOM, &params);
        use superschematic_rules::Behavior;
        assert_eq!(
            Budget.answer(
                &config,
                &query,
                &superschematic_rules::Row::default(),
                &Default::default(),
                0
            ),
            Some(serde_json::json!({"room": true}))
        );
    }
}
