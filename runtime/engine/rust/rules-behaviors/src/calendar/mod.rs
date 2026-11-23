//! Calendars on the core: the cron lines and time zones Schedules reads its
//! runs with ([`cron`], re-exported here: [`Cron`], [`Zone`],
//! [`HORIZON_DAYS`]), and the behavior Calendar (D70, D103's step 4), time
//! a supply opens ([`behavior`]: [`Calendar`], [`CalendarConfig`]), whose
//! weekly hours in a time zone become instants through [`time`]. Each reads
//! zones in the IANA database jiff bundles alone, never a host's zone
//! files, so every host, the wasm32 module among them, computes the same
//! runs and the same slots.

pub mod behavior;
mod cron;
pub mod time;

pub use behavior::{Calendar, CalendarConfig};
pub use cron::{Cron, Zone, HORIZON_DAYS};
