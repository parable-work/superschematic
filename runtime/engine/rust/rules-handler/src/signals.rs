//! Signals (D77's primitive 2) as development mode serves them: a message
//! about one row that the host delivers to the event streams open on it
//! once the request's transaction commits, and keeps nowhere. A behavior's
//! operation sends one through the core (`Cx::signal`: Comments' `draft`);
//! a caller sends one whose access is `read` or `write`
//! ([`crate::Request::Signal`], `POST
//! /namespaces/default/schemas/{type}/instances/{id}/signals/{behavior}/{name}`),
//! which the policy is asked about with that action and the operation
//! `signal:<name>`, and which the core holds to its declaration and its
//! data.
//!
//! A request's [`crate::Unit`] carries the signals its change set sent
//! ([`HostSignal`]), stamped with the caller's subject and the request's
//! time: the host publishes them after the commit and drops them with a
//! rollback ([`crate::serve_publishing`]). A stream opened with `signals=1`
//! sends each one its filters keep, on a type its caller may read, as the
//! engine's stream message: `event: signal` and its JSON, with no `id:`, so
//! a reconnect's `Last-Event-ID` and its replay never see one ([`frame`]).
//! Delivery is at most once and in no order with the events.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use superschematic_rules::{Action as CoreAction, Refusal, SignalAccess};

use crate::access::{Action, NAMESPACE};
use crate::document::Model;
use crate::events::EventsQuery;
use crate::handler::{Context, Handler, Live};

/// The prefix of the operation the policy is asked a caller's signal by:
/// `signal:<name>`, as the engine asks it.
pub const SIGNAL_OPERATION_PREFIX: &str = "signal:";

/// The name of the event a stream opened with signals sends for each one.
pub const SIGNAL_EVENT: &str = "signal";

/// How many signals a stream holds for a slow client; past it the oldest
/// is dropped, as the engine's stream drops them.
pub const SIGNAL_BUFFER: usize = 256;

/// One signal as the host publishes it, the engine's stream message: the
/// namespace, the type (`schema`), the row (`instanceId`), the behavior
/// that declares it and the use when its id is not the behavior's name (a
/// field anchor's), its name and data, the sender's subject (`from`) and
/// the request's time (`at`, epoch milliseconds).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct HostSignal {
    pub namespace: String,
    pub schema: String,
    pub instance_id: String,
    pub behavior: String,
    #[serde(default, rename = "use", skip_serializing_if = "Option::is_none")]
    pub use_id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub data: Value,
    pub from: String,
    pub at: i64,
}

impl HostSignal {
    /// The use the signal is of: its id, or the behavior's name.
    pub fn use_of(&self) -> &str {
        self.use_id.as_deref().unwrap_or(&self.behavior)
    }
}

/// The signals a change set sent, as the host publishes them: each with its
/// row's type and key, the behavior its use composes, the caller's subject
/// and the request's time.
pub(crate) fn published(
    live: &Live,
    signals: &[superschematic_rules::Signal],
    context: &Context,
) -> Vec<HostSignal> {
    signals
        .iter()
        .map(|signal| {
            let type_name = signal.row.type_name.clone();
            let behavior = live
                .models
                .get(&type_name)
                .and_then(|model| model.use_of(&signal.use_id))
                .map_or_else(|| signal.use_id.clone(), |used| used.spec.behavior.clone());
            HostSignal {
                namespace: NAMESPACE.to_owned(),
                schema: type_name,
                instance_id: signal.row.key.clone().unwrap_or_default(),
                use_id: (behavior != signal.use_id).then(|| signal.use_id.clone()),
                behavior,
                name: signal.name.clone(),
                data: signal.data.clone(),
                from: context.caller.subject.clone(),
                at: context.now,
            }
        })
        .collect()
}

/// One server-sent event of a signal: `event: signal` and its JSON, with no
/// `id:`.
pub fn frame(signal: &HostSignal) -> String {
    let data = serde_json::to_string(signal).unwrap_or_default();
    format!("event: {SIGNAL_EVENT}\ndata: {data}\n\n")
}

impl EventsQuery {
    /// Whether a stream opened with signals keeps one, by its filters: its
    /// type, its row, and its behavior or use.
    pub fn keeps_signal(&self, signal: &HostSignal) -> bool {
        self.signals
            && self
                .schema
                .as_ref()
                .is_none_or(|schema| *schema == signal.schema)
            && self
                .instance_id
                .as_ref()
                .is_none_or(|id| *id == signal.instance_id)
            && (self.behaviors.is_empty()
                || self
                    .behaviors
                    .iter()
                    .any(|named| *named == signal.behavior || named == signal.use_of()))
    }
}

impl Handler<'_> {
    /// Whether a stream's caller is sent a signal: its filters keep it and
    /// the caller may read its type.
    pub fn delivers(
        &self,
        caller: &superschematic_rules::Caller,
        query: &EventsQuery,
        signal: &HostSignal,
    ) -> bool {
        query.keeps_signal(signal) && self.allows(caller, Action::Read, &signal.schema)
    }

    /// A caller's signal's action, its use resolved by the behavior a route
    /// names: the one use of the type whose behavior it is, or the use of
    /// that id. The policy is asked `read` or `write` as the signal's
    /// declaration says, with the operation `signal:<name>`; a signal only
    /// the behavior's operations send, and one it does not declare, the
    /// core refuses.
    pub(crate) fn signal_action(
        &self,
        model: &Model,
        behavior: &str,
        key: &str,
        name: &str,
        data: &Value,
        context: &Context,
    ) -> Result<CoreAction, Refusal> {
        let uses: Vec<&str> = model
            .uses
            .iter()
            .filter(|used| used.spec.behavior == behavior || used.id() == behavior)
            .map(crate::document::Use::id)
            .collect();
        let use_id = match uses.as_slice() {
            [one] => (*one).to_owned(),
            [] => {
                return Err(Refusal::not_found(format!(
                    "{} does not compose behavior {behavior}",
                    model.name
                )))
            }
            many => {
                return Err(Refusal::invalid_argument(format!(
                    "{} has {behavior} on uses {}; name the use in its place",
                    model.name,
                    many.join(", ")
                )))
            }
        };
        let declared_behavior = model
            .use_of(&use_id)
            .map(|used| used.spec.behavior.clone())
            .unwrap_or_default();
        let access = self
            .core()
            .declaration(&declared_behavior)
            .and_then(|declaration| declaration.find_signal(name))
            .map(|signal| signal.access);
        let asked = match access {
            Some(SignalAccess::Write) => Action::Write,
            _ => Action::Read,
        };
        self.ask(
            &context.caller,
            asked,
            &model.name,
            Some(&format!("{SIGNAL_OPERATION_PREFIX}{name}")),
        )?;
        Ok(CoreAction::Signal {
            key: key.to_owned(),
            use_id,
            name: name.to_owned(),
            data: if data.is_null() {
                Value::Object(Map::new())
            } else {
                data.clone()
            },
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_signal_frames_as_the_engines_stream_message_with_no_id() {
        let signal = HostSignal {
            namespace: NAMESPACE.into(),
            schema: "Note".into(),
            instance_id: "n1".into(),
            behavior: "Comments".into(),
            use_id: None,
            name: "draft".into(),
            data: json!({"message": 2, "offset": 0, "text": "Hi"}),
            from: "bob".into(),
            at: 1000,
        };
        assert_eq!(
            frame(&signal),
            "event: signal\ndata: {\"namespace\":\"default\",\"schema\":\"Note\",\"instanceId\":\"n1\",\"behavior\":\"Comments\",\"name\":\"draft\",\"data\":{\"message\":2,\"offset\":0,\"text\":\"Hi\"},\"from\":\"bob\",\"at\":1000}\n\n"
        );
        let query = EventsQuery {
            after: crate::events::After::Head,
            schema: Some("Note".into()),
            instance_id: None,
            behaviors: vec!["Comments".into()],
            kinds: Vec::new(),
            limit: 100,
            stream: true,
            signals: true,
        };
        assert!(query.keeps_signal(&signal));
        let other = HostSignal {
            schema: "Task".into(),
            ..signal.clone()
        };
        assert!(!query.keeps_signal(&other));
        let closed = EventsQuery {
            signals: false,
            ..query
        };
        assert!(!closed.keeps_signal(&signal));
    }
}
