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
//! The loop knows no database: it claims through a [`WorkerQueue`], which
//! [`PostgresQueue`](crate::queue::PostgresQueue) implements, so its tests
//! run over a queue in memory.
//!
//! It logs through `tracing`, in a span carrying `queue`, with the fields
//! and messages of Go's and TypeScript's loops: `message`, `attempt`,
//! `took` (seconds) and `error` on each outcome, `retryIn` (seconds) on a
//! failed claim, `pruned` on a prune, and `running`, `grace`, `released` and
//! `unfinished` as it stops. The `logging` module writes them as JSON
//! lines. With the `otel` feature and export on, each message's handling is
//! a consumer span, `process <queue>`, the root of a trace of its own (D67).

use std::any::Any;
use std::collections::HashMap;
use std::convert::Infallible;
use std::future::Future;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::pin::Pin;
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
use crate::queue::{Claim, QueueError};
use crate::stackconfig::{quote, StackConfigError};
use crate::{BoxError, Shutdown};

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
}

impl Default for Options {
    fn default() -> Self {
        Self {
            concurrency: 1,
            grace: DEFAULT_GRACE,
            idle: DEFAULT_IDLE,
            prune_every: DEFAULT_PRUNE_EVERY,
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

/// Claims and handles `queue`'s messages with `handler` until `shutdown` is
/// cancelled, then stops as the module says: it claims no more, lets the
/// running handlers finish within the grace, and drops those that do not,
/// releasing their messages. It returns `Ok` once it stopped, and an error
/// only for a queue it cannot run, whose lease is zero.
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
    let lease = queue.lease();
    if lease.is_zero() {
        return Err(QueueError::Definition(format!(
            "worker: queue {} has a lease of 0 s; it is positive",
            queue.name()
        )));
    }
    let span = tracing::error_span!("worker", queue = queue.name());
    let worker = Arc::new(Loop {
        queue,
        handler,
        options: options.or_defaults(),
        lease,
        running: Mutex::new(HashMap::new()),
        freed: Notify::new(),
        tracker: TaskTracker::new(),
        span: span.clone(),
    });
    async {
        tokio::join!(worker.claim_loop(&shutdown), worker.prune_loop(&shutdown));
        worker.stop().await;
    }
    .instrument(span)
    .await;
    Ok(())
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

    /// Claims messages for the free slots until `shutdown` is cancelled.
    async fn claim_loop(self: &Arc<Self>, shutdown: &Shutdown) {
        let mut wait = self.options.idle;
        while !shutdown.is_cancelled() {
            let free = self.options.concurrency.saturating_sub(self.count());
            if free == 0 {
                tokio::select! {
                    () = shutdown.cancelled() => return,
                    () = self.freed.notified() => continue,
                }
            }
            let claimed = tokio::select! {
                () = shutdown.cancelled() => return,
                claimed = timed(self.queue.claim(free, self.lease)) => claimed,
            };
            let claims = match claimed {
                Ok(claims) => claims,
                Err(err) => {
                    tracing::error!(retryIn = wait.as_secs_f64(), error = %err, "claim failed");
                    if !sleep(shutdown, wait).await {
                        return;
                    }
                    wait = (wait * 2).min(MAX_BACKOFF);
                    continue;
                }
            };
            wait = self.options.idle;
            let found = claims.len();
            for claim in claims {
                self.start(claim);
            }
            if found < free && !sleep(shutdown, self.options.idle).await {
                return;
            }
        }
    }

    /// Prunes the queue at once, then every `prune_every`, until `shutdown`
    /// is cancelled.
    async fn prune_loop(&self, shutdown: &Shutdown) {
        loop {
            self.prune(shutdown).await;
            if !sleep(shutdown, self.options.prune_every).await {
                return;
            }
        }
    }

    /// Deletes the queue's done messages past its retention, a batch at a
    /// time until a batch is short, and logs how many it deleted. A failed
    /// batch ends the prune, which the next tries again.
    async fn prune(&self, shutdown: &Shutdown) {
        let mut pruned: u64 = 0;
        loop {
            let batch = tokio::select! {
                () = shutdown.cancelled() => break,
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
