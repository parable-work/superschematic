//! The log a generated Rust server, job or worker writes (D57, D65; section
//! 8.6 of `docs/stack-model.md`): one JSON object per line on stdout, in the
//! shape the TypeScript runtime's `createLogger` writes and the Go server's
//! zap logger reads alike:
//!
//! ```text
//! {"level":"info","time":"2026-10-12T17:32:05.123Z","msg":"listening","stack":"shop-stack","server":"shop-stock","addr":":8080"}
//! ```
//!
//! `level` is the event's level in lower case (`debug`, `info`, `warn`,
//! `error`, and `trace`, which TypeScript does not have), `time` is RFC 3339
//! in UTC to the millisecond, and `msg` is the event's message. Then come
//! the fields every line carries, `stack` and `server`, which a job's line
//! names `job` and a worker's `worker` ([`init_json_as`]); the fields of the
//! spans the event is in, the outermost first, such as the `api` of an
//! [`api_span`], but a span's `otel.` fields, which are OpenTelemetry's
//! (D67); and the event's own. A later field replaces an earlier one
//! of the same name in its place, and none replaces `level`, `time` or
//! `msg`; a `message` field beside the event's message (`info!(message =
//! id, "handled")`, as a worker logs a message's ID) is a field of that
//! name. A string, number or boolean field is written as itself, an error
//! (`error = &err as &dyn Error`) as its message, and any other value as
//! its `Debug` or, with `%`, its `Display`.
//!
//! [`init_json`] installs it for the process at `info`, as zap's production
//! logger and the TypeScript logger default to. It is a
//! `tracing-subscriber` layer ([`json_lines`]) over the registry, with no
//! formatter of tracing-subscriber's, whose JSON names its members
//! otherwise.

use std::error::Error;
use std::fmt;
use std::io::Write as _;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::Value;
use tracing::field::{Field, Visit};
use tracing::level_filters::LevelFilter;
use tracing::span::{Attributes, Id, Record};
use tracing::{Event, Level, Metadata, Subscriber};
use tracing_subscriber::layer::{Context, Layer, SubscriberExt as _};
use tracing_subscriber::registry::LookupSpan;

/// The crate a server logs through, re-exported so a generated crate's
/// `tracing::info!` and `Instrument` are the version this layer reads.
pub use tracing;

/// The members a line's head takes, which no field replaces.
const RESERVED: [&str; 3] = ["level", "time", "msg"];

/// Installs [`json_lines`] for the process, writing to stdout at `info`.
/// It fails when the process already has a subscriber.
pub fn init_json(
    stack: &str,
    server: &str,
) -> Result<(), tracing::subscriber::SetGlobalDefaultError> {
    init_json_as(stack, SERVER, server)
}

/// [`init_json`] for a deployable of the kind `kind`, the field that names
/// it on every line: a job's entrypoint installs it with [`JOB`] and a
/// worker's with [`WORKER`] (D65), as Go's and TypeScript's job and worker
/// loggers name theirs. It fails when the process already has a subscriber.
pub fn init_json_as(
    stack: &str,
    kind: &str,
    name: &str,
) -> Result<(), tracing::subscriber::SetGlobalDefaultError> {
    tracing::subscriber::set_global_default(
        tracing_subscriber::registry().with(json_lines_as(stack, kind, name)),
    )
}

/// The field that names a server on every line of its log.
pub const SERVER: &str = "server";

/// The field that names a job on every line of its log.
pub const JOB: &str = "job";

/// The field that names a worker on every line of its log.
pub const WORKER: &str = "worker";

/// The span of one API of the server: each line written in it carries
/// `"api": api`. The generated `Deps` holds one, and the entrypoint runs the
/// API's work in it (`span.in_scope`, or `tracing::Instrument::instrument`
/// on a future). It is an error-level span, so that it is enabled whatever
/// level the log writes, and every line in it names its API.
pub fn api_span(api: &str) -> tracing::Span {
    tracing::error_span!("api", api = api)
}

/// The layer that writes each event as a JSON line, with `stack` and
/// `server` on every line.
pub fn json_lines(stack: &str, server: &str) -> JsonLines {
    json_lines_as(stack, SERVER, server)
}

/// [`json_lines`] with `kind` in place of `server`: `stack` and `kind`, the
/// field that names the deployable, on every line. A reserved name
/// (`level`, `time` or `msg`) stays the head's, and leaves the line without
/// the deployable's name.
pub fn json_lines_as(stack: &str, kind: &str, name: &str) -> JsonLines {
    JsonLines {
        fields: vec![
            ("stack".to_string(), Value::from(stack)),
            (kind.to_string(), Value::from(name)),
        ],
        level: LevelFilter::INFO,
        write: Arc::new(|line: &str| {
            let mut out = std::io::stdout().lock();
            // A line that cannot be written is lost, as console.log loses one.
            let _ = out.write_all(format!("{line}\n").as_bytes());
        }),
        now: Arc::new(SystemTime::now),
    }
}

/// A `tracing-subscriber` layer writing one JSON object per event; see the
/// module's documentation.
#[derive(Clone)]
pub struct JsonLines {
    fields: Vec<(String, Value)>,
    level: LevelFilter,
    write: Arc<dyn Fn(&str) + Send + Sync>,
    now: Arc<dyn Fn() -> SystemTime + Send + Sync>,
}

impl JsonLines {
    /// The most verbose level written; `info` by default.
    #[must_use]
    pub fn with_level(mut self, level: LevelFilter) -> Self {
        self.level = level;
        self
    }

    /// Writes each line, without its newline, in place of stdout.
    #[must_use]
    pub fn with_writer(mut self, write: impl Fn(&str) + Send + Sync + 'static) -> Self {
        self.write = Arc::new(write);
        self
    }

    /// The clock of each line's `time`, for tests.
    #[must_use]
    pub fn with_clock(mut self, now: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.now = Arc::new(now);
        self
    }
}

impl fmt::Debug for JsonLines {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("JsonLines")
            .field("fields", &self.fields)
            .field("level", &self.level)
            .finish_non_exhaustive()
    }
}

/// A span's fields, kept in its extensions.
struct SpanFields(Vec<(String, Value)>);

/// Sets `name` to `value` in `fields`: in its place when it is there, and
/// last otherwise.
fn set(fields: &mut Vec<(String, Value)>, name: &str, value: Value) {
    match fields.iter_mut().find(|(field, _)| field == name) {
        Some((_, slot)) => *slot = value,
        None => fields.push((name.to_string(), value)),
    }
}

/// Records a span's or an event's fields as JSON values. An event's first
/// `message` that is text, which tracing's macros record first, is its
/// message, kept apart; a later one, as in `info!(message = id, "handled")`,
/// is a field of that name.
struct Fields<'a> {
    fields: &'a mut Vec<(String, Value)>,
    message: Option<&'a mut Option<String>>,
}

impl<'a> Fields<'a> {
    fn span(fields: &'a mut Vec<(String, Value)>) -> Self {
        Self {
            fields,
            message: None,
        }
    }

    fn event(fields: &'a mut Vec<(String, Value)>, message: &'a mut Option<String>) -> Self {
        Self {
            fields,
            message: Some(message),
        }
    }

    fn put(&mut self, name: &str, value: Value) {
        // A span's `otel.` fields are OpenTelemetry's (`otel.name`,
        // `otel.kind`), which the telemetry module's spans set for its
        // layer, and not the log's (D67).
        if self.message.is_none() && name.starts_with("otel.") {
            return;
        }
        match (&mut self.message, value) {
            (Some(slot), Value::String(text)) if name == "message" && slot.is_none() => {
                **slot = Some(text);
            }
            (_, value) => set(self.fields, name, value),
        }
    }
}

impl Visit for Fields<'_> {
    fn record_str(&mut self, field: &Field, value: &str) {
        self.put(field.name(), Value::from(value));
    }

    fn record_bool(&mut self, field: &Field, value: bool) {
        self.put(field.name(), Value::from(value));
    }

    fn record_i64(&mut self, field: &Field, value: i64) {
        self.put(field.name(), Value::from(value));
    }

    fn record_u64(&mut self, field: &Field, value: u64) {
        self.put(field.name(), Value::from(value));
    }

    fn record_i128(&mut self, field: &Field, value: i128) {
        let value =
            i64::try_from(value).map_or_else(|_| Value::from(value.to_string()), Value::from);
        self.put(field.name(), value);
    }

    fn record_u128(&mut self, field: &Field, value: u128) {
        let value =
            u64::try_from(value).map_or_else(|_| Value::from(value.to_string()), Value::from);
        self.put(field.name(), value);
    }

    fn record_f64(&mut self, field: &Field, value: f64) {
        // JSON has no NaN or infinity, which serde_json writes as null.
        let value = serde_json::Number::from_f64(value)
            .map_or_else(|| Value::from(value.to_string()), Value::Number);
        self.put(field.name(), value);
    }

    fn record_error(&mut self, field: &Field, value: &(dyn Error + 'static)) {
        self.put(field.name(), Value::from(value.to_string()));
    }

    fn record_debug(&mut self, field: &Field, value: &dyn fmt::Debug) {
        self.put(field.name(), Value::from(format!("{value:?}")));
    }
}

impl<S> Layer<S> for JsonLines
where
    S: Subscriber + for<'a> LookupSpan<'a>,
{
    fn enabled(&self, metadata: &Metadata<'_>, _ctx: Context<'_, S>) -> bool {
        self.level >= *metadata.level()
    }

    fn max_level_hint(&self) -> Option<LevelFilter> {
        Some(self.level)
    }

    fn on_new_span(&self, attrs: &Attributes<'_>, id: &Id, ctx: Context<'_, S>) {
        let mut fields = Vec::new();
        attrs.record(&mut Fields::span(&mut fields));
        if let Some(span) = ctx.span(id) {
            span.extensions_mut().insert(SpanFields(fields));
        }
    }

    fn on_record(&self, id: &Id, values: &Record<'_>, ctx: Context<'_, S>) {
        if let Some(span) = ctx.span(id) {
            if let Some(SpanFields(fields)) = span.extensions_mut().get_mut::<SpanFields>() {
                values.record(&mut Fields::span(fields));
            }
        }
    }

    fn on_event(&self, event: &Event<'_>, ctx: Context<'_, S>) {
        let mut fields = self.fields.clone();
        if let Some(scope) = ctx.event_scope(event) {
            for span in scope.from_root() {
                if let Some(SpanFields(span_fields)) = span.extensions().get::<SpanFields>() {
                    for (name, value) in span_fields {
                        set(&mut fields, name, value.clone());
                    }
                }
            }
        }
        let mut own = Vec::new();
        let mut msg = None;
        event.record(&mut Fields::event(&mut own, &mut msg));
        for (name, value) in own {
            set(&mut fields, &name, value);
        }
        let msg = msg.unwrap_or_default();
        let head = [
            ("level", Value::from(level_name(*event.metadata().level()))),
            ("time", Value::from(format_time((self.now)()))),
            ("msg", Value::from(msg)),
        ];
        let mut line = String::from("{");
        let members = head.iter().map(|(name, value)| (*name, value)).chain(
            fields
                .iter()
                .filter(|(name, _)| !RESERVED.contains(&name.as_str()))
                .map(|(name, value)| (name.as_str(), value)),
        );
        for (i, (name, value)) in members.enumerate() {
            if i > 0 {
                line.push(',');
            }
            line.push_str(&serde_json::to_string(name).unwrap_or_default());
            line.push(':');
            line.push_str(&serde_json::to_string(value).unwrap_or_else(|_| "null".to_string()));
        }
        line.push('}');
        (self.write)(&line);
    }
}

fn level_name(level: Level) -> &'static str {
    match level {
        Level::TRACE => "trace",
        Level::DEBUG => "debug",
        Level::INFO => "info",
        Level::WARN => "warn",
        Level::ERROR => "error",
    }
}

/// `t` in RFC 3339 in UTC to the millisecond, as JavaScript's
/// `toISOString` writes it: `2026-10-12T17:32:05.123Z`.
fn format_time(t: SystemTime) -> String {
    let millis = t
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX));
    let seconds = millis.div_euclid(1000);
    let days = seconds.div_euclid(86_400);
    let of_day = seconds.rem_euclid(86_400);
    let (year, month, day) = crate::civil::from_days(days);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60,
        millis.rem_euclid(1000)
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;
    use std::time::Duration;

    /// 2026-10-12T17:32:05.123Z.
    fn at() -> SystemTime {
        UNIX_EPOCH + Duration::from_millis(1_791_826_325_123)
    }

    fn capture(level: LevelFilter) -> (impl Subscriber + Send + Sync, Arc<Mutex<Vec<String>>>) {
        let lines = Arc::new(Mutex::new(Vec::new()));
        let sink = Arc::clone(&lines);
        let layer = json_lines("shop-stack", "shop-stock")
            .with_level(level)
            .with_clock(at)
            .with_writer(move |line| sink.lock().unwrap().push(line.to_string()));
        (tracing_subscriber::registry().with(layer), lines)
    }

    #[test]
    fn a_line_is_the_typescript_loggers() {
        let (subscriber, lines) = capture(LevelFilter::INFO);
        tracing::subscriber::with_default(subscriber, || {
            tracing::info!(addr = ":8080", "listening");
            let api = api_span("shop-stock");
            api.in_scope(|| {
                let err = std::io::Error::other("connection refused");
                tracing::warn!(
                    dependency = "shop-db",
                    error = &err as &dyn Error,
                    attempts = 3,
                    ready = false,
                    ratio = 0.5,
                    "not ready"
                );
                tracing::debug!("left out at info");
            });
        });
        assert_eq!(
            lines.lock().unwrap().as_slice(),
            [
                r#"{"level":"info","time":"2026-10-12T17:32:05.123Z","msg":"listening","stack":"shop-stack","server":"shop-stock","addr":":8080"}"#,
                r#"{"level":"warn","time":"2026-10-12T17:32:05.123Z","msg":"not ready","stack":"shop-stack","server":"shop-stock","api":"shop-stock","dependency":"shop-db","error":"connection refused","attempts":3,"ready":false,"ratio":0.5}"#,
            ]
        );
    }

    #[test]
    fn a_later_field_replaces_an_earlier_one_in_its_place_and_none_the_head() {
        let (subscriber, lines) = capture(LevelFilter::DEBUG);
        tracing::subscriber::with_default(subscriber, || {
            let outer = tracing::info_span!("outer", api = "a", request = tracing::field::Empty);
            outer.record("request", "r-1");
            outer.in_scope(|| {
                tracing::info_span!("inner", api = "b").in_scope(|| {
                    tracing::debug!(server = "other", level = "loud", msg = "no", value = ?Some(1), "{}", "formatted");
                });
            });
        });
        assert_eq!(
            lines.lock().unwrap().as_slice(),
            [
                r#"{"level":"debug","time":"2026-10-12T17:32:05.123Z","msg":"formatted","stack":"shop-stack","server":"other","api":"b","request":"r-1","value":"Some(1)"}"#
            ]
        );
    }

    #[test]
    fn a_message_field_beside_the_events_message_is_a_field() {
        let (subscriber, lines) = capture(LevelFilter::INFO);
        tracing::subscriber::with_default(subscriber, || {
            tracing::info!(message = "m-1", attempt = 2, "message handled");
            tracing::info!(message = "only a message");
        });
        assert_eq!(
            lines.lock().unwrap().as_slice(),
            [
                r#"{"level":"info","time":"2026-10-12T17:32:05.123Z","msg":"message handled","stack":"shop-stack","server":"shop-stock","message":"m-1","attempt":2}"#,
                r#"{"level":"info","time":"2026-10-12T17:32:05.123Z","msg":"only a message","stack":"shop-stack","server":"shop-stock"}"#,
            ]
        );
    }

    #[test]
    fn an_api_span_names_its_api_at_every_level() {
        let (subscriber, lines) = capture(LevelFilter::ERROR);
        tracing::subscriber::with_default(subscriber, || {
            api_span("shop-stock").in_scope(|| tracing::error!("down"));
        });
        assert_eq!(
            lines.lock().unwrap().as_slice(),
            [
                r#"{"level":"error","time":"2026-10-12T17:32:05.123Z","msg":"down","stack":"shop-stack","server":"shop-stock","api":"shop-stock"}"#
            ]
        );
    }

    #[test]
    fn a_job_or_a_worker_is_named_by_its_kind() {
        for (kind, name) in [
            (JOB, "shop-stock-order-restocks"),
            (WORKER, "shop-stock-reorder-stock"),
        ] {
            let lines = Arc::new(Mutex::new(Vec::new()));
            let sink = Arc::clone(&lines);
            let layer = json_lines_as("shop-stack", kind, name)
                .with_clock(at)
                .with_writer(move |line| sink.lock().unwrap().push(line.to_string()));
            tracing::subscriber::with_default(tracing_subscriber::registry().with(layer), || {
                tracing::info!(api = "shop-stock", "started");
            });
            assert_eq!(
                lines.lock().unwrap().as_slice(),
                [format!(
                    r#"{{"level":"info","time":"2026-10-12T17:32:05.123Z","msg":"started","stack":"shop-stack","{kind}":"{name}","api":"shop-stock"}}"#
                )]
            );
        }
    }

    #[test]
    fn the_time_is_rfc_3339_in_utc_to_the_millisecond() {
        assert_eq!(format_time(UNIX_EPOCH), "1970-01-01T00:00:00.000Z");
        assert_eq!(format_time(at()), "2026-10-12T17:32:05.123Z");
        assert_eq!(
            format_time(UNIX_EPOCH + Duration::from_millis(951_782_400_999)),
            "2000-02-29T00:00:00.999Z"
        );
    }
}
