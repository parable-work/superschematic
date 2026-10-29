//! What the core is asked to run (D103): a create, an update, a delete, an
//! operation of one use, a deadline that fell due, or the define and
//! publish checks of a type's configs. Every command carries its caller,
//! the time it runs at, in epoch milliseconds, since the core reads no
//! clock, and the host's id for the request, which the keys a behavior
//! makes are derived from, since the core makes no id; a create carries
//! the id the host will write.
//!
//! The list of actions is open: an event from the outbox joins it with the
//! behaviors that react to one (D103's order of the work, step 3), so a
//! host matches it with a fallback arm.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::spec::TypeSpec;

/// Who makes the call: a subject and the permissions it holds. A service
/// standing in for an end user (D37) holds none, so a behavior that asks
/// whether it holds a permission is told no.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Caller {
    pub subject: String,
    #[serde(default)]
    pub permissions: Vec<String>,
    /// True when no end user came with the call: the principal is a
    /// service's own.
    #[serde(default, skip_serializing_if = "is_false")]
    pub stands_in: bool,
}

fn is_false(flag: &bool) -> bool {
    !*flag
}

impl Caller {
    /// A caller with a subject and its permissions.
    pub fn new<P: Into<String>>(
        subject: impl Into<String>,
        permissions: impl IntoIterator<Item = P>,
    ) -> Self {
        Caller {
            subject: subject.into(),
            permissions: permissions.into_iter().map(Into::into).collect(),
            stands_in: false,
        }
    }

    /// A service standing in for an end user: a subject and no
    /// permissions.
    pub fn standing_in(subject: impl Into<String>) -> Self {
        Caller {
            subject: subject.into(),
            permissions: Vec::new(),
            stands_in: true,
        }
    }
}

/// One command: who calls, when, what, and the host's id for the request.
/// It may gain members, so a host builds it with [`Command::new`].
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
#[non_exhaustive]
pub struct Command {
    pub caller: Caller,
    /// The time the command runs at, in epoch milliseconds: what a
    /// behavior's context answers for now.
    pub now: i64,
    pub action: Action,
    /// The host's id for the request: unique per command, and the same
    /// each time the host runs the command again, after a read, a lock or
    /// a row that moved. The keys a behavior makes (`Cx::new_key`) are
    /// derived from it, so a run again makes the same keys. Empty when the
    /// host gives none, and then a behavior that makes a key is refused.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub request: String,
}

impl Command {
    /// A command with no request id yet.
    pub fn new(caller: Caller, now: i64, action: Action) -> Self {
        Command {
            caller,
            now,
            action,
            request: String::new(),
        }
    }

    /// The command with the host's id for the request.
    pub fn with_request(mut self, request: impl Into<String>) -> Self {
        self.request = request.into();
        self
    }
}

/// What a command does.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
#[non_exhaustive]
pub enum Action {
    /// The define check: every use names a behavior the core runs, the
    /// uses' ids are distinct, and each behavior parses its config.
    Define,
    /// The publish check: the define check, then each behavior's rule for
    /// the change of its config from the published version (`from`) to the
    /// one the command runs on. `from` is `None` for a first version.
    Publish {
        #[serde(default)]
        from: Option<TypeSpec>,
    },
    /// A new row: its own fields (the author's, not a behavior's), each
    /// use's create parameters by use id, and the key the host will write:
    /// the caller's or one the host made. `None` leaves the key to the
    /// host's layout, and the change set's create has none.
    Create {
        #[serde(default)]
        id: Option<String>,
        #[serde(default)]
        own: Map<String, Value>,
        #[serde(default)]
        params: Map<String, Value>,
    },
    /// A merge patch (RFC 7396) of a row's own fields.
    Update {
        key: String,
        patch: Map<String, Value>,
    },
    Delete {
        key: String,
    },
    /// An operation of one use, by the use's id and the operation's
    /// declared name.
    Operation {
        key: String,
        use_id: String,
        operation: String,
        #[serde(default = "empty_object")]
        params: Value,
    },
    /// A deadline a use armed that fell due: the time it was armed to fall
    /// due at and the payload it was armed with. The host runs it only
    /// while the deadline is still armed at that time, so a deadline
    /// cleared or moved since cannot fall due on its old arming.
    Deadline {
        key: String,
        use_id: String,
        name: String,
        at: i64,
        #[serde(default)]
        payload: Value,
    },
}

fn empty_object() -> Value {
    Value::Object(Map::new())
}

impl Action {
    /// What a refusal names as refused: `define`, `publish`, `create`,
    /// `update`, `delete`, the operation's name or the deadline's.
    pub fn name(&self) -> &str {
        match self {
            Action::Define => "define",
            Action::Publish { .. } => "publish",
            Action::Create { .. } => "create",
            Action::Update { .. } => "update",
            Action::Delete { .. } => "delete",
            Action::Operation { operation, .. } => operation,
            Action::Deadline { name, .. } => name,
        }
    }

    /// The row the command runs on: a create's id, or the key the others
    /// name; `None` for a create without one and for the define and
    /// publish checks.
    pub fn key(&self) -> Option<&str> {
        match self {
            Action::Define | Action::Publish { .. } => None,
            Action::Create { id, .. } => id.as_deref(),
            Action::Update { key, .. }
            | Action::Delete { key }
            | Action::Operation { key, .. }
            | Action::Deadline { key, .. } => Some(key),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_command_reads_from_json() {
        let command: Command = serde_json::from_value(json!({
            "caller": {"subject": "alice", "permissions": ["orders.ship"]},
            "now": 1000,
            "action": {"kind": "operation", "key": "o1", "useId": "Workflow", "operation": "transition", "params": {"to": "open"}}
        }))
        .unwrap();
        assert_eq!(command.caller, Caller::new("alice", ["orders.ship"]));
        assert_eq!(command.action.name(), "transition");
        assert_eq!(command.action.key(), Some("o1"));
        assert_eq!(command.request, "");
        assert_eq!(
            serde_json::to_value(command.with_request("r1")).unwrap()["request"],
            json!("r1")
        );
        let due: Action = serde_json::from_value(json!({"kind": "deadline", "key": "o1", "useId": "Timers", "name": "expire", "at": 5000})).unwrap();
        assert_eq!(due.name(), "expire");

        let create: Action =
            serde_json::from_value(json!({"kind": "create", "own": {"title": "Desk"}})).unwrap();
        assert_eq!(create.key(), None);
        assert_eq!(create.name(), "create");
        assert_eq!(
            serde_json::to_value(Action::Publish { from: None }).unwrap(),
            json!({"kind": "publish", "from": null})
        );
    }
}
