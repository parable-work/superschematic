//! Capacity (D104), the first contract between behaviors on the core: whether
//! a row has room now, which Budget implements over its meters and Pool asks
//! of each member, as D70's Supply joins Holds to Stock. A behavior's
//! declaration names the contracts it implements and asks (`contracts`,
//! each `{ name, role }`); the core resolves a type's implementers by those
//! declarations, never by a behavior's name, and asks each through its pure
//! answer hook over the row a read across the table gave it
//! ([`superschematic_rules::View::ask`]).
//!
//! Its one query is [`HAS_ROOM`], with no parameters, answered as a
//! [`Room`]: `{ "room": true }`, or `{ "room": false, "until": <epoch ms> }`
//! with the earliest time the clock alone gives the row room again, or
//! `{ "room": false }` when only a write would. A type no use of which
//! implements it always has room.

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use superschematic_rules::{ContractQuery, Hook, KeyedRow, View};

/// The contract's name, as declarations give it.
pub const NAME: &str = "Capacity";

/// Its one query: whether a row has room now, and when it will.
pub const HAS_ROOM: &str = "hasRoom";

/// A row's room: whether it has room now, and when not, the earliest time
/// the clock alone gives it room again; none when only a write would.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Room {
    pub room: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub until: Option<i64>,
}

impl Room {
    /// Room, with no time: what a type no use of which implements Capacity
    /// answers.
    pub const OPEN: Room = Room {
        room: true,
        until: None,
    };

    /// The answer as JSON, as an implementer returns it.
    pub fn to_json(self) -> Value {
        serde_json::to_value(self).unwrap_or_else(|_| json!({ "room": self.room }))
    }
}

/// Asks every use of the row's type that implements Capacity whether the
/// row has room (`View::ask` with `hasRoom` and `{}`), and joins the
/// answers: room when every one has it, none implementing it among them;
/// with none, `until` the latest of the implementers' `until` among those
/// without room, absent when any of them gives none. An answer that is no
/// `Room` is the implementer's defect.
pub fn has_room<C>(view: &View<'_, C>, type_name: &str, row: &KeyedRow) -> Hook<Room> {
    let params = json!({});
    let answers = view.ask(type_name, row, &ContractQuery::new(NAME, HAS_ROOM, &params))?;
    let mut joined = Room::OPEN;
    let mut timeless = false;
    for (use_id, answer) in answers {
        let Ok(room) = serde_json::from_value::<Room>(answer.clone()) else {
            return Err(view.defect(format!(
                "{use_id} answered {NAME}'s {HAS_ROOM} with {answer}, which is no room"
            )));
        };
        if room.room {
            continue;
        }
        joined.room = false;
        match room.until {
            Some(until) => {
                joined.until = Some(joined.until.map_or(until, |kept| kept.max(until)));
            }
            None => timeless = true,
        }
    }
    if timeless {
        joined.until = None;
    }
    Ok(joined)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_room_reads_and_writes_as_its_json() {
        assert_eq!(Room::OPEN.to_json(), json!({"room": true}));
        let out = Room {
            room: false,
            until: Some(1_772_326_800_000),
        };
        assert_eq!(
            out.to_json(),
            json!({"room": false, "until": 1772326800000_i64})
        );
        assert_eq!(
            serde_json::from_value::<Room>(json!({"room": false})).unwrap(),
            Room {
                room: false,
                until: None
            }
        );
        assert!(serde_json::from_value::<Room>(json!({"until": 3})).is_err());
    }
}
