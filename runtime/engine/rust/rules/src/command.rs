//! What the core is asked to run (D103): a create, an update, a delete, an
//! operation of one use on a row, an operation of one use on the table as a
//! whole (D104), a deadline that fell due, a write of a row the outbox hands
//! it, or the define and publish checks of a type's configs. Every command carries its caller,
//! the time it runs at, in epoch milliseconds, since the core reads no
//! clock, and the host's id for the request, which the keys a behavior
//! makes are derived from, since the core makes no id; a create carries
//! the id the host will write. An update, a delete and an operation may
//! carry the caller's preconditions too, which the uses' guards hold the
//! command to (D16, D104).
//!
//! The list of actions is open (a behavior's own outbox events may join it),
//! so a host matches it with a fallback arm.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::changes::RowChange;
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
    /// The caller's preconditions (D16, D104): each use's entry by its use
    /// id (an implements use's id is its behavior's name: `{ "Lease": {
    /// "token": 3 } }`), checked against the use's behavior before the row
    /// is read, and handed to that use's guard as the request's
    /// precondition ([`crate::View::precondition`]). Only an update, a
    /// delete and an operation present them; empty for none.
    #[serde(default, skip_serializing_if = "Map::is_empty")]
    pub preconditions: Map<String, Value>,
}

impl Command {
    /// A command with no request id yet.
    pub fn new(caller: Caller, now: i64, action: Action) -> Self {
        Command {
            caller,
            now,
            action,
            request: String::new(),
            preconditions: Map::new(),
        }
    }

    /// The command with the host's id for the request.
    pub fn with_request(mut self, request: impl Into<String>) -> Self {
        self.request = request.into();
        self
    }

    /// The command with the caller's preconditions, by use id.
    pub fn with_preconditions(mut self, preconditions: Map<String, Value>) -> Self {
        self.preconditions = preconditions;
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
    /// An operation of one use on the table as a whole (D104), by the use's
    /// id and the operation's declared name: one its declaration's scope
    /// `schema` gives ([`crate::Declaration::find_table_operation`]). It runs
    /// on no row: it reads rows through the reads across a table, writes them
    /// only through `Cx::call_on` and `Cx::create_row`, and presents no
    /// preconditions.
    TableOperation {
        use_id: String,
        operation: String,
        #[serde(default = "empty_object")]
        params: Value,
    },
    /// A deadline a use armed that fell due: the time it was armed to fall
    /// due at and the payload it was armed with. The host runs it only
    /// while the deadline is still armed at that time, so a deadline
    /// cleared or moved since cannot fall due on its old arming: a worker's
    /// claim of the deadline's message, which the unit that applies the
    /// change set completes, or the host's read of the arming before each
    /// run and its spend in that unit. The core does not check it (D103):
    /// the host's record of an arming is the only one. A host runs it at
    /// its clock's time, and never before `at`.
    Deadline {
        key: String,
        use_id: String,
        name: String,
        at: i64,
        #[serde(default)]
        payload: Value,
    },
    /// A write of a row that the outbox hands the core (D103): the row's
    /// key and the change. The core runs the uses that hear the row's
    /// writes on the row as it is now, and the listeners of other types the
    /// row's relations reach.
    Change {
        key: String,
        change: RowChange,
    },
}

fn empty_object() -> Value {
    Value::Object(Map::new())
}

impl Action {
    /// What a refusal names as refused: `define`, `publish`, `create`,
    /// `update`, `delete`, the operation's name (of a row or of the table),
    /// the deadline's, or `change`.
    pub fn name(&self) -> &str {
        match self {
            Action::Define => "define",
            Action::Publish { .. } => "publish",
            Action::Create { .. } => "create",
            Action::Update { .. } => "update",
            Action::Delete { .. } => "delete",
            Action::Operation { operation, .. } | Action::TableOperation { operation, .. } => {
                operation
            }
            Action::Deadline { name, .. } => name,
            Action::Change { .. } => "change",
        }
    }

    /// The action's kind, as its JSON tag writes it: `define`, `publish`,
    /// `create`, `update`, `delete`, `operation`, `tableOperation`,
    /// `deadline` or `change`.
    pub fn kind(&self) -> &'static str {
        match self {
            Action::Define => "define",
            Action::Publish { .. } => "publish",
            Action::Create { .. } => "create",
            Action::Update { .. } => "update",
            Action::Delete { .. } => "delete",
            Action::Operation { .. } => "operation",
            Action::TableOperation { .. } => "tableOperation",
            Action::Deadline { .. } => "deadline",
            Action::Change { .. } => "change",
        }
    }

    /// The row the command runs on: a create's id, or the key the others
    /// name; `None` for a create without one, for an operation of the table
    /// as a whole and for the define and publish checks.
    pub fn key(&self) -> Option<&str> {
        match self {
            Action::Define | Action::Publish { .. } | Action::TableOperation { .. } => None,
            Action::Create { id, .. } => id.as_deref(),
            Action::Update { key, .. }
            | Action::Delete { key }
            | Action::Operation { key, .. }
            | Action::Deadline { key, .. }
            | Action::Change { key, .. } => Some(key),
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
        assert!(command.preconditions.is_empty());
        let written = serde_json::to_value(command.clone().with_request("r1")).unwrap();
        assert_eq!(written["request"], json!("r1"));
        // A command with no preconditions writes none.
        assert!(written.get("preconditions").is_none());
        let fenced = command.with_preconditions(
            json!({"Lease": {"token": 3}})
                .as_object()
                .cloned()
                .unwrap_or_default(),
        );
        let written = serde_json::to_value(&fenced).unwrap();
        assert_eq!(written["preconditions"], json!({"Lease": {"token": 3}}));
        let read: Command = serde_json::from_value(written).unwrap();
        assert_eq!(read, fenced);
        assert_eq!(fenced.action.kind(), "operation");
        let due: Action = serde_json::from_value(json!({"kind": "deadline", "key": "o1", "useId": "Timers", "name": "expire", "at": 5000})).unwrap();
        assert_eq!(due.name(), "expire");

        let change: Action = serde_json::from_value(json!({"kind": "change", "key": "t1", "change": {
            "write": "update", "at": 1001000,
            "before": {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "todo", "enteredAt": 1000000}}},
            "after":  {"own": {"title": "t1", "projectId": "p1"}, "owned": {"Workflow": {"status": "doing", "enteredAt": 1001000}}}}}))
        .unwrap();
        assert_eq!(change.name(), "change");
        assert_eq!(change.key(), Some("t1"));
        let Action::Change {
            change: written, ..
        } = &change
        else {
            panic!("a change");
        };
        assert_eq!(written.write, crate::changes::WriteKind::Update);
        assert_eq!(
            serde_json::to_value(&change).unwrap()["change"]["after"]["owned"],
            json!({"Workflow": {"status": "doing", "enteredAt": 1001000}})
        );

        let create: Action =
            serde_json::from_value(json!({"kind": "create", "own": {"title": "Desk"}})).unwrap();
        assert_eq!(create.key(), None);
        assert_eq!(create.name(), "create");
        assert_eq!(
            serde_json::to_value(Action::Publish { from: None }).unwrap(),
            json!({"kind": "publish", "from": null})
        );
    }

    /// An operation of the table as a whole names no row (D104).
    #[test]
    fn a_table_operation_reads_from_json_and_names_no_row() {
        let written =
            json!({"kind": "tableOperation", "useId": "Pool", "operation": "pick", "params": {}});
        let action: Action = serde_json::from_value(written.clone()).unwrap();
        assert_eq!(
            action,
            Action::TableOperation {
                use_id: "Pool".into(),
                operation: "pick".into(),
                params: json!({}),
            }
        );
        assert_eq!(action.key(), None);
        assert_eq!(action.kind(), "tableOperation");
        assert_eq!(action.name(), "pick");
        assert_eq!(serde_json::to_value(&action).unwrap(), written);
        // Its parameters are {} when it gives none.
        let bare: Action = serde_json::from_value(
            json!({"kind": "tableOperation", "useId": "Lease", "operation": "expireHolder"}),
        )
        .unwrap();
        assert_eq!(serde_json::to_value(&bare).unwrap()["params"], json!({}));
    }
}
