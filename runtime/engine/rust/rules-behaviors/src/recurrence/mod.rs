//! Recurrence (D152): a rule that turns into dated occurrences. Its
//! expansion ([`expand()`]) is pure and is what every language calls to date
//! a rule, through the core's `expand` operation.

pub mod behavior;
pub mod expand;
pub mod rrule;

pub use behavior::{Recurrence, RecurrenceConfig};

pub use expand::{expand, expand_json, Adjust, ExpandInput, Expansion, Occurrence};
pub use rrule::{Freq, Rule};
