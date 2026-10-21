//! The commerce behaviors (D70) for the Rust engine (D58): `Stock`,
//! units a supply counts; `Calendar`, time a supply opens; `Holds`, one
//! claim's holds on any supply, tied to its Workflow; and `Payments`, what
//! a claim owes and the payments and refunds that settle it. The core
//! declares them under bare names, with the package
//! `@superschematic/engine-commerce`; this crate implements them on the
//! plug-in API alone, as the work queue's crate implements its behaviors.
//!
//! A claim reaches a supply only through the supply contract's operations,
//! which [`supply`] gives the types, the holder check and the refusals of,
//! so an extension's supply joins it the same way `Stock` and `Calendar`
//! do.
//!
//! Each behavior embeds its declaration from `declarations/`, the copy
//! `make behaviors` writes from the core registry
//! (`internal/registry/behaviors`, `--package
//! @superschematic/engine-commerce`) and CI checks (`make
//! behaviors-check`), so the engine and the compiler read one declaration.
//!
//! An engine runs them once a host registers them, after the core's and,
//! when it has them, the work queue's:
//!
//! ```no_run
//! use superschematic_engine::{BehaviorSet, Engine, EngineOptions, Policy};
//!
//! let mut options = EngineOptions::sqlite("shop.db");
//! options.policy = Some(Policy::allow_all());
//! options.behaviors = BehaviorSet::new().extend(superschematic_engine_commerce::commerce());
//! let engine = Engine::open(options)?;
//! # Ok::<(), superschematic_engine::Error>(())
//! ```
//!
//! [`commerce`] registers every behavior of the package, in [`NAMES`]'
//! order; [`behavior`] gives one by name.

pub mod currency;
mod guidance;
pub mod stock;
pub mod supply;

use superschematic_engine::BehaviorSet;

pub use stock::{CurrencySource, LowAt, Stock, StockConfig, StockPrice};

/// Every commerce behavior this crate implements so far, in the order
/// [`commerce`] registers them.
pub const NAMES: [&str; 1] = ["Stock"];

/// Registers one behavior in a set.
type Register = fn(BehaviorSet) -> BehaviorSet;

/// Each behavior, by name, in [`NAMES`]' order.
const REGISTER: [(&str, Register); 1] = [("Stock", |set| set.with(Stock))];

/// One commerce behavior by name, for a host that registers only some;
/// `None` for a name that is none of [`NAMES`].
pub fn behavior(name: &str) -> Option<BehaviorSet> {
    REGISTER
        .iter()
        .find(|(known, _)| *known == name)
        .map(|(_, register)| register(BehaviorSet::new()))
}

/// Every commerce behavior, in [`NAMES`]' order: what a host passes in
/// `EngineOptions::behaviors` after the core's
/// (`superschematic_engine_behaviors::core`).
pub fn commerce() -> BehaviorSet {
    REGISTER
        .iter()
        .fold(BehaviorSet::new(), |set, (_, register)| register(set))
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
        assert_eq!(REGISTER.map(|(name, _)| name), NAMES);
        assert_eq!(commerce().len(), NAMES.len());
    }
}
