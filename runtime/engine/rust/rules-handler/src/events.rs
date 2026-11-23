//! The event queue as a host serves it (D58's event log and stream, D104's
//! events, D117): what the behaviors emitted about rows, in the order
//! written, read from a cursor and kept by type, row, use and kind, as a
//! JSON page or as server-sent events. Nothing here does I/O: the host
//! reads the queue ([`crate::Store::events`]) and hands its messages in.
//!
//! A read takes `after` (a cursor, or `head` for only what comes next; a
//! reconnecting client's `Last-Event-ID` wins over it), `schema` (a type),
//! `instanceId`, `behavior` (a use) and `kind` (one or a list, repeated or
//! separated by commas), and `limit` for a page. A read of one type asks
//! the policy whether the caller may read it, and is refused when not; a
//! read of every type keeps the events of the types the caller may read.
//!
//! A stream sends each event as one message, its `id:` the event's cursor
//! and its `data:` the event as JSON; then, once it has caught up, a
//! `ready` event whose id and data carry the cursor; then, while it waits,
//! a comment each heartbeat, so a proxy keeps the connection open and a
//! dead client is found. A stream opened with `signals=1` (D77's primitive
//! 2) also sends each signal its filters keep, on a type its caller may
//! read, once it has caught up, as `event: signal` with no `id:`
//! ([`crate::signals`]); the host holds them for it from the moment it
//! opens, so a reader that opens the stream before it reads misses none.

use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Refusal};

use crate::access::{Action, NAMESPACE};
use crate::handler::Handler;
use crate::http::HttpRequest;
use crate::store::QueuedEvent;

/// The most events one page holds, and its size when a read names none.
pub const MAX_PAGE: usize = 500;
pub const DEFAULT_PAGE: usize = 100;

/// Where a read starts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum After {
    /// After the event of a cursor: 0 for the first.
    Cursor(i64),
    /// After the last event written when the read is made.
    Head,
}

/// One read of the event queue.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EventsQuery {
    pub after: After,
    pub schema: Option<String>,
    pub instance_id: Option<String>,
    pub behaviors: Vec<String>,
    pub kinds: Vec<String>,
    pub limit: usize,
    /// Whether the client asked for server-sent events (`Accept:
    /// text/event-stream`).
    pub stream: bool,
    /// Whether a stream sends signals beside the events (`signals=1`,
    /// D77's primitive 2).
    pub signals: bool,
}

fn cursor_of(text: &str) -> Option<i64> {
    (!text.is_empty() && text.len() <= 15 && text.bytes().all(|b| b.is_ascii_digit()))
        .then(|| text.parse().ok())
        .flatten()
}

/// Whether `Accept` asks for `text/event-stream` with a quality above zero.
pub fn wants_stream(accept: Option<&str>) -> bool {
    let Some(accept) = accept.filter(|accept| !accept.is_empty()) else {
        return false;
    };
    accept.split(',').any(|range| {
        let mut parts = range.split(';').map(|part| part.trim().to_lowercase());
        if parts.next().as_deref() != Some("text/event-stream") {
            return false;
        }
        match parts.find(|part| part.starts_with("q=")) {
            None => true,
            Some(quality) => quality[2..].trim().parse::<f64>().is_ok_and(|q| q > 0.0),
        }
    })
}

impl EventsQuery {
    /// The read a request's query and headers ask for; the detail of a
    /// 400 when they ask for none.
    pub fn read(request: &HttpRequest, query: &[(String, String)]) -> Result<Self, String> {
        let mut read = EventsQuery {
            after: After::Cursor(0),
            schema: None,
            instance_id: None,
            behaviors: Vec::new(),
            kinds: Vec::new(),
            limit: DEFAULT_PAGE,
            stream: wants_stream(request.header("accept")),
            signals: false,
        };
        let list = |value: &str| -> Vec<String> {
            value
                .split(',')
                .map(str::trim)
                .filter(|item| !item.is_empty())
                .map(str::to_owned)
                .collect()
        };
        for (name, value) in query {
            match name.as_str() {
                "after" => {
                    read.after =
                        match value.as_str() {
                            "head" => After::Head,
                            text => After::Cursor(cursor_of(text).ok_or(
                                "after is an event cursor, a non-negative integer, or head",
                            )?),
                        }
                }
                "signals" => {
                    read.signals = match value.as_str() {
                        "1" | "true" => true,
                        "0" | "false" => false,
                        _ => return Err("signals is 1 or 0".to_owned()),
                    }
                }
                "schema" => read.schema = Some(value.clone()),
                "instanceId" => read.instance_id = Some(value.clone()),
                "behavior" => read.behaviors.extend(list(value)),
                "kind" => read.kinds.extend(list(value)),
                "limit" => {
                    read.limit = value
                        .parse::<usize>()
                        .ok()
                        .filter(|limit| (1..=MAX_PAGE).contains(limit))
                        .ok_or_else(|| format!("limit is a whole number from 1 to {MAX_PAGE}"))?;
                }
                other => return Err(format!("Unknown query parameter {other}")),
            }
        }
        if let Some(header) = request
            .header("last-event-id")
            .map(str::trim)
            .filter(|header| !header.is_empty())
        {
            read.after = After::Cursor(
                cursor_of(header)
                    .ok_or("Last-Event-ID is an event cursor, a non-negative integer")?,
            );
        }
        Ok(read)
    }

    /// Whether the read keeps an event, by its filters.
    pub fn keeps(&self, event: &QueuedEvent) -> bool {
        self.schema
            .as_ref()
            .is_none_or(|schema| *schema == event.type_name)
            && self.instance_id.as_ref().is_none_or(|id| *id == event.id)
            && (self.behaviors.is_empty() || self.behaviors.contains(&event.use_id))
            && (self.kinds.is_empty() || self.kinds.contains(&event.kind))
    }

    /// The cursor the read starts after, `head` read as the last event the
    /// queue holds.
    pub fn start(&self, head: i64) -> i64 {
        match self.after {
            After::Cursor(cursor) => cursor,
            After::Head => head,
        }
    }
}

/// An event as a read answers it: its cursor, kind, namespace, type
/// (`schema`), row (`instanceId`), use, time and payload.
pub fn event_json(event: &QueuedEvent) -> Value {
    let at = crate::values::iso_of(event.at).map_or(Value::from(event.at), Value::String);
    json!({
        "cursor": event.seq,
        "kind": event.kind,
        "namespace": NAMESPACE,
        "schema": event.type_name,
        "instanceId": event.id,
        "use": event.use_id,
        "at": at,
        "payload": event.payload,
    })
}

/// One server-sent event of an event.
pub fn frame(event: &QueuedEvent) -> String {
    let data = serde_json::to_string(&event_json(event)).unwrap_or_default();
    format!("id: {}\ndata: {data}\n\n", event.seq)
}

/// The event a stream sends once it has caught up.
pub fn ready_frame(cursor: i64) -> String {
    format!("id: {cursor}\nevent: ready\ndata: {{\"cursor\":{cursor}}}\n\n")
}

/// The comment a waiting stream sends each heartbeat.
pub const HEARTBEAT: &str = ": heartbeat\n\n";

impl Handler<'_> {
    /// Whether the policy allows a caller an action on a type.
    pub fn allows(&self, caller: &Caller, action: Action, schema: &str) -> bool {
        self.ask(caller, action, schema, None).is_ok()
    }

    /// Admits a read of the event queue: a caller with a subject, and, for
    /// a read of one type, the policy's leave to read it.
    pub fn admit_events(&self, caller: &Caller, query: &EventsQuery) -> Result<(), Refusal> {
        if caller.subject.is_empty() {
            return Err(Refusal::invalid_argument(
                "a request needs a caller with a subject",
            ));
        }
        match &query.schema {
            Some(schema) => self.ask(caller, Action::Read, schema, None),
            None => Ok(()),
        }
    }

    /// The events a read keeps of those after its cursor, as the caller
    /// may read them, at most `limit`, and the cursor to read on from: past
    /// every event scanned, the ones its filters dropped too.
    pub fn events_after<'e>(
        &self,
        caller: &Caller,
        query: &EventsQuery,
        events: &'e [QueuedEvent],
        limit: usize,
    ) -> (Vec<&'e QueuedEvent>, i64, bool) {
        let mut kept = Vec::new();
        let mut next = None;
        let mut more = false;
        for event in events {
            if kept.len() >= limit {
                more = true;
                break;
            }
            next = Some(event.seq);
            if query.keeps(event) && self.allows(caller, Action::Read, &event.type_name) {
                kept.push(event);
            }
        }
        (kept, next.unwrap_or_default(), more)
    }

    /// A page of the event queue: `{events, next, more}`, from the events
    /// the queue holds after the read's cursor, in the order written.
    pub fn events_page(
        &self,
        caller: &Caller,
        query: &EventsQuery,
        events: &[QueuedEvent],
        head: i64,
    ) -> Result<Value, Refusal> {
        self.admit_events(caller, query)?;
        let start = query.start(head);
        let after: Vec<QueuedEvent> = events
            .iter()
            .filter(|event| event.seq > start)
            .cloned()
            .collect();
        let (kept, next, more) = self.events_after(caller, query, &after, query.limit);
        let mut page = Map::new();
        page.insert(
            "events".into(),
            Value::Array(kept.into_iter().map(event_json).collect()),
        );
        page.insert("next".into(), Value::from(next.max(start)));
        page.insert("more".into(), Value::Bool(more));
        Ok(Value::Object(page))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn event(seq: i64, type_name: &str, kind: &str) -> QueuedEvent {
        QueuedEvent {
            seq,
            type_name: type_name.into(),
            id: format!("r{seq}"),
            use_id: "Schedules".into(),
            kind: kind.into(),
            at: 1_767_225_600_000,
            payload: json!({"n": seq}),
        }
    }

    #[test]
    fn a_read_keeps_by_its_filters_and_frames_each_event() {
        let request = HttpRequest {
            method: "GET".into(),
            path: "/namespaces/default/events".into(),
            headers: vec![
                ("Accept".into(), "text/event-stream".into()),
                ("Last-Event-ID".into(), "2".into()),
            ],
            ..HttpRequest::default()
        };
        let query = EventsQuery::read(
            &request,
            &[
                ("after".into(), "9".into()),
                ("kind".into(), "run,skip".into()),
                ("schema".into(), "Job".into()),
            ],
        )
        .expect("it reads");
        assert!(query.stream);
        assert_eq!(query.after, After::Cursor(2), "Last-Event-ID wins");
        assert!(query.keeps(&event(3, "Job", "skip")));
        assert!(!query.keeps(&event(3, "Task", "run")));
        assert!(!query.keeps(&event(3, "Job", "other")));
        assert_eq!(
            frame(&event(3, "Job", "run")),
            "id: 3\ndata: {\"cursor\":3,\"kind\":\"run\",\"namespace\":\"default\",\"schema\":\"Job\",\"instanceId\":\"r3\",\"use\":\"Schedules\",\"at\":\"2026-01-01T00:00:00.000Z\",\"payload\":{\"n\":3}}\n\n"
        );
        assert_eq!(
            ready_frame(7),
            "id: 7\nevent: ready\ndata: {\"cursor\":7}\n\n"
        );
        assert!(EventsQuery::read(&request, &[("after".into(), "-1".into())]).is_err());
    }
}
