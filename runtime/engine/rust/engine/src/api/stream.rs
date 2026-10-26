//! The event log as server-sent events, the port of `src/http/stream.ts`
//! (D16, D58). The engine writes every byte; the host copies them to the
//! client as it pulls them ([`EventStream::next`]).
//!
//! A stream reads the log from a cursor in pages, as its principal, with
//! the read's filters, and sends each event as one message: `id:` is the
//! event's cursor and `data:` the event as JSON. The first time a page is
//! the last (`more` false), replay has caught up, and it sends a `ready`
//! event whose id and data carry the cursor. Then it waits on the storage's
//! notifier (a commit in any process, on Postgres) and reads on from its
//! cursor. While it waits it sends a comment every heartbeat, so proxies
//! keep the connection open and a dead client is found.
//!
//! A page's cursor moves past every event it scanned, the ones its filters
//! dropped too, so the stream's cursor can be ahead of the last event it
//! sent. A message with only `id:` moves the client's last event id up to
//! it: after a write whose last event is behind its cursor, for a page of
//! replay that kept no event, and, while it waits, in place of a
//! heartbeat's comment; so a client that reconnects with `Last-Event-ID`
//! scans again at most what one heartbeat let pass. The ready event's id
//! does the same.
//!
//! A stream opened with signals (D77) also sends each signal on an
//! instance its namespace, schema, instance and behavior filters keep,
//! whose schema its principal may read (asked once per schema between two
//! reads of the log, so a policy that stops letting it read a schema stops
//! its signals by the next read), as one message `event: signal` with no
//! `id:`, so `Last-Event-ID` and a reconnect's replay never see one. It holds up to 256 for a slow client,
//! the oldest dropped first, and sends them once replay has caught up.
//! Signals wake it (the notifier's wakes, not its epoch), and it reads the
//! log again only when a commit moved the epoch since its last read, or a
//! heartbeat went by since its last read, so a signal costs a waiting
//! stream no read, and signals sent more often than a heartbeat, which
//! put its keepalive off, never put off a read: a stream with no listener
//! still hears another process's commits within a heartbeat.
//!
//! The stream is pulled: it reads the next page only when the host has
//! taken the previous one, so it holds at most one page for a slow client.
//! It ends when the host closes it (the client went away), when the engine
//! shuts down, and when a read is refused (the policy changed, say); the
//! client then reconnects from its last id and gets the refusal as a
//! problem document.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, PoisonError, Weak};
use std::time::{Duration, Instant};

use serde_json::Value;

use super::problems::Problem;
use super::{ApiOptions, Body, Request, Response};
use crate::access::SchemaAction;
use crate::errors::{EngineError, Error};
use crate::events::{EventPage, EventsAfter, ReadEvents};
use crate::json;
use crate::paging::MAX_PAGE_SIZE;
use crate::principal::Principal;
use crate::signals::{SignalFilter, SignalQueue};
use crate::Engine;

/// Events read per page unless configured.
pub const DEFAULT_STREAM_PAGE_SIZE: i64 = 100;
/// Milliseconds between heartbeats while a stream waits unless configured:
/// under the 10 seconds after which Bun closes a connection that sends
/// nothing, and the shorter proxy timeouts.
pub const DEFAULT_HEARTBEAT_MS: u64 = 5_000;

/// The name of the event a stream sends once replay has caught up.
pub const READY_EVENT: &str = "ready";

/// The name of the event a stream opened with signals sends for each
/// signal (D77).
pub const SIGNAL_EVENT: &str = "signal";

/// The event stream's page size and heartbeat; each the default when
/// absent.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct StreamOptions {
    /// Events read per page, 1 to 500.
    pub page_size: Option<i64>,
    /// Milliseconds between heartbeats, at least 1.
    pub heartbeat_ms: Option<u64>,
}

impl StreamOptions {
    /// The options with their defaults, refused as the TypeScript app
    /// refuses them when they are out of range.
    pub fn check(&self) -> Result<(i64, Duration), String> {
        let page_size = self.page_size.unwrap_or(DEFAULT_STREAM_PAGE_SIZE);
        let heartbeat = self.heartbeat_ms.unwrap_or(DEFAULT_HEARTBEAT_MS);
        if !(1..=MAX_PAGE_SIZE).contains(&page_size) {
            return Err(format!(
                "stream.pageSize must be an integer from 1 to {MAX_PAGE_SIZE}, got {page_size}"
            ));
        }
        if heartbeat < 1 {
            return Err(format!(
                "stream.heartbeatMs must be a positive integer, got {heartbeat}"
            ));
        }
        Ok((page_size, Duration::from_millis(heartbeat)))
    }
}

/// What a pull of a stream gives: bytes to send, nothing yet (the pull's
/// timeout passed while the stream waited), or the stream's end.
#[derive(Debug, PartialEq, Eq)]
pub enum Pull {
    Data(Vec<u8>),
    Pending,
    End,
}

// The read a stream makes, owned.
#[derive(Clone, Debug)]
struct Read {
    namespace: String,
    schema: Option<String>,
    instance_id: Option<String>,
    kinds: Option<Vec<String>>,
    behaviors: Option<Vec<String>>,
    exclude: Option<Vec<String>>,
    beside: Option<bool>,
}

impl Read {
    fn at(&self, after: EventsAfter, limit: i64) -> ReadEvents<'_> {
        ReadEvents {
            namespace: Some(&self.namespace),
            schema: self.schema.as_deref(),
            instance_id: self.instance_id.as_deref(),
            after: Some(after),
            limit: Some(limit),
            kinds: self.kinds.clone(),
            behaviors: self.behaviors.clone(),
            exclude: self.exclude.clone(),
            beside: self.beside,
        }
    }
}

/// A stream of server-sent events of the log, as one response's body.
pub struct EventStream {
    engine: Weak<Engine>,
    principal: Principal,
    read: Read,
    page_size: i64,
    heartbeat: Duration,
    /// The page read before the response answered, sent first.
    first: Option<EventPage>,
    /// The cursor the stream reads on from.
    cursor: i64,
    /// The cursor the client would resume from.
    sent: i64,
    ready: bool,
    opened: bool,
    done: bool,
    /// When the heartbeat is due, while the stream waits.
    beat_at: Option<Instant>,
    /// The notifier's epoch when the stream last read the log.
    read_epoch: u64,
    /// Whether its last page was the last (`more` false).
    caught_up: bool,
    /// Whether it reads at its next pull whatever the epoch says: after a
    /// heartbeat, so a stream with no listener still reads at each.
    read_due: bool,
    /// When it last read the log. Caught up, it reads again once a
    /// heartbeat has gone by since, whatever the epoch says, even while
    /// signals keep putting its heartbeat off.
    last_read: Instant,
    /// Its signals, when it was opened with them (D77).
    signals: Option<StreamSignals>,
}

// A stream's signals: the queue the engine fills, and whether its
// principal may read each schema, asked once per schema and asked again
// after each read of the log.
struct StreamSignals {
    queue: Arc<SignalQueue>,
    readable: HashMap<String, bool>,
}

impl std::fmt::Debug for EventStream {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("EventStream")
            .field("namespace", &self.read.namespace)
            .field("cursor", &self.cursor)
            .field("sent", &self.sent)
            .field("done", &self.done)
            .finish_non_exhaustive()
    }
}

/// Opens the stream a read of the log asks for: it reads the first page
/// before it answers, so a refused read (403, 404, 400, 410) is a problem
/// document rather than a stream that ends at once.
pub(crate) fn open(
    engine: &Engine,
    options: &ApiOptions,
    request: &Request,
    principal: &Principal,
    read: &ReadEvents<'_>,
    signals: bool,
) -> Result<Response, Problem> {
    let stream = start_with(engine, &options.stream, principal, read, signals).map_err(
        |error| match error {
            Error::Engine(_) => Problem::of(&error),
            _ => Problem::internal(),
        },
    )?;
    Ok(Response {
        status: 200,
        headers: vec![
            ("content-type".to_owned(), "text/event-stream".to_owned()),
            ("cache-control".to_owned(), "no-store".to_owned()),
            ("x-request-id".to_owned(), request.request_id.clone()),
        ],
        body: Body::Stream(Box::new(stream)),
    })
}

/// Starts a stream of the log as `principal` reads it: its first page read
/// now, so a refused read is the read's error. The engine must be shared
/// ([`Engine::shared`]), since the stream outlives the call.
pub fn start(
    engine: &Engine,
    options: &StreamOptions,
    principal: &Principal,
    read: &ReadEvents<'_>,
) -> Result<EventStream, Error> {
    start_with(engine, options, principal, read, false)
}

/// [`start`], with the signals (D77) of the instances its namespace,
/// schema, instance and behavior filters keep when `signals` is true.
pub fn start_with(
    engine: &Engine,
    options: &StreamOptions,
    principal: &Principal,
    read: &ReadEvents<'_>,
    signals: bool,
) -> Result<EventStream, Error> {
    let (page_size, heartbeat) = options.check().map_err(EngineError::invalid_argument)?;
    let Some(weak) = engine.weak() else {
        return Err(Error::usage(
            "an event stream needs a shared engine (Engine::shared)",
        ));
    };
    let owned = Read {
        namespace: read.namespace.unwrap_or("default").to_owned(),
        schema: read.schema.map(str::to_owned),
        instance_id: read.instance_id.map(str::to_owned),
        kinds: read.kinds.clone(),
        behaviors: read.behaviors.clone(),
        exclude: read.exclude.clone(),
        beside: read.beside,
    };
    let after = read.after.unwrap_or(EventsAfter::Cursor(0));
    // The queue opens before the first page is read, so a signal sent
    // from now on waits for the stream.
    let signals = signals.then(|| StreamSignals {
        queue: engine.signals.open(SignalFilter {
            namespace: owned.namespace.clone(),
            schema: owned.schema.clone(),
            instance_id: owned.instance_id.clone(),
            behaviors: owned.behaviors.clone(),
        }),
        readable: HashMap::new(),
    });
    let read_epoch = engine.db().notifier().epoch();
    let first = engine
        .events()
        .read(principal, &owned.at(after, page_size))?;
    let cursor = match after {
        EventsAfter::Head => first.next,
        EventsAfter::Cursor(cursor) => cursor,
    };
    Ok(EventStream {
        engine: weak,
        principal: principal.clone(),
        read: owned,
        page_size,
        heartbeat,
        first: Some(first),
        cursor,
        sent: cursor,
        ready: false,
        opened: false,
        done: false,
        beat_at: None,
        read_epoch,
        caught_up: false,
        read_due: false,
        last_read: Instant::now(),
        signals,
    })
}

/// The event streams a caller of the JSON calls holds open
/// ([`crate::calls`]: `api.handle` and `stream.open` hand back an id,
/// `stream.next` pulls and `stream.close` ends one), so a binding that
/// passes JSON across its boundary can serve them.
#[derive(Default)]
pub(crate) struct OpenStreams {
    next: AtomicU64,
    open: Mutex<HashMap<u64, Box<EventStream>>>,
}

impl OpenStreams {
    /// Holds a stream open and returns its id.
    pub(crate) fn hold(&self, stream: Box<EventStream>) -> u64 {
        let id = self.next.fetch_add(1, Ordering::Relaxed) + 1;
        self.streams().insert(id, stream);
        id
    }

    /// Pulls a stream by its id, as [`EventStream::next`]; `None` for an
    /// id that names no open stream. A stream that ends is let go. The
    /// stream is out of the map while it waits, so pulls of others go on.
    pub(crate) fn next(&self, id: u64, timeout: Duration) -> Option<Pull> {
        let mut stream = self.streams().remove(&id)?;
        let pulled = stream.next(timeout);
        if pulled != Pull::End {
            self.streams().insert(id, stream);
        }
        Some(pulled)
    }

    /// Ends a stream by its id; false for one that is not open.
    pub(crate) fn close(&self, id: u64) -> bool {
        self.streams()
            .remove(&id)
            .map(|mut stream| stream.close())
            .is_some()
    }

    fn streams(&self) -> std::sync::MutexGuard<'_, HashMap<u64, Box<EventStream>>> {
        self.open.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl EventStream {
    /// The stream's next bytes, waiting up to `timeout` for a commit or a
    /// heartbeat while it has nothing to send: `Pending` when the timeout
    /// passes first (call again), `End` once it is over.
    pub fn next(&mut self, timeout: Duration) -> Pull {
        if !self.opened {
            // A comment first, so the response starts before any event
            // does.
            self.opened = true;
            return Pull::Data(b": open\n\n".to_vec());
        }
        let deadline = Instant::now() + timeout;
        loop {
            let Some(engine) = self.live() else {
                return Pull::End;
            };
            let notifier = Arc::clone(engine.db().notifier());
            let wakes = notifier.wakes();
            let epoch = notifier.epoch();
            // Caught up, it reads again only for a commit since its last
            // read, after a heartbeat, or once a heartbeat has gone by
            // since its last read: a signal's wake reads nothing.
            // The first page was read when the stream opened, at the epoch
            // start noted; any other is read now, at this one.
            let read_now = self.first.is_none();
            let page = match self.first.take() {
                Some(page) => Some(page),
                None if self.caught_up
                    && !self.read_due
                    && epoch == self.read_epoch
                    && self.last_read.elapsed() < self.heartbeat =>
                {
                    None
                }
                None => match engine.events().read(
                    &self.principal,
                    &self
                        .read
                        .at(EventsAfter::Cursor(self.cursor), self.page_size),
                ) {
                    Ok(page) => Some(page),
                    Err(_) => {
                        self.done = true;
                        return Pull::End;
                    }
                },
            };
            let mut frames = String::new();
            if let Some(page) = page {
                if read_now {
                    self.read_epoch = epoch;
                    self.read_due = false;
                    self.last_read = Instant::now();
                    // The policy is asked again for the signals that
                    // follow a read.
                    if let Some(signals) = self.signals.as_mut() {
                        signals.readable.clear();
                    }
                }
                self.caught_up = !page.more;
                self.cursor = page.next;
                for event in &page.events {
                    frames.push_str(&format!(
                        "id: {}\ndata: {}\n\n",
                        event.cursor,
                        json::stringify(&serde_json::to_value(event).unwrap_or(Value::Null))
                    ));
                }
                if let Some(last) = page.events.last() {
                    self.sent = last.cursor;
                }
                if !page.more && !self.ready {
                    self.ready = true;
                    self.sent = self.cursor;
                    frames.push_str(&format!(
                        "event: {READY_EVENT}\nid: {}\ndata: {{\"cursor\":{}}}\n\n",
                        self.cursor, self.cursor
                    ));
                } else if self.cursor > self.sent && (!frames.is_empty() || page.more) {
                    // Past the events the filters dropped: with the page's
                    // events, or alone for a page of replay that kept none.
                    self.sent = self.cursor;
                    frames.push_str(&format!("id: {}\n\n", self.cursor));
                }
            }
            if self.ready {
                self.signal_frames(&engine, &mut frames);
            }
            drop(engine);
            if !frames.is_empty() {
                self.beat_at = None;
                return Pull::Data(frames.into_bytes());
            }
            // Caught up, with nothing to send: wait for a commit, a signal,
            // a beat, or the read a heartbeat since the last one is due.
            let now = Instant::now();
            let beat_at = *self.beat_at.get_or_insert(now + self.heartbeat);
            let read_at = self.last_read + self.heartbeat;
            let until = beat_at.min(read_at).min(deadline);
            let moved = notifier.wait_wakes(wakes, until.saturating_duration_since(now)) > wakes;
            if moved {
                // A commit woke it: read again, with a fresh heartbeat. A
                // signal keeps the heartbeat where it was.
                if notifier.epoch() != epoch {
                    self.beat_at = None;
                }
                continue;
            }
            if Instant::now() >= beat_at {
                if self.live().is_none() {
                    return Pull::End;
                }
                self.beat_at = None;
                self.read_due = true;
                // A heartbeat carries the cursor when the filters dropped
                // what the stream read since its last write.
                let moved = self.cursor > self.sent;
                self.sent = self.cursor;
                return Pull::Data(if moved {
                    format!("id: {}\n\n", self.cursor).into_bytes()
                } else {
                    b": keepalive\n\n".to_vec()
                });
            }
            if Instant::now() >= read_at && Instant::now() < deadline {
                // A heartbeat since the last read, with the keepalive put
                // off by signals: read again.
                continue;
            }
            return Pull::Pending;
        }
    }

    // signal_frames appends a message for each signal the stream holds
    // whose schema its principal may read, asking the policy once per
    // schema between two reads of the log.
    fn signal_frames(&mut self, engine: &Engine, frames: &mut String) {
        let Some(signals) = self.signals.as_mut() else {
            return;
        };
        for signal in signals.queue.take() {
            let readable = *signals
                .readable
                .entry(signal.schema.clone())
                .or_insert_with(|| {
                    engine.access.allows(
                        &self.principal,
                        SchemaAction::Read,
                        &signal.namespace,
                        &signal.schema,
                    )
                });
            if !readable {
                continue;
            }
            frames.push_str(&format!(
                "event: {SIGNAL_EVENT}\ndata: {}\n\n",
                json::stringify(&serde_json::to_value(&*signal).unwrap_or(Value::Null))
            ));
        }
    }

    /// Ends the stream: the client went away. Every later pull is `End`.
    pub fn close(&mut self) {
        self.done = true;
    }

    /// The cursor a client that reconnected now would resume from: its
    /// last event id.
    pub fn resume_cursor(&self) -> i64 {
        self.sent
    }

    /// The storage's notifier the stream waits on, while the engine is
    /// open: what a host that waits for many streams at once blocks on,
    /// for its wakes ([`superschematic_engine_storage::Notifier::wakes`]),
    /// which a commit and a signal move.
    pub fn notifier(&self) -> Option<Arc<superschematic_engine_storage::Notifier>> {
        if self.done {
            return None;
        }
        self.engine
            .upgrade()
            .filter(|engine| !engine.is_closed())
            .map(|engine| Arc::clone(engine.db().notifier()))
    }

    // live is the engine while the stream goes on.
    fn live(&mut self) -> Option<Arc<Engine>> {
        if self.done {
            return None;
        }
        match self.engine.upgrade() {
            Some(engine) if !engine.is_closed() => Some(engine),
            _ => {
                self.done = true;
                None
            }
        }
    }
}
