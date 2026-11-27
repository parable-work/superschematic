//! The claim loop of a stack's Rust worker (D53, D65; section 8.8 of
//! `docs/stack-model.md`), Go's `worker.Run` ported: the process that
//! claims the messages of a queue of a database and handles each with an
//! API's `Deps`. The worker's generated entrypoint builds a
//! [`PostgresQueue`](crate::queue::PostgresQueue) from the descriptor the
//! DB's Rust types carry, and calls [`run_worker`] with the method of the
//! API's `Workers` as the handler.
//!
//! [`run_worker`] claims up to its concurrency of messages at a time, a
//! claim for the slots that are free, and runs each message's handler in a
//! task of its own, extending the message's claim every third of the lease
//! while the handler runs, so a handler longer than the lease keeps its
//! message. A handler that returns `Ok` completes its message; one that
//! returns an error, or panics, fails it, which the queue retries after its
//! backoff or marks dead, the error's chain of causes, up to 4000
//! characters, recorded as the cause. Each outcome is logged. After a claim
//! that found fewer messages than it had slots for, the loop waits a second
//! (the idle wait) before claiming again, and after one that failed,
//! longer each time, doubling up to thirty seconds. Each claim, extension,
//! completion, failure and release has five seconds to answer.
//!
//! When its [`Shutdown`] is cancelled (SIGTERM), it claims nothing more and
//! gives the running handlers the grace to finish. Then it drops the
//! handlers that have not, which stops each at the point it waits at, and
//! releases their messages, which are due again at once for another worker,
//! and returns.
//!
//! Beside the claims, it prunes the queue (D68): when it starts and every
//! five minutes after, it deletes the done messages older than the queue's
//! retention, [`PRUNE_BATCH`] at a time until a batch is short, and logs how
//! many. Every instance of every worker of the queue prunes, which is safe:
//! each batch skips the rows another holds, and a row deleted once is gone.
//! It never deletes a dead message.
//!
//! In drain mode (D145), which a platform sets in [`MODE_VARIABLE`] and
//! [`drain_from_env`] reads, the worker runs as a job that empties its
//! queue and exits: [`run_worker`] takes one of [`Drain::slots`] locks
//! named for its queue as it starts ([`WorkerQueue::slot`]), returning at
//! once when none is free, claims as any worker does, and returns `Ok` once
//! no message was ready or held for [`Drain::idle`] and none falls due
//! within [`Drain::horizon`] ([`WorkerQueue::next_due`]), which it waits
//! for. Once every claim has failed for the idle time it returns an error,
//! so the process exits with one and gives its slot back. A behavior
//! worker's queues run so too, with one slot for them all, idle together
//! or not at all ([`Group`]), as Go's `worker.RunAll` runs them.
//!
//! The loop knows no database: it claims through a [`WorkerQueue`], which
//! [`PostgresQueue`](crate::queue::PostgresQueue) implements, so its tests
//! run over a queue in memory. A behavior worker's queue (D103) is a
//! [`SelfCompleting`] one: its handler takes each message's id and claim
//! token ([`Claimed`]) and completes the message in the transaction that
//! runs it, so the loop's completion does nothing.
//!
//! It logs through `tracing`, in a span carrying `queue`, with the fields
//! and messages of Go's and TypeScript's loops: `message`, `attempt`,
//! `took` (seconds) and `error` on each outcome, `retryIn` (seconds) on a
//! failed claim, `pruned` on a prune, and `running`, `grace`, `released` and
//! `unfinished` as it stops. The `logging` module writes them as JSON
//! lines. With the `otel` feature and export on, each message's handling is
//! a consumer span, `process <queue>`, the root of a trace of its own (D67).
//!
//! On a SQLite file a worker is instead a function its host calls (D116),
//! [`SqliteWorker::drain`], with the `sqlite` feature: the `drain` module
//! has it, the [`Wake`] a commit of the host's own process wakes a sleeping
//! worker with, and [`run_drain`], the loop over it on tokio.

#[cfg(feature = "sqlite")]
pub mod drain;

use std::any::Any;
use std::collections::HashMap;
use std::convert::Infallible;
use std::fmt;
use std::future::Future;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::pin::Pin;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::task::{Context, Poll};
use std::time::Duration;

use async_trait::async_trait;
use serde::de::DeserializeOwned;
use tokio::sync::Notify;
use tokio::task::AbortHandle;
use tokio::time::{Instant, MissedTickBehavior};
use tokio_util::task::TaskTracker;
use tracing::Instrument as _;

#[cfg(feature = "postgres")]
use crate::queue::PostgresQueue;
#[cfg(feature = "sqlite")]
use crate::queue::SqliteQueue;
use crate::queue::{Claim, QueueError, Slot};
use crate::stackconfig::{quote, StackConfigError};
use crate::{BoxError, Shutdown};

#[cfg(feature = "sqlite")]
pub use drain::{run_drain, DrainOptions, Drained, SqliteWorker, Unit, Wake, DEFAULT_DRAIN_LIMIT};

/// The environment variable a platform sets to the number of messages a
/// worker handles at a time in its environment: the worker's settings or
/// its `@worker` concurrency.
pub const CONCURRENCY_VARIABLE: &str = "WORKER_CONCURRENCY";

/// How long a stopping worker lets its handlers finish: under the ten
/// seconds Cloud Run gives a container after SIGTERM.
pub const DEFAULT_GRACE: Duration = Duration::from_secs(8);

/// How long a worker waits to claim again after a claim found fewer
/// messages than it had slots for.
pub const DEFAULT_IDLE: Duration = Duration::from_secs(1);

/// How often a worker prunes its queue's done messages, after the prune it
/// starts with.
pub const DEFAULT_PRUNE_EVERY: Duration = Duration::from_secs(5 * 60);

/// How many done messages one prune statement deletes at most, so no
/// statement holds its locks for long; a prune runs another while one
/// deletes a whole batch.
pub const PRUNE_BATCH: usize = 1000;

/// The longest wait after a claim that failed, which doubles from the idle
/// wait.
const MAX_BACKOFF: Duration = Duration::from_secs(30);

/// How long each claim, extension, completion, failure, release and prune
/// batch may take, so a stopping worker still records them.
const OP_TIMEOUT: Duration = Duration::from_secs(5);

/// The longest cause a failed message records, in characters.
const MAX_CAUSE: usize = 4000;

/// The environment variables a platform sets on a worker in drain mode
/// (D145; `ir.WorkerModeVariable` and its neighbours): [`MODE_VARIABLE`] is
/// [`MODE_DRAIN`], [`IDLE_SECONDS_VARIABLE`] how long the worker waits with
/// no message ready or held before it exits, and [`SLOTS_VARIABLE`] how many
/// of it may run at once. A worker with no [`MODE_VARIABLE`], or
/// [`MODE_POOL`], polls until it is stopped.
pub const MODE_VARIABLE: &str = "WORKER_MODE";
pub const IDLE_SECONDS_VARIABLE: &str = "WORKER_IDLE_SECONDS";
pub const SLOTS_VARIABLE: &str = "WORKER_SLOTS";
pub const MODE_POOL: &str = "pool";
pub const MODE_DRAIN: &str = "drain";

/// How long a worker in drain mode waits idle before it exits, unless
/// [`IDLE_SECONDS_VARIABLE`] says.
pub const DEFAULT_DRAIN_IDLE: Duration = Duration::from_secs(30);

/// How far ahead a worker in drain mode that found its queue idle looks for
/// a message that is not due yet, a delayed one or a retry after its
/// backoff (D145): it waits for one due within the horizon instead of
/// exiting, and leaves one due later to the platform's next start, a wake
/// or its tick. Five minutes is gcp's default tick, as Go's
/// `worker.DrainHorizon` and TypeScript's `DRAIN_HORIZON_MS` are.
pub const DRAIN_HORIZON: Duration = Duration::from_secs(5 * 60);

/// How a worker in drain mode runs (D145), as the module says. A zero in
/// any of them is its default.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Drain {
    /// How long the worker waits with no message ready or held before it
    /// stops; [`DEFAULT_DRAIN_IDLE`] by default.
    pub idle: Duration,
    /// How many workers may drain the queue at once; one by default.
    pub slots: usize,
    /// How far ahead the worker looks for a message that is not due yet
    /// before it stops; [`DRAIN_HORIZON`] by default.
    pub horizon: Duration,
}

impl Default for Drain {
    fn default() -> Self {
        Self {
            idle: DEFAULT_DRAIN_IDLE,
            slots: 1,
            horizon: DRAIN_HORIZON,
        }
    }
}

impl Drain {
    /// The drain with each zero replaced by its default.
    fn or_defaults(self) -> Self {
        let defaults = Self::default();
        Self {
            idle: if self.idle.is_zero() {
                defaults.idle
            } else {
                self.idle
            },
            slots: self.slots.max(1),
            horizon: if self.horizon.is_zero() {
                defaults.horizon
            } else {
                self.horizon
            },
        }
    }
}

/// The drain the environment gives a worker, or `None` for one that runs
/// as a pool's instance: [`MODE_VARIABLE`] unset, empty or [`MODE_POOL`].
/// Refuses a mode it does not know, and an idle time or a slot count that
/// is no whole number, one or more, as Go's `worker.DrainFromEnv` does.
pub fn drain_from_env() -> Result<Option<Drain>, StackConfigError> {
    let mut vars = HashMap::new();
    for name in [MODE_VARIABLE, IDLE_SECONDS_VARIABLE, SLOTS_VARIABLE] {
        if let Some(value) = std::env::var_os(name) {
            let Ok(value) = value.into_string() else {
                return Err(StackConfigError::one(format!("{name} is not UTF-8")));
            };
            vars.insert(name.to_string(), value);
        }
    }
    drain_from(&vars)
}

/// [`drain_from_env`] over `vars` in place of the environment.
pub fn drain_from(vars: &HashMap<String, String>) -> Result<Option<Drain>, StackConfigError> {
    let get = |name: &str| vars.get(name).map_or("", String::as_str);
    match get(MODE_VARIABLE) {
        "" | MODE_POOL => return Ok(None),
        MODE_DRAIN => {}
        mode => {
            return Err(StackConfigError::one(format!(
                "{MODE_VARIABLE} is {}; it is {} or {}",
                quote(mode),
                quote(MODE_POOL),
                quote(MODE_DRAIN)
            )))
        }
    }
    let whole = |name: &str| -> Result<Option<u64>, StackConfigError> {
        let value = get(name);
        if value.is_empty() {
            return Ok(None);
        }
        match value.parse::<u64>() {
            Ok(n) if n >= 1 => Ok(Some(n)),
            _ => Err(StackConfigError::one(format!(
                "{name} is {}; it is a whole number, one or more",
                quote(value)
            ))),
        }
    };
    let mut drain = Drain::default();
    if let Some(seconds) = whole(IDLE_SECONDS_VARIABLE)? {
        drain.idle = Duration::from_secs(seconds);
    }
    if let Some(slots) = whole(SLOTS_VARIABLE)? {
        drain.slots = usize::try_from(slots).unwrap_or(usize::MAX);
    }
    Ok(Some(drain))
}

/// How [`run_worker`] runs. A zero in any of them is its default.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Options {
    /// How many messages are handled at a time; one by default.
    pub concurrency: usize,
    /// How long a stopping worker lets its running handlers finish before
    /// it drops them and releases their messages; [`DEFAULT_GRACE`] by
    /// default.
    pub grace: Duration,
    /// How long to wait after a claim that found fewer messages than it had
    /// slots for; [`DEFAULT_IDLE`] by default.
    pub idle: Duration,
    /// How often to prune the queue after the prune the worker starts with;
    /// [`DEFAULT_PRUNE_EVERY`] by default.
    pub prune_every: Duration,
    /// Runs the worker in drain mode (D145), as the module says; `None`
    /// polls until the worker is stopped.
    pub drain: Option<Drain>,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            concurrency: 1,
            grace: DEFAULT_GRACE,
            idle: DEFAULT_IDLE,
            prune_every: DEFAULT_PRUNE_EVERY,
            drain: None,
        }
    }
}

impl Options {
    /// The options with each zero replaced by its default.
    fn or_defaults(self) -> Self {
        let defaults = Self::default();
        Self {
            concurrency: self.concurrency.max(1),
            grace: if self.grace.is_zero() {
                defaults.grace
            } else {
                self.grace
            },
            idle: if self.idle.is_zero() {
                defaults.idle
            } else {
                self.idle
            },
            prune_every: if self.prune_every.is_zero() {
                defaults.prune_every
            } else {
                self.prune_every
            },
            drain: self.drain.map(Drain::or_defaults),
        }
    }
}

/// The concurrency the environment gives a worker in
/// [`CONCURRENCY_VARIABLE`], or `default` when it sets none. Refuses a value
/// that is not a whole number, one or more, as Go's `worker.Concurrency`
/// does.
pub fn concurrency_from_env(default: usize) -> Result<usize, StackConfigError> {
    match std::env::var_os(CONCURRENCY_VARIABLE) {
        None => Ok(default),
        Some(value) => match value.into_string() {
            Ok(value) => read_concurrency(default, &value),
            Err(_) => Err(StackConfigError::one(format!(
                "{CONCURRENCY_VARIABLE} is not UTF-8; it is a whole number, one or more"
            ))),
        },
    }
}

/// [`concurrency_from_env`] over `vars` in place of the environment.
pub fn concurrency_from(
    default: usize,
    vars: &HashMap<String, String>,
) -> Result<usize, StackConfigError> {
    read_concurrency(
        default,
        vars.get(CONCURRENCY_VARIABLE).map_or("", String::as_str),
    )
}

fn read_concurrency(default: usize, value: &str) -> Result<usize, StackConfigError> {
    if value.is_empty() {
        return Ok(default);
    }
    match value.parse::<usize>() {
        Ok(n) if n >= 1 => Ok(n),
        _ => Err(StackConfigError::one(format!(
            "{CONCURRENCY_VARIABLE} is {}; it is a whole number, one or more",
            quote(value)
        ))),
    }
}

/// The queue a worker claims from: what [`run_worker`] asks of it, which
/// [`PostgresQueue`] does by its descriptor's statements.
#[async_trait]
pub trait WorkerQueue: Send + Sync + 'static {
    /// The queue's messages.
    type Message: Send + 'static;

    /// Names the queue in the log.
    fn name(&self) -> &str;

    /// How long a claim holds a message; [`run_worker`] extends it every
    /// third of it while the message's handler runs.
    fn lease(&self) -> Duration;

    /// Claims up to `n` due messages for `lease`.
    async fn claim(
        &self,
        n: usize,
        lease: Duration,
    ) -> Result<Vec<Claim<Self::Message>>, QueueError>;

    /// Holds a claimed message for `lease` from now.
    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError>;

    /// Marks a claimed message done.
    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError>;

    /// Records `cause` on a claimed message, which the queue retries after
    /// its backoff or marks dead, and returns whether it is dead.
    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError>;

    /// Gives a claimed message back, due at once.
    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError>;

    /// Deletes up to `limit` messages done for the queue's retention, and
    /// returns how many it deleted. A queue that does not implement it
    /// prunes nothing.
    async fn prune(&self, _limit: usize) -> Result<u64, QueueError> {
        Ok(0)
    }

    /// Takes one of `slots` locks named for the queue, which a worker in
    /// drain mode holds while it runs (D145): `None` when every one is
    /// taken, and otherwise the slot, which gives the lock back as it
    /// drops. A queue that does not implement it takes none, as on a
    /// database with no such lock: every worker started then drains.
    async fn slot(&self, _slots: usize) -> Result<Option<Slot>, QueueError> {
        Ok(Some(Slot::none()))
    }

    /// How long until the first ready message that is not due yet falls
    /// due, or `None` when none waits (D145): a worker in drain mode that
    /// found the queue idle waits for one due within its horizon instead of
    /// stopping. A queue that does not implement it reads none, and the
    /// worker stops once idle.
    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        Ok(None)
    }
}

#[cfg(feature = "postgres")]
#[async_trait]
impl<M: DeserializeOwned + Send + 'static> WorkerQueue for PostgresQueue<M> {
    type Message = M;

    fn name(&self) -> &str {
        PostgresQueue::name(self)
    }

    fn lease(&self) -> Duration {
        PostgresQueue::lease(self)
    }

    async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<M>>, QueueError> {
        PostgresQueue::claim(self, n, lease).await
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        PostgresQueue::extend(self, id, token, lease).await
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        PostgresQueue::complete(self, id, token).await
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        PostgresQueue::fail(self, id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        PostgresQueue::release(self, id, token).await
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        PostgresQueue::prune(self, limit).await
    }

    async fn slot(&self, slots: usize) -> Result<Option<Slot>, QueueError> {
        PostgresQueue::slot(self, slots).await
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        PostgresQueue::next_due(self).await
    }
}

#[cfg(feature = "sqlite")]
#[async_trait]
impl<M: DeserializeOwned + Send + 'static> WorkerQueue for SqliteQueue<M> {
    type Message = M;

    fn name(&self) -> &str {
        SqliteQueue::name(self)
    }

    fn lease(&self) -> Duration {
        SqliteQueue::lease(self)
    }

    async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<M>>, QueueError> {
        SqliteQueue::claim(self, n, lease).await
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        SqliteQueue::extend(self, id, token, lease).await
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        SqliteQueue::complete(self, id, token).await
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        SqliteQueue::fail(self, id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        SqliteQueue::release(self, id, token).await
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        SqliteQueue::prune(self, limit).await
    }
}

/// A claimed message as a handler that completes it itself takes it (D103):
/// the message's id and the claim's token beside the message, so the
/// handler can complete the message in the transaction that does its work.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Claimed<M> {
    pub id: String,
    pub token: String,
    pub message: M,
}

/// A queue whose handler completes each message itself, in the transaction
/// that does the message's work (D103): a behavior worker's deadline queue,
/// whose handler runs a deadline through the behaviors' core and spends its
/// claim in the same commit, so a crash between the commit and a completion
/// cannot run it twice. Each message is a [`Claimed`] of the queue's, and a
/// completion of the loop's does nothing; every other call is the queue's,
/// so a handler's error still fails its message, which is due again after
/// the queue's backoff, and a stopping worker still releases the messages
/// its handlers did not finish.
#[derive(Clone, Debug)]
pub struct SelfCompleting<Q> {
    queue: Q,
}

impl<Q> SelfCompleting<Q> {
    /// The queue, its messages completed by their handler.
    pub fn new(queue: Q) -> Self {
        Self { queue }
    }

    /// The queue it claims from.
    pub fn queue(&self) -> &Q {
        &self.queue
    }
}

#[async_trait]
impl<Q: WorkerQueue> WorkerQueue for SelfCompleting<Q> {
    type Message = Claimed<Q::Message>;

    fn name(&self) -> &str {
        self.queue.name()
    }

    fn lease(&self) -> Duration {
        self.queue.lease()
    }

    async fn claim(
        &self,
        n: usize,
        lease: Duration,
    ) -> Result<Vec<Claim<Self::Message>>, QueueError> {
        let claims = self.queue.claim(n, lease).await?;
        Ok(claims
            .into_iter()
            .map(|claim| Claim {
                message: Claimed {
                    id: claim.id.clone(),
                    token: claim.token.clone(),
                    message: claim.message,
                },
                id: claim.id,
                token: claim.token,
                attempt: claim.attempt,
            })
            .collect())
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        self.queue.extend(id, token, lease).await
    }

    /// Does nothing: the handler completed the message in its own commit.
    async fn complete(&self, _id: &str, _token: &str) -> Result<(), QueueError> {
        Ok(())
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        self.queue.fail(id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.queue.release(id, token).await
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        self.queue.prune(limit).await
    }

    async fn slot(&self, slots: usize) -> Result<Option<Slot>, QueueError> {
        self.queue.slot(slots).await
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        self.queue.next_due().await
    }
}

/// Claims and handles `queue`'s messages with `handler` until `shutdown` is
/// cancelled, then stops as the module says: it claims no more, lets the
/// running handlers finish within the grace, and drops those that do not,
/// releasing their messages. It returns `Ok` once it stopped, and an error
/// only for a queue it cannot run, whose lease is zero, or in drain mode
/// for a slot it could not take or once every claim has failed for the
/// drain's idle time.
///
/// In drain mode (`options.drain`, D145) it first takes a slot of the
/// queue, returning `Ok` at once when every one is taken, and returns `Ok`
/// once no message was ready or held for the drain's idle time and none
/// falls due within its horizon, giving the slot back as it returns.
///
/// The handler is any function of a message to a future that resolves to
/// `Ok` or a [`BoxError`]: an `async` closure over the API's `Workers`.
pub async fn run_worker<Q, H, F>(
    queue: Q,
    handler: H,
    options: Options,
    shutdown: Shutdown,
) -> Result<(), QueueError>
where
    Q: WorkerQueue,
    H: Fn(Q::Message) -> F + Send + Sync + 'static,
    F: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    check_lease(&queue)?;
    let options = options.or_defaults();
    let span = tracing::error_span!("worker", queue = queue.name());
    async move {
        let Some(_slot) = slot_of(&queue, options.drain, &shutdown).await? else {
            return Ok(());
        };
        run_loop(
            queue,
            handler,
            options,
            shutdown.child(),
            Arc::new(Quiet::new()),
        )
        .await
    }
    .instrument(span)
    .await
}

/// The claim loops of one behavior worker (D103), one per behavior queue of
/// its table, run together as Go's `worker.RunAll` runs them: each is
/// [`run_worker`]'s over its queue, with the same options, and in drain
/// mode (D145) the worker takes one slot for them all
/// ([`Group::take_slot`]), and they stop together, once none was busy for
/// the drain's idle time: a claim that found a message, a handler that
/// started and one that returned, of any of them, make them all busy. A
/// loop that stops with an error stops the others at once.
///
/// ```ignore
/// let group = worker::Group::new(&shutdown);
/// if !group.take_slot(&deadlines, options).await? {
///     return Ok(());
/// }
/// let ran = tokio::join!(
///     group.run(deadlines, handle_deadline, options),
///     group.run(changes, handle_change, options),
/// );
/// ```
pub struct Group {
    stop: Shutdown,
    quiet: Arc<Quiet>,
    slot: Mutex<Option<Slot>>,
}

impl Group {
    /// The loops of a worker that stop when `shutdown` is cancelled.
    pub fn new(shutdown: &Shutdown) -> Self {
        Self {
            stop: shutdown.child(),
            quiet: Arc::new(Quiet::new()),
            slot: Mutex::new(None),
        }
    }

    /// In drain mode, takes the worker's one slot through `queue`, its first
    /// queue, which the group holds until it is dropped, and returns false
    /// when every one is taken: the worker then runs none of its loops, as
    /// one that drains them. Out of drain mode it takes none and returns
    /// true.
    pub async fn take_slot<Q: WorkerQueue>(
        &self,
        queue: &Q,
        options: Options,
    ) -> Result<bool, QueueError> {
        let drain = options.or_defaults().drain;
        let span = tracing::error_span!("worker", queue = queue.name());
        let taken = slot_of(queue, drain, &self.stop).instrument(span).await?;
        let Some(slot) = taken else {
            return Ok(false);
        };
        *self.slot.lock().unwrap_or_else(PoisonError::into_inner) = Some(slot);
        Ok(true)
    }

    /// Runs one of the worker's queues as [`run_worker`] does, but for the
    /// slot, which [`Group::take_slot`] took for them all, and the idle
    /// time, which its loops share.
    pub async fn run<Q, H, F>(
        &self,
        queue: Q,
        handler: H,
        options: Options,
    ) -> Result<(), QueueError>
    where
        Q: WorkerQueue,
        H: Fn(Q::Message) -> F + Send + Sync + 'static,
        F: Future<Output = Result<(), BoxError>> + Send + 'static,
    {
        let ran = match check_lease(&queue) {
            Ok(()) => {
                let span = tracing::error_span!("worker", queue = queue.name());
                run_loop(
                    queue,
                    handler,
                    options.or_defaults(),
                    self.stop.child(),
                    Arc::clone(&self.quiet),
                )
                .instrument(span)
                .await
            }
            Err(err) => Err(err),
        };
        if ran.is_err() {
            self.stop.cancel();
        }
        ran
    }
}

impl fmt::Debug for Group {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Group").finish_non_exhaustive()
    }
}

/// Refuses a queue whose lease is zero.
fn check_lease<Q: WorkerQueue>(queue: &Q) -> Result<(), QueueError> {
    if queue.lease().is_zero() {
        return Err(QueueError::Definition(format!(
            "worker: queue {} has a lease of 0 s; it is positive",
            queue.name()
        )));
    }
    Ok(())
}

/// The slot a worker in drain mode takes of `queue` as it starts (D145):
/// `Some` of the slot, which holds nothing out of drain mode, or `None`
/// when every one is taken or `shutdown` is cancelled first.
async fn slot_of<Q: WorkerQueue>(
    queue: &Q,
    drain: Option<Drain>,
    shutdown: &Shutdown,
) -> Result<Option<Slot>, QueueError> {
    let Some(drain) = drain else {
        return Ok(Some(Slot::none()));
    };
    let taken = tokio::select! {
        () = shutdown.cancelled() => return Ok(None),
        taken = queue.slot(drain.slots) => taken,
    };
    match taken {
        Ok(Some(slot)) => Ok(Some(slot)),
        Ok(None) => {
            tracing::info!(
                slots = drain.slots,
                "every slot of the queue is taken: the workers that hold them drain it"
            );
            Ok(None)
        }
        Err(err) => Err(QueueError::Failed(format!(
            "worker: take a slot of queue {}: {err}",
            queue.name()
        ))),
    }
}

/// Runs the loop over `queue` until `stop` is cancelled or, in drain mode,
/// it has drained, then stops within the grace.
async fn run_loop<Q, H, F>(
    queue: Q,
    handler: H,
    options: Options,
    stop: Shutdown,
    quiet: Arc<Quiet>,
) -> Result<(), QueueError>
where
    Q: WorkerQueue,
    H: Fn(Q::Message) -> F + Send + Sync + 'static,
    F: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    let lease = queue.lease();
    let worker = Arc::new(Loop {
        queue,
        handler,
        options,
        lease,
        running: Mutex::new(HashMap::new()),
        freed: Notify::new(),
        tracker: TaskTracker::new(),
        span: tracing::Span::current(),
        quiet,
    });
    // The prune stops with the claims: when the worker is stopped, or once
    // a worker in drain mode has drained.
    let claims = async {
        let claimed = worker.claim_loop(&stop).await;
        stop.cancel();
        claimed
    };
    let (claimed, ()) = tokio::join!(claims, worker.prune_loop(&stop));
    worker.stop().await;
    claimed
}

/// When the claim loops of one worker in drain mode were last busy, and how
/// many of their handlers run (D145): a claim that found a message, a
/// handler that started and one that returned each make them busy, so the
/// queues of a behavior worker are idle together or not at all.
struct Quiet {
    last: Mutex<Instant>,
    running: AtomicUsize,
}

impl Quiet {
    fn new() -> Self {
        Self {
            last: Mutex::new(Instant::now()),
            running: AtomicUsize::new(0),
        }
    }

    /// Marks the worker busy now.
    fn busy(&self) {
        *self.last.lock().unwrap_or_else(PoisonError::into_inner) = Instant::now();
    }

    /// Whether no handler runs and none was busy for `idle`.
    fn over(&self, idle: Duration) -> bool {
        self.running.load(Ordering::SeqCst) == 0
            && self
                .last
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .elapsed()
                >= idle
    }
}

/// One run of [`run_worker`].
struct Loop<Q, H> {
    queue: Q,
    handler: H,
    options: Options,
    lease: Duration,
    /// The messages whose handlers run, by the token of their claim, which
    /// no other claim shares.
    running: Mutex<HashMap<String, Running>>,
    /// Notified once per handler that finishes, so the claim loop wakes to
    /// claim for the slot it frees.
    freed: Notify,
    /// The task of each message, which the stop waits for.
    tracker: TaskTracker,
    span: tracing::Span,
    /// The idle clock the worker's loops share in drain mode.
    quiet: Arc<Quiet>,
}

/// A message whose handler runs.
struct Running {
    id: String,
    started: Instant,
    /// Set when the grace is over and the message is released, so its
    /// handler's outcome, should it come, changes nothing.
    released: bool,
    abort: Option<AbortHandle>,
}

impl<Q, H, F> Loop<Q, H>
where
    Q: WorkerQueue,
    H: Fn(Q::Message) -> F + Send + Sync + 'static,
    F: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    fn running(&self) -> MutexGuard<'_, HashMap<String, Running>> {
        // A panic never holds the lock: each holder only reads and writes
        // the map.
        self.running.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn count(&self) -> usize {
        self.running().len()
    }

    /// Claims messages for the free slots until `stop` is cancelled, or, in
    /// drain mode, until the worker has drained. It returns an error only
    /// in drain mode, once every claim has failed for the drain's idle
    /// time.
    async fn claim_loop(self: &Arc<Self>, stop: &Shutdown) -> Result<(), QueueError> {
        let mut wait = self.options.idle;
        // When the claims began to fail: a worker in drain mode whose every
        // claim fails for its idle time stops with an error.
        let mut failing_since: Option<Instant> = None;
        while !stop.is_cancelled() {
            let free = self.options.concurrency.saturating_sub(self.count());
            if free == 0 {
                tokio::select! {
                    () = stop.cancelled() => return Ok(()),
                    () = self.freed.notified() => continue,
                }
            }
            let claimed = tokio::select! {
                () = stop.cancelled() => return Ok(()),
                claimed = timed(self.queue.claim(free, self.lease)) => claimed,
            };
            let claims = match claimed {
                Ok(claims) => claims,
                Err(err) => {
                    let since = *failing_since.get_or_insert_with(Instant::now);
                    if !self.claim_failed(&err, since, wait, stop).await? {
                        return Ok(());
                    }
                    wait = (wait * 2).min(MAX_BACKOFF);
                    continue;
                }
            };
            wait = self.options.idle;
            failing_since = None;
            let found = claims.len();
            for claim in claims {
                self.start(claim);
            }
            if self.drained(found) {
                if self.wait_for_due(stop).await {
                    continue;
                }
                return Ok(());
            }
            if found < free && !sleep(stop, self.options.idle).await {
                return Ok(());
            }
        }
        Ok(())
    }

    /// After a claim that failed, the claims having failed since `since`:
    /// the error that stops a worker in drain mode whose every claim failed
    /// for its idle time, or the wait before the next claim, and whether
    /// the worker is still live after it.
    async fn claim_failed(
        &self,
        err: &QueueError,
        since: Instant,
        wait: Duration,
        stop: &Shutdown,
    ) -> Result<bool, QueueError> {
        if let Some(drain) = self.options.drain {
            if since.elapsed() >= drain.idle {
                tracing::error!(
                    idle = drain.idle.as_secs_f64(),
                    error = %err,
                    "every claim failed for the idle time: the worker stops with an error and gives its slot back"
                );
                return Err(QueueError::Failed(format!(
                    "worker: every claim of queue {} failed for {}s, so the worker in drain mode stops: {err}",
                    self.queue.name(),
                    drain.idle.as_secs_f64()
                )));
            }
        }
        tracing::error!(retryIn = wait.as_secs_f64(), error = %err, "claim failed");
        Ok(sleep(stop, wait).await)
    }

    /// Once a worker in drain mode has drained: waits for a message that
    /// falls due within the horizon, a delayed one or a retry, and returns
    /// whether to claim again, which takes it; or logs that it drained and
    /// returns false.
    async fn wait_for_due(&self, stop: &Shutdown) -> bool {
        let Some(due) = self.next_due(stop).await else {
            tracing::info!(
                idle = self
                    .options
                    .drain
                    .map_or(0.0, |drain| drain.idle.as_secs_f64()),
                "drained: no message was ready or held"
            );
            return false;
        };
        tracing::info!(
            dueIn = due.as_secs_f64(),
            "idle, and waiting for a message due soon"
        );
        sleep(stop, due.max(self.options.idle)).await
    }

    /// Whether a worker in drain mode is done: its claim found nothing, none
    /// of its handlers runs, and none was busy for the drain's idle time. A
    /// claim that found a message makes the worker busy.
    fn drained(&self, claimed: usize) -> bool {
        let Some(drain) = self.options.drain else {
            return false;
        };
        if claimed > 0 || self.count() > 0 {
            self.quiet.busy();
            return false;
        }
        self.quiet.over(drain.idle)
    }

    /// How long until a message that is not due yet falls due, when that is
    /// within the drain's horizon: a worker in drain mode that found its
    /// queue idle waits for it. A queue that reads none, or whose read
    /// fails, gives `None`, and the worker stops.
    async fn next_due(&self, stop: &Shutdown) -> Option<Duration> {
        let horizon = self.options.drain?.horizon;
        let read = tokio::select! {
            () = stop.cancelled() => return None,
            read = timed(self.queue.next_due()) => read,
        };
        match read {
            Err(err) => {
                tracing::warn!(error = %err, "reading when the next message is due failed: the tick runs it");
                None
            }
            Ok(None) => None,
            Ok(Some(due)) if due > horizon => {
                tracing::info!(
                    dueIn = due.as_secs_f64(),
                    horizon = horizon.as_secs_f64(),
                    "the next message is due past the drain's horizon: a wake or the tick runs it"
                );
                None
            }
            Ok(Some(due)) => Some(due),
        }
    }

    /// Prunes the queue at once, then every `prune_every`, until `stop` is
    /// cancelled.
    async fn prune_loop(&self, stop: &Shutdown) {
        loop {
            self.prune(stop).await;
            if !sleep(stop, self.options.prune_every).await {
                return;
            }
        }
    }

    /// Deletes the queue's done messages past its retention, a batch at a
    /// time until a batch is short, and logs how many it deleted. A failed
    /// batch ends the prune, which the next tries again.
    async fn prune(&self, stop: &Shutdown) {
        let mut pruned: u64 = 0;
        loop {
            let batch = tokio::select! {
                () = stop.cancelled() => break,
                batch = timed(self.queue.prune(PRUNE_BATCH)) => batch,
            };
            match batch {
                Ok(n) => {
                    pruned += n;
                    if n < PRUNE_BATCH as u64 {
                        break;
                    }
                }
                Err(err) => {
                    tracing::warn!(pruned, error = %err, "pruning the done messages failed");
                    break;
                }
            }
        }
        if pruned > 0 {
            tracing::info!(pruned, "done messages pruned");
        }
    }

    /// Runs a message's handler in a task of its own, extending its claim
    /// while it runs.
    fn start(self: &Arc<Self>, claim: Claim<Q::Message>) {
        let Claim {
            id,
            token,
            attempt,
            message,
        } = claim;
        self.running().insert(
            token.clone(),
            Running {
                id: id.clone(),
                started: Instant::now(),
                released: false,
                abort: None,
            },
        );
        self.quiet.running.fetch_add(1, Ordering::SeqCst);
        self.quiet.busy();
        let worker = Arc::clone(self);
        let key = token.clone();
        let task = self.tracker.spawn(
            async move {
                let span = message_span(worker.queue.name(), &id, attempt);
                let outcome = tokio::select! {
                    outcome = worker.handle(message).instrument(span.clone()) => outcome,
                    never = worker.extend(&id, &token, attempt) => match never {},
                };
                if let Err(cause) = &outcome {
                    message_failed(&span, cause);
                }
                worker.finish(&id, &token, attempt, outcome).await;
                // A handler that returns leaves the worker busy until now.
                worker.quiet.busy();
                worker.quiet.running.fetch_sub(1, Ordering::SeqCst);
                worker.freed.notify_one();
            }
            .instrument(self.span.clone()),
        );
        if let Some(running) = self.running().get_mut(&key) {
            running.abort = Some(task.abort_handle());
        }
    }

    /// Runs the handler on `message`: its error, or its panic, is the
    /// message's cause.
    async fn handle(&self, message: Q::Message) -> Result<(), String> {
        let handled = match catch_unwind(AssertUnwindSafe(|| (self.handler)(message))) {
            Ok(future) => CatchUnwind(Box::pin(future)).await,
            Err(panic) => Err(panic),
        };
        match handled {
            Ok(Ok(())) => Ok(()),
            Ok(Err(err)) => Err(crate::error::chain(err.as_ref())),
            Err(panic) => Err(format!("panic: {}", panic_message(panic.as_ref()))),
        }
    }

    /// Extends the claim every third of the lease, until the handler's
    /// outcome drops it.
    async fn extend(&self, id: &str, token: &str, attempt: u32) -> Infallible {
        let period = (self.lease / 3).max(Duration::from_millis(1));
        let mut ticker = tokio::time::interval_at(Instant::now() + period, period);
        ticker.set_missed_tick_behavior(MissedTickBehavior::Delay);
        loop {
            ticker.tick().await;
            if let Err(err) = timed(self.queue.extend(id, token, self.lease)).await {
                tracing::warn!(message = id, attempt, error = %err, "extend the claim failed");
            }
        }
    }

    /// Records a handler's outcome, unless its message was released while
    /// it ran.
    async fn finish(&self, id: &str, token: &str, attempt: u32, outcome: Result<(), String>) {
        let (released, started) = self
            .running()
            .remove(token)
            .map_or((false, None), |running| {
                (running.released, Some(running.started))
            });
        let took = started.map_or(0.0, |started| started.elapsed().as_secs_f64());
        if released {
            tracing::warn!(
                message = id,
                attempt,
                took,
                "message handler returned after its message was released"
            );
            return;
        }
        match outcome {
            Ok(()) => self.complete(id, token, attempt, took).await,
            Err(cause) => self.fail(id, token, attempt, took, &cap(cause)).await,
        }
    }

    /// Completes a message whose handler returned `Ok`.
    async fn complete(&self, id: &str, token: &str, attempt: u32, took: f64) {
        match timed(self.queue.complete(id, token)).await {
            Ok(()) => tracing::info!(message = id, attempt, took, "message handled"),
            Err(err) => tracing::error!(
                message = id,
                attempt,
                took,
                error = %err,
                "message handled, but completing it failed"
            ),
        }
    }

    /// Fails a message whose handler returned an error or panicked.
    async fn fail(&self, id: &str, token: &str, attempt: u32, took: f64, cause: &str) {
        match timed(self.queue.fail(id, token, cause)).await {
            Ok(true) => tracing::error!(
                message = id,
                attempt,
                took,
                error = cause,
                "message failed and is dead: its retries are spent"
            ),
            Ok(false) => tracing::warn!(
                message = id,
                attempt,
                took,
                error = cause,
                "message failed and will be retried"
            ),
            Err(err) => tracing::error!(
                message = id,
                attempt,
                took,
                error = cause,
                recordError = %err,
                "message failed, and recording the failure failed"
            ),
        }
    }

    /// Waits up to the grace for the running handlers, then drops the rest
    /// and releases their messages.
    async fn stop(&self) {
        self.tracker.close();
        let running = self.count();
        if running > 0 {
            tracing::info!(
                running,
                grace = self.options.grace.as_secs_f64(),
                "stopping: letting the running handlers finish"
            );
        }
        if tokio::time::timeout(self.options.grace, self.tracker.wait())
            .await
            .is_ok()
        {
            tracing::info!("worker stopped");
            return;
        }
        self.release_unfinished().await;
    }

    /// Drops the handlers still running once the grace is over, and
    /// releases their messages.
    async fn release_unfinished(&self) {
        let unfinished: Vec<(String, String)> = {
            let mut running = self.running();
            running
                .iter_mut()
                .map(|(token, running)| {
                    running.released = true;
                    if let Some(abort) = &running.abort {
                        abort.abort();
                    }
                    (running.id.clone(), token.clone())
                })
                .collect()
        };
        let mut released = 0;
        for (id, token) in &unfinished {
            match timed(self.queue.release(id, token)).await {
                Ok(()) => released += 1,
                Err(err) => tracing::error!(
                    message = id.as_str(),
                    error = %err,
                    "release failed: the message is due again when its claim expires"
                ),
            }
        }
        tracing::info!(
            released,
            unfinished = unfinished.len(),
            "worker stopped: the grace is over, and the unfinished messages are released"
        );
    }
}

/// The span of a message's handling, which trace export makes a consumer
/// span (D67): none without the `otel` feature, or with export off.
#[cfg(feature = "otel")]
fn message_span(queue: &str, id: &str, attempt: u32) -> tracing::Span {
    crate::telemetry::message_span(queue, id, attempt)
}

#[cfg(not(feature = "otel"))]
fn message_span(_queue: &str, _id: &str, _attempt: u32) -> tracing::Span {
    tracing::Span::none()
}

/// Records a message's failure on its span.
#[cfg(feature = "otel")]
fn message_failed(span: &tracing::Span, cause: &str) {
    crate::telemetry::message_failed(span, cause);
}

#[cfg(not(feature = "otel"))]
fn message_failed(_span: &tracing::Span, _cause: &str) {}

/// A queue operation, given [`OP_TIMEOUT`] to answer.
async fn timed<T>(operation: impl Future<Output = Result<T, QueueError>>) -> Result<T, QueueError> {
    tokio::time::timeout(OP_TIMEOUT, operation)
        .await
        .unwrap_or_else(|_| {
            Err(QueueError::Failed(format!(
                "no answer within {} s",
                OP_TIMEOUT.as_secs()
            )))
        })
}

/// Waits `wait`, or until `shutdown` is cancelled, and returns whether it
/// is still live.
async fn sleep(shutdown: &Shutdown, wait: Duration) -> bool {
    tokio::select! {
        () = shutdown.cancelled() => false,
        () = tokio::time::sleep(wait) => true,
    }
}

/// A cause of at most [`MAX_CAUSE`] characters.
fn cap(cause: String) -> String {
    match cause.char_indices().nth(MAX_CAUSE) {
        Some((end, _)) => cause[..end].to_string(),
        None => cause,
    }
}

/// What a panic said: its message, when it is text.
fn panic_message(panic: &(dyn Any + Send)) -> &str {
    if let Some(text) = panic.downcast_ref::<&str>() {
        text
    } else if let Some(text) = panic.downcast_ref::<String>() {
        text
    } else {
        "a value that is not text"
    }
}

/// A future whose panic, while it is polled, is its outcome.
struct CatchUnwind<F>(Pin<Box<F>>);

impl<F: Future> Future for CatchUnwind<F> {
    type Output = Result<F::Output, Box<dyn Any + Send>>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let future = self.0.as_mut();
        match catch_unwind(AssertUnwindSafe(|| future.poll(cx))) {
            Ok(Poll::Pending) => Poll::Pending,
            Ok(Poll::Ready(output)) => Poll::Ready(Ok(output)),
            Err(panic) => Poll::Ready(Err(panic)),
        }
    }
}

#[cfg(test)]
mod tests;
