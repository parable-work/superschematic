//! Signals (D77): a message about one instance that the engine delivers to
//! the event streams open on it and keeps nowhere. A behavior declares its
//! signals, each with a JSON Schema of its data and who may send it
//! (`read`, `write` or `operation`, [`SignalAccess`]). A caller sends one
//! with [`Signals::send`] (`engine.signals.send`, `POST
//! .../instances/{id}/signals/{behavior}/{name}`); a behavior's operation
//! with its context's `signal(name, data)`, delivered once the call commits
//! and dropped when it rolls back. The engine stamps `from`, the sender's
//! subject, and `at`, its clock's time, and appends no event.
//!
//! A signal travels as its JSON ([`EngineSignal`]) on the storage's
//! carrier (`Tx::signal`): in this process on SQLite, through the engine's
//! own `pg_notify` channel on Postgres, so every process's streams hear
//! what any process sends. The [`SignalHub`] is this engine's sink: it hands
//! each signal to the queues of the streams opened with signals whose
//! filters keep it, each at most [`SIGNAL_BUFFER`] long, the oldest dropped
//! when it is full, and stirs the notifier so a waiting stream sends it.
//! Delivery is at most once and in no order with the events.
//!
//! A caller's signals are limited per principal's subject and instance by a
//! token bucket of the engine's `signals.perSecond` (20 when absent), whose
//! burst is the same; over it the send is refused `rate_limited` (429). An
//! operation's signals are not limited. The limit is each process's own.

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex, PoisonError, Weak};

use serde::{Deserialize, Serialize};
use serde_json::Value;

use superschematic_engine_storage::{Notifier, SignalSink, Tx, MAX_SIGNAL_BYTES};

use crate::access::SchemaAction;
use crate::behaviors::declaration::SignalAccess;
use crate::behaviors::registry::{detail, SignalSpec};
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::json;
use crate::namespaces::NamespaceView;
use crate::principal::{check_principal, Principal};
use crate::registry::document::check_schema_name;
use crate::registry::Which;

/// The most a signal's data may be, in bytes of JSON.
pub const MAX_SIGNAL_DATA_BYTES: usize = 4096;

/// The signals a principal may send on one instance each second, and its
/// burst, unless the engine's options say (`signals.perSecond`).
pub const DEFAULT_SIGNALS_PER_SECOND: u32 = 20;

/// How many signals a stream holds for a slow client; past it the oldest
/// is dropped.
pub const SIGNAL_BUFFER: usize = 256;

/// The prefix of the operation the access policy is asked a signal's send
/// by: `signal:<name>`.
pub const SIGNAL_OPERATION_PREFIX: &str = "signal:";

/// One signal as a stream delivers it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EngineSignal {
    pub namespace: String,
    pub schema: String,
    pub instance_id: String,
    pub behavior: String,
    pub name: String,
    pub data: Value,
    /// The sender's subject: the caller's, or the operation's principal's.
    pub from: String,
    /// The engine's clock when it was sent, in epoch milliseconds.
    pub at: i64,
}

/// What a caller sends ([`Signals::send`]).
#[derive(Clone, Copy, Debug)]
pub struct SendSignal<'a> {
    /// The instance's namespace; `default` when absent.
    pub namespace: Option<&'a str>,
    pub schema: &'a str,
    pub instance_id: &'a str,
    /// The behavior that declares the signal.
    pub behavior: &'a str,
    pub name: &'a str,
    pub data: &'a Value,
}

/// The engine's signal options: how many a principal may send on one
/// instance each second, which is also its burst; 20 when absent.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SignalOptions {
    pub per_second: Option<u32>,
}

impl SignalOptions {
    /// Reads `{ perSecond? }`, the `signals` member of an engine's options.
    pub fn from_json(given: &Value) -> Result<SignalOptions> {
        const SHAPE: &str = "signals is { perSecond? }: the signals a principal may send on one instance each second";
        let Value::Object(members) = given else {
            return Err(Error::usage(SHAPE));
        };
        if let Some(unknown) = members.keys().find(|key| key.as_str() != "perSecond") {
            return Err(Error::usage(format!(
                "{SHAPE}: {} is not one of its members",
                json::quote(unknown)
            )));
        }
        let per_second = match members.get("perSecond").filter(|value| !value.is_null()) {
            None => None,
            Some(given) => Some(
                given
                    .as_u64()
                    .filter(|rate| *rate >= 1)
                    .and_then(|rate| u32::try_from(rate).ok())
                    .ok_or_else(|| {
                        Error::usage(format!(
                            "signals.perSecond is a positive integer, got {}",
                            crate::calls::js_string(given)
                        ))
                    })?,
            ),
        };
        Ok(SignalOptions { per_second })
    }
}

/// The engine's signals ([`Engine::signals`]).
pub struct Signals<'e> {
    pub(crate) engine: &'e Engine,
}

impl Signals<'_> {
    /// Sends a signal as `principal`: the schema's live version composes
    /// the behavior, which declares the signal with access `read` or
    /// `write` (`operation` is `forbidden` to a caller); the policy allows
    /// that access with the operation `signal:<name>`; the data is at most
    /// [`MAX_SIGNAL_DATA_BYTES`] of JSON its schema accepts
    /// (`invalid_argument`); the instance exists; and the principal is
    /// within its rate on the instance (`rate_limited`). It is delivered to
    /// the streams open on it once the check's read commits.
    pub fn send(&self, principal: &Principal, input: &SendSignal<'_>) -> Result<()> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(input.namespace)?;
            let spec = find_signal(engine, tx, &view, principal, &namespace, input)?;
            if spec.access == SignalAccess::Operation {
                return Err(EngineError::new(
                    EngineErrorCode::Forbidden,
                    format!(
                        "signal {} of {} is sent by {}'s operations alone",
                        input.name, input.behavior, input.behavior
                    ),
                )
                .into());
            }
            engine.access.require_operation(
                principal,
                if spec.access == SignalAccess::Write {
                    SchemaAction::Write
                } else {
                    SchemaAction::Read
                },
                &namespace,
                input.schema,
                Some(&format!("{SIGNAL_OPERATION_PREFIX}{}", input.name)),
            )?;
            if let Some(problem) = data_problem(&spec, input.behavior, input.data) {
                return Err(EngineError::invalid_argument(problem).into());
            }
            crate::instances::existing(tx, &namespace, input.schema, input.instance_id)?;
            let signal = EngineSignal {
                namespace,
                schema: input.schema.to_owned(),
                instance_id: input.instance_id.to_owned(),
                behavior: input.behavior.to_owned(),
                name: input.name.to_owned(),
                data: input.data.clone(),
                from: principal.subject.clone(),
                at: engine.clock.now(),
            };
            let payload = payload_of(&signal).map_err(EngineError::invalid_argument)?;
            engine.signals.limiter.take(&signal)?;
            tx.signal(&payload)?;
            Ok(())
        })
    }
}

// find_signal is the declaration of the signal a send names on the
// schema's live version. A principal that may read the schema learns what
// is missing; one that may not is refused.
fn find_signal(
    engine: &Engine,
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    principal: &Principal,
    namespace: &str,
    input: &SendSignal<'_>,
) -> Result<SignalHandle> {
    check_schema_name(input.schema)?;
    let access = &engine.access;
    let missing = |message: String| -> Result<SignalHandle> {
        access.require(principal, SchemaAction::Read, namespace, input.schema)?;
        Err(EngineError::new(EngineErrorCode::NotFound, message).into())
    };
    let Some(record) = engine
        .catalog
        .find(tx, view, input.schema, namespace, Which::Live)?
    else {
        return missing(format!(
            "schema {} has no live version in namespace {namespace}",
            input.schema
        ));
    };
    let runtime = engine.catalog.runtime_of(tx, &record)?;
    let Some(bound) = runtime
        .composition
        .behaviors
        .iter()
        .find(|bound| bound.behavior.name == input.behavior)
    else {
        return missing(format!(
            "{} does not compose behavior {}",
            input.schema, input.behavior
        ));
    };
    let Some(position) = bound
        .behavior
        .signals
        .iter()
        .position(|signal| signal.name == input.name)
    else {
        return missing(format!(
            "behavior {} declares no signal {}",
            input.behavior, input.name
        ));
    };
    Ok(SignalHandle {
        behavior: Arc::clone(&bound.behavior),
        position,
    })
}

// A declared signal, held by its behavior.
pub(crate) struct SignalHandle {
    behavior: Arc<crate::behaviors::registry::RegisteredBehavior>,
    position: usize,
}

impl std::ops::Deref for SignalHandle {
    type Target = SignalSpec;

    fn deref(&self) -> &SignalSpec {
        &self.behavior.signals[self.position]
    }
}

/// Why a signal's data is refused: longer than [`MAX_SIGNAL_DATA_BYTES`],
/// or one its schema refuses; `None` when it is not.
pub(crate) fn data_problem(spec: &SignalSpec, behavior: &str, data: &Value) -> Option<String> {
    if let Some((path, problem)) = json::first_not_json(data) {
        return Some(format!(
            "signal {} of {behavior}: its data at {} is {problem}",
            spec.name,
            json::path_pointer(&path)
        ));
    }
    let bytes = json::stringify(data).len();
    if bytes > MAX_SIGNAL_DATA_BYTES {
        return Some(format!(
            "signal {} of {behavior}: its data is {bytes} bytes of JSON, more than {MAX_SIGNAL_DATA_BYTES}",
            spec.name
        ));
    }
    if spec.params.is_valid(data) {
        return None;
    }
    Some(format!(
        "signal {} of {behavior}: its data does not match its paramsSchema: {}",
        spec.name,
        detail(&spec.params, data)
    ))
}

/// A signal as the storage carries it: its JSON, refused when it would not
/// fit a Postgres notification.
pub(crate) fn payload_of(signal: &EngineSignal) -> Result<String, String> {
    let payload = json::stringify(&serde_json::to_value(signal).unwrap_or(Value::Null));
    if payload.len() > MAX_SIGNAL_BYTES {
        return Err(format!(
            "signal {} of {} on {} {} is {} bytes with its envelope, more than a signal carries ({MAX_SIGNAL_BYTES})",
            signal.name,
            signal.behavior,
            signal.schema,
            signal.instance_id,
            payload.len()
        ));
    }
    Ok(payload)
}

/// What a stream keeps of the signals ([`SignalHub::open`]): its
/// namespace, and the schema, instance and behaviors its filters name.
#[derive(Clone, Debug, Default)]
pub(crate) struct SignalFilter {
    pub namespace: String,
    pub schema: Option<String>,
    pub instance_id: Option<String>,
    pub behaviors: Option<Vec<String>>,
}

impl SignalFilter {
    fn keeps(&self, signal: &EngineSignal) -> bool {
        signal.namespace == self.namespace
            && self
                .schema
                .as_ref()
                .is_none_or(|schema| *schema == signal.schema)
            && self
                .instance_id
                .as_ref()
                .is_none_or(|id| *id == signal.instance_id)
            && self
                .behaviors
                .as_ref()
                .is_none_or(|behaviors| behaviors.contains(&signal.behavior))
    }
}

/// A stream's signals, held for it until it sends them.
pub(crate) struct SignalQueue {
    filter: SignalFilter,
    held: Mutex<VecDeque<Arc<EngineSignal>>>,
}

impl SignalQueue {
    /// The signals held, oldest first, taken.
    pub fn take(&self) -> Vec<Arc<EngineSignal>> {
        self.held().drain(..).collect()
    }

    fn push(&self, signal: &Arc<EngineSignal>) -> bool {
        if !self.filter.keeps(signal) {
            return false;
        }
        let mut held = self.held();
        if held.len() >= SIGNAL_BUFFER {
            held.pop_front();
        }
        held.push_back(Arc::clone(signal));
        true
    }

    fn held(&self) -> std::sync::MutexGuard<'_, VecDeque<Arc<EngineSignal>>> {
        self.held.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// The engine's sink of signals: the queues of its streams, and the rate
/// limit of its callers.
pub(crate) struct SignalHub {
    queues: Arc<Queues>,
    /// What the storage hands each signal to; held here, so the storage's
    /// weak hold of it lasts as long as the engine.
    _sink: Arc<SignalSink>,
    pub(crate) limiter: RateLimiter,
}

struct Queues {
    open: Mutex<Vec<Weak<SignalQueue>>>,
    notifier: Arc<Notifier>,
}

impl Queues {
    fn deliver(&self, payload: &str) {
        let Ok(signal) = serde_json::from_str::<EngineSignal>(payload) else {
            return;
        };
        let signal = Arc::new(signal);
        let open: Vec<Arc<SignalQueue>> = {
            let mut open = self.open.lock().unwrap_or_else(PoisonError::into_inner);
            open.retain(|queue| queue.strong_count() > 0);
            open.iter().filter_map(Weak::upgrade).collect()
        };
        let mut kept = false;
        for queue in open {
            kept |= queue.push(&signal);
        }
        if kept {
            self.notifier.stir();
        }
    }
}

impl SignalHub {
    /// The hub of an engine on `db`, subscribed to its signals.
    pub fn new(db: &superschematic_engine_storage::Db, options: SignalOptions) -> SignalHub {
        let queues = Arc::new(Queues {
            open: Mutex::new(Vec::new()),
            notifier: Arc::clone(db.notifier()),
        });
        let weak = Arc::downgrade(&queues);
        let sink: Arc<SignalSink> = Arc::new(move |payload: &str| {
            if let Some(queues) = weak.upgrade() {
                queues.deliver(payload);
            }
        });
        db.signals().subscribe(&sink);
        SignalHub {
            queues,
            _sink: sink,
            limiter: RateLimiter::new(options.per_second.unwrap_or(DEFAULT_SIGNALS_PER_SECOND)),
        }
    }

    /// A stream's queue, which holds every signal its filter keeps from
    /// now until it is dropped.
    pub fn open(&self, filter: SignalFilter) -> Arc<SignalQueue> {
        let queue = Arc::new(SignalQueue {
            filter,
            held: Mutex::new(VecDeque::new()),
        });
        self.queues
            .open
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .push(Arc::downgrade(&queue));
        queue
    }
}

/// How many entries the limiter keeps before it forgets the full buckets.
const LIMITER_ENTRIES: usize = 4096;

/// A token bucket per principal's subject and instance.
pub(crate) struct RateLimiter {
    per_second: u32,
    buckets: Mutex<HashMap<BucketKey, Bucket>>,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
struct BucketKey {
    subject: String,
    namespace: String,
    schema: String,
    id: String,
}

#[derive(Clone, Copy, Debug)]
struct Bucket {
    tokens: f64,
    at: i64,
}

impl RateLimiter {
    fn new(per_second: u32) -> RateLimiter {
        RateLimiter {
            per_second,
            buckets: Mutex::new(HashMap::new()),
        }
    }

    // take spends a token of the sender's bucket on the instance, at the
    // signal's time, or refuses the send `rate_limited`.
    fn take(&self, signal: &EngineSignal) -> Result<()> {
        let rate = f64::from(self.per_second);
        let now = signal.at;
        let refill = |bucket: &Bucket| -> f64 {
            #[allow(clippy::cast_precision_loss)] // milliseconds apart
            let elapsed = (now - bucket.at).max(0) as f64;
            (bucket.tokens + elapsed * rate / 1000.0).min(rate)
        };
        let mut buckets = self.buckets.lock().unwrap_or_else(PoisonError::into_inner);
        if buckets.len() >= LIMITER_ENTRIES {
            buckets.retain(|_, bucket| refill(bucket) < rate);
        }
        let key = BucketKey {
            subject: signal.from.clone(),
            namespace: signal.namespace.clone(),
            schema: signal.schema.clone(),
            id: signal.instance_id.clone(),
        };
        let bucket = buckets.entry(key).or_insert(Bucket {
            tokens: rate,
            at: now,
        });
        let tokens = refill(bucket);
        if tokens < 1.0 {
            bucket.tokens = tokens;
            bucket.at = now.max(bucket.at);
            return Err(EngineError::new(
                EngineErrorCode::RateLimited,
                format!(
                    "{} sent more than {} signals a second on {} {}; try again in a moment",
                    signal.from, self.per_second, signal.schema, signal.instance_id
                ),
            )
            .into());
        }
        bucket.tokens = tokens - 1.0;
        bucket.at = now.max(bucket.at);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn signal(from: &str, id: &str, at: i64) -> EngineSignal {
        EngineSignal {
            namespace: "default".into(),
            schema: "Ticket".into(),
            instance_id: id.into(),
            behavior: "test.Chat".into(),
            name: "typing".into(),
            data: json!({}),
            from: from.into(),
            at,
        }
    }

    #[test]
    fn a_bucket_holds_its_rate_and_refills_with_the_clock() {
        let limiter = RateLimiter::new(2);
        assert!(limiter.take(&signal("alice", "t1", 0)).is_ok());
        assert!(limiter.take(&signal("alice", "t1", 0)).is_ok());
        let refused = limiter.take(&signal("alice", "t1", 0)).unwrap_err();
        assert_eq!(refused.code(), Some(EngineErrorCode::RateLimited));
        // Another subject, and another instance, each have their own.
        assert!(limiter.take(&signal("bob", "t1", 0)).is_ok());
        assert!(limiter.take(&signal("alice", "t2", 0)).is_ok());
        // Half a second is one token at two a second.
        assert!(limiter.take(&signal("alice", "t1", 500)).is_ok());
        assert!(limiter.take(&signal("alice", "t1", 500)).is_err());
    }

    #[test]
    fn a_queue_keeps_what_its_filter_keeps_and_drops_its_oldest_when_full() {
        let queue = SignalQueue {
            filter: SignalFilter {
                namespace: "default".into(),
                instance_id: Some("t1".into()),
                ..SignalFilter::default()
            },
            held: Mutex::new(VecDeque::new()),
        };
        assert!(!queue.push(&Arc::new(signal("alice", "t2", 0))));
        for at in 0..300 {
            assert!(queue.push(&Arc::new(signal("alice", "t1", at))));
        }
        let held = queue.take();
        assert_eq!(held.len(), SIGNAL_BUFFER);
        assert_eq!(held[0].at, 300 - SIGNAL_BUFFER as i64);
        assert!(queue.take().is_empty());
    }

    #[test]
    fn options_read_from_json() {
        assert_eq!(
            SignalOptions::from_json(&json!({"perSecond": 5})).unwrap(),
            SignalOptions {
                per_second: Some(5)
            }
        );
        assert!(SignalOptions::from_json(&json!({"perSecond": 0})).is_err());
        assert!(SignalOptions::from_json(&json!({"rate": 5})).is_err());
    }
}
