//! The notifier (D153, D191): events raised on a table's rows, routed by
//! rows, held to each recipient's audience when sent, and delivered per
//! recipient through the channel adapters an environment configures
//! (D159), with replies that run as their sender. The TypeScript runtime's
//! `notify.ts`, with its tables, statements, states and refusals.
//!
//! Events. A table declares the events its rows raise with `@event` (its
//! kind, the fields its payload carries, the field a notice is titled by,
//! its default urgency), which the Postgres definition carries
//! ([`Table::events`](super::Table::events)). [`Notifier::raise`] writes an
//! event as a message of the notifier's dispatch queue, a D80 queue the
//! loader filled, in the caller's transaction: it commits with the change
//! that caused it or not at all. A behavior's event (D104), which the core
//! writes to its table's event queue in the command's transaction, reaches
//! the notifier from the behavior worker's Workers method
//! ([`Notifier::from_behavior_event`]), and a cleared reference (D154) from
//! its queue's worker ([`Notifier::cleared`]).
//!
//! Routing. The worker routes an event in one transaction: each enabled
//! route of its kind (or `*`) and table whose filter, in the repositories'
//! filter syntax, the record matches, names recipients by relation paths of
//! the record, principals, roles or parties. A recipient who cannot read
//! the record, through its table's repository as the recipient (D156:
//! audience and grants), is not notified, and nothing records that it was
//! skipped. A recipient who can gets one notice in the inbox, numbered
//! among its own (a reply names that number), and one delivery on the
//! first channel its preferences name, sent at once, after its quiet hours,
//! or in its next digest, by the urgency: urgent ignores quiet hours and
//! escalates through its route's steps unless acknowledged in time, each
//! step a delayed message of the same queue; normal waits out quiet hours;
//! digest joins the next digest; record sends nothing.
//!
//! Delivery. A delivery is queued, then sent, delivered and read, or ends
//! failed or acknowledged. Each is a message of the dispatch queue, due
//! when it should go (a delay on its enqueue, D80). An adapter whose health
//! check fails is skipped, and the notifier raises `channel.down`, routed
//! as any event. A failure, or no delivery report within the timeout from
//! an adapter that reports them, falls through to the recipient's next
//! channel.
//!
//! Replies. [`Notifier::reply`] is the entry point an inbound adapter's
//! route calls with a sender and the text, and [`Notifier::inbound`] reads
//! an adapter's `@webhook` request and calls it per message. A sender whose
//! channel identity is verified (or entered by hand) runs as its principal;
//! an unknown one reaches nothing and answers `unknown`, which D157's
//! triage takes. The channel kind's trust row caps the permissions a reply
//! may use, and an identity entered by hand reaches no operation past an
//! acknowledgement. A reply names its notice by its reference unless
//! exactly one notice waits on its sender on that channel. `ack`
//! acknowledges; any other verb is a reply action the app gives for the
//! notice's event kind ([`ReplyAction`]), which runs in a transaction as
//! the principal, held to the action's permission through the same
//! repository write as its route. An approval's `approve` and `deny`
//! ([`approval_replies`]) are two such actions. Every reply is a row of the
//! replies table, and every action is echoed back with its undo.
//!
//! The parts here are pure and shared with the TypeScript runtime through
//! `runtime/http/testdata/notify_parity.json`: an address normalised by its
//! kind, a reply's words read, quiet hours, a channel's trust over a
//! principal's permissions, and a code's hash. The notifier itself runs
//! over [`Store`](super::store::Store): a Postgres pool's repositories, or
//! a SQLite file's (#618), where a transaction is a held write of the file
//! and the zone database a recipient's wall time is read from is the one
//! the runtime bundles. A caller's own rusqlite transaction raises through
//! [`Notifier::raise_sqlite`].

#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod notifier;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod replies;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod routes;

#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use notifier::{
    AddIdentity, ChannelHealthOf, Clock, Expand, NoticeWork, Notifier, NotifierError,
    NotifierQueue, PasswordReset, PermissionsOf, RaiseInput, RecipientRef, Render, ReplyInput,
    ReplyOutcome, ResetIdentity, SendDirect,
};
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use replies::{approval_replies, ReplyAction, ReplyContext};
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use routes::NotifierRoutes;

use crate::auth::covers;
use crate::uuid62::{parse_uuid, uuid_to_base62};

/// How long a delivery that ended is kept before a prune deletes it.
pub const DEFAULT_DELIVERY_RETENTION_DAYS: u64 = 90;

/// How long a delivery that an adapter reports on may go unreported before
/// it falls through to the next channel.
pub const DEFAULT_DELIVERY_TIMEOUT_SECONDS: u64 = 300;

/// How long an adapter's health check holds before it is asked again, in
/// milliseconds.
pub const HEALTH_TTL_MS: u64 = 60_000;

/// How long a code sent to an address answers.
pub const CODE_TTL_SECONDS: u64 = 1800;

/// How many wrong answers a code takes before it is spent.
pub const CODE_ATTEMPTS: i64 = 5;

/// The channel kind of the in-app inbox, which every notice reaches.
pub const INAPP: &str = "inapp";

/// The urgencies of an event (D153), from the most pressing.
pub const URGENT: &str = "urgent";
/// Sent now, or at the end of the recipient's quiet hours.
pub const NORMAL: &str = "normal";
/// Batched into the recipient's next digest.
pub const DIGEST: &str = "digest";
/// History alone: a notice in the inbox, nothing sent.
pub const RECORD: &str = "record";

/// The verbs that acknowledge a notice.
pub const ACK_VERBS: &[&str] = &["ack", "ok", "acknowledge", "acknowledged"];

/// The verb that undoes the latest action on a notice.
pub const UNDO_VERB: &str = "undo";

/// The same id in one form, whatever form a UUID came in: a UUID's base62,
/// and any other text as it is.
pub fn same_id(id: &str) -> String {
    match parse_uuid(id) {
        Some(uuid) => uuid_to_base62(uuid),
        None => id.to_owned(),
    }
}

/// An address normalised as the kind of address it is: an email address in
/// lower case, a phone number as `+` and its digits, a handle trimmed.
pub fn normalise_address(kind: &str, address: &str) -> String {
    let text = address.trim();
    match kind {
        "email" => text.to_lowercase(),
        "phone" => {
            let digits: String = text.chars().filter(char::is_ascii_digit).collect();
            if text.starts_with('+') || text.starts_with("00") {
                format!("+{}", digits.strip_prefix("00").unwrap_or(&digits))
            } else {
                format!("+{digits}")
            }
        }
        _ => text.to_owned(),
    }
}

/// The kind of address a channel kind sends to unless the notifier is told
/// otherwise: email to email, sms and voice to phone, any other to handle.
pub fn default_address_kind(channel: &str) -> &'static str {
    match channel {
        "email" => "email",
        "sms" | "voice" => "phone",
        _ => "handle",
    }
}

/// The SHA-256 of a code, in hex, as the channel identities hold it: the
/// code trimmed and in upper case.
pub fn code_hash(code: &str) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, code.trim().to_uppercase().as_bytes());
    digest.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// A reply's words as the keyword interpreter reads them: its verb in
/// lower case, the reference it names after the verb (`done 142`, `ack
/// #7`), and the words after.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ReadReply {
    pub verb: String,
    pub reference: Option<i64>,
    pub args: Vec<String>,
}

impl ReadReply {
    /// Whether the verb acknowledges.
    pub fn acknowledges(&self) -> bool {
        ACK_VERBS.contains(&self.verb.as_str())
    }

    /// Whether the verb undoes.
    pub fn undoes(&self) -> bool {
        self.verb == UNDO_VERB
    }
}

/// Reads a reply's text: its first word is the verb, a second of digits
/// (with or without `#`) the reference, and the rest its words.
pub fn read_reply(text: &str) -> ReadReply {
    let mut words = text.split_whitespace();
    let verb = words.next().unwrap_or_default().to_lowercase();
    let mut args: Vec<String> = words.map(str::to_owned).collect();
    let mut reference = None;
    if let Some(first) = args.first() {
        let digits = first.strip_prefix('#').unwrap_or(first);
        if !digits.is_empty() && digits.chars().all(|c| c.is_ascii_digit()) {
            reference = digits.parse::<i64>().ok();
            if reference.is_some() {
                args.remove(0);
            }
        }
    }
    ReadReply {
        verb,
        reference,
        args,
    }
}

/// Whether a reply's text is shaped as a code an address answers its
/// verification with: four to twelve letters and digits.
pub fn looks_like_code(text: &str) -> bool {
    let text = text.trim();
    (4..=12).contains(&text.len()) && text.chars().all(|c| c.is_ascii_alphanumeric())
}

/// The minutes of the day of a time of day: `HH:MM`, or the `HH:MM:SS` a
/// `Temporal.Time` is stored as, its seconds dropped.
fn minutes_of(text: &str) -> Option<u32> {
    let (hour, rest) = text.split_once(':')?;
    let minute = match rest.split_once(':') {
        Some((minute, seconds)) => {
            let (whole, fraction) = seconds.split_once('.').unwrap_or((seconds, "0"));
            if whole.len() != 2
                || fraction.is_empty()
                || !whole
                    .chars()
                    .chain(fraction.chars())
                    .all(|c| c.is_ascii_digit())
            {
                return None;
            }
            minute
        }
        None => rest,
    };
    if hour.is_empty()
        || hour.len() > 2
        || minute.len() != 2
        || !hour
            .chars()
            .chain(minute.chars())
            .all(|c| c.is_ascii_digit())
    {
        return None;
    }
    Some(hour.parse::<u32>().ok()? * 60 + minute.parse::<u32>().ok()?)
}

/// How many seconds from a wall time (its minutes of the day and its
/// second) until the quiet hours `[start, end)` end, or 0 when it is
/// outside them. A window that crosses midnight (22:00 to 07:00) is read as
/// one. The notifier reads the wall time of an instant in a zone from
/// Postgres, which holds the zone database.
pub fn quiet_delay_seconds(
    wall_minutes: u32,
    second: u32,
    start: Option<&str>,
    end: Option<&str>,
) -> u64 {
    let (Some(from), Some(to)) = (start.and_then(minutes_of), end.and_then(minutes_of)) else {
        return 0;
    };
    if from == to {
        return 0;
    }
    let now = wall_minutes;
    let inside = if from < to {
        now >= from && now < to
    } else {
        now >= from || now < to
    };
    if !inside {
        return 0;
    }
    let minutes = (u64::from(to) + 1440 - u64::from(now)) % 1440;
    (minutes * 60).saturating_sub(u64::from(second))
}

/// Whether `held`, a principal's permissions, may use `need` within `cap`,
/// the permissions its channel's trust leaves a reply (`None`: no cap).
pub fn reply_allows(held: &[String], cap: Option<&[String]>, need: &str) -> bool {
    if !held.iter().any(|h| covers(h, need)) {
        return false;
    }
    match cap {
        None => true,
        Some(cap) => cap.iter().any(|c| covers(c, need)),
    }
}

/// `held` narrowed to `cap`: each held permission `cap` covers, and each of
/// `cap`'s that a held one covers, each once in that order.
pub fn narrowed_permissions(held: &[String], cap: Option<&[String]>) -> Vec<String> {
    let Some(cap) = cap else {
        return held.to_vec();
    };
    let mut out: Vec<String> = Vec::new();
    for h in held {
        for c in cap {
            let next = if covers(c, h) {
                Some(h)
            } else if covers(h, c) {
                Some(c)
            } else {
                None
            };
            if let Some(next) = next {
                if !out.contains(next) {
                    out.push(next.clone());
                }
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    fn strings(value: &Value) -> Vec<String> {
        value
            .as_array()
            .map(|items| {
                items
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default()
    }

    /// The vectors the TypeScript runtime's notify-parity test writes
    /// (`NOTIFY_PARITY_WRITE=1 bun test src/notify-parity.test.ts`).
    #[test]
    fn the_parity_vectors_hold() {
        let text = include_str!("../../../../testdata/notify_parity.json");
        let vectors: Value = serde_json::from_str(text).unwrap();
        for case in vectors["addresses"].as_array().unwrap() {
            let got = normalise_address(
                case["kind"].as_str().unwrap(),
                case["address"].as_str().unwrap(),
            );
            assert_eq!(got, case["want"].as_str().unwrap(), "address {case}");
        }
        for case in vectors["replies"].as_array().unwrap() {
            let text = case["text"].as_str().unwrap();
            let read = read_reply(text);
            let want = &case["want"];
            assert_eq!(read.verb, want["verb"].as_str().unwrap(), "reply {case}");
            assert_eq!(read.reference, want["reference"].as_i64(), "reply {case}");
            assert_eq!(read.args, strings(&want["args"]), "reply {case}");
            assert_eq!(
                read.acknowledges(),
                want["ack"].as_bool().unwrap(),
                "reply {case}"
            );
            assert_eq!(
                read.undoes(),
                want["undo"].as_bool().unwrap(),
                "reply {case}"
            );
            assert_eq!(
                looks_like_code(text),
                want["code"].as_bool().unwrap(),
                "reply {case}"
            );
        }
        for case in vectors["quietHours"].as_array().unwrap() {
            let wall = case["wall"].as_str().unwrap();
            let parts: Vec<u32> = wall.split(':').map(|p| p.parse().unwrap()).collect();
            let got = quiet_delay_seconds(
                parts[0] * 60 + parts[1],
                parts[2],
                case["start"].as_str(),
                case["end"].as_str(),
            );
            assert_eq!(got, case["want"].as_u64().unwrap(), "quiet hours {case}");
        }
        for case in vectors["trust"].as_array().unwrap() {
            let held = strings(&case["held"]);
            let cap = case["cap"].as_array().map(|_| strings(&case["cap"]));
            let need = case["need"].as_str().unwrap();
            assert_eq!(
                reply_allows(&held, cap.as_deref(), need),
                case["allows"].as_bool().unwrap(),
                "trust {case}"
            );
            assert_eq!(
                narrowed_permissions(&held, cap.as_deref()),
                strings(&case["narrowed"]),
                "trust {case}"
            );
        }
        for case in vectors["codeHashes"].as_array().unwrap() {
            assert_eq!(
                code_hash(case["code"].as_str().unwrap()),
                case["want"].as_str().unwrap(),
                "code {case}"
            );
        }
        for case in vectors["sameIds"].as_array().unwrap() {
            assert_eq!(
                same_id(case["id"].as_str().unwrap()),
                case["want"].as_str().unwrap(),
                "id {case}"
            );
        }
    }
}
