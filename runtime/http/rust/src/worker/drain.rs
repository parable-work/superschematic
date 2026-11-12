//! A worker on a SQLite file as a function its host calls (D116), with the
//! `sqlite` feature: [`SqliteWorker::drain`], which takes a connection, the
//! time and a limit, claims and runs what is due, and answers how many
//! messages it ran and when the next falls due. It reads no clock of its
//! own and starts no task, so a host with no async runtime calls it on a
//! thread of its own and sleeps until that time, or until a commit of its
//! own process wakes it ([`Wake`]); [`run_drain`] is the loop over it on
//! tokio, which a stack's worker entrypoint runs.
//!
//! A worker is its queues, in the order a drain runs them, each added with
//! its descriptor and its handler:
//!
//! - [`SqliteWorker::queue`] adds a queue whose handler takes each message
//!   and answers `Ok` or an error, as a `@worker`'s method does. The drain
//!   completes the message of a handler that answers `Ok`, and fails the
//!   message of one that answers an error or panics, which the queue retries
//!   after its backoff or marks dead (D68).
//! - [`SqliteWorker::queue_in_unit`] adds a queue whose handler completes
//!   each message itself (D103): a behavior worker's deadline, change or
//!   event queue. The drain begins a transaction IMMEDIATE on the
//!   connection, hands the handler the connection inside it (a [`Unit`]),
//!   the claimed message with its id and its claim's token, and the time,
//!   and commits when the handler answers `Ok`: the handler spends the
//!   message in that transaction, by the rules adapter's `sqlite` module,
//!   so the message is spent in the unit that applies its change set. An
//!   error or a panic rolls the transaction back and fails the message.
//!
//! A drain claims one message at a time, for its queue's lease, and runs it
//! before it claims the next, so a claim's lease covers its own handler
//! alone. It extends no claim: a host with no runtime has no timer to
//! extend one with, so a handler that outlasts its queue's lease may see
//! its message claimed again, as after any expired claim. The claims are
//! the queue's statements on SQLite (D53, amended), which read the
//! database's clock, the host's own on a file; `now` is the time a
//! handler's commands run at, the time the next due time is measured from,
//! and the time the prune's cadence goes by.
//!
//! It prunes each queue as the claim loop prunes any (D68): at its first
//! drain and every five minutes of the times it is given, the done messages
//! past the queue's retention, [`PRUNE_BATCH`](super::PRUNE_BATCH) at a
//! time until a batch is short.
//!
//! It logs each outcome with the claim loop's messages and fields.

use std::marker::PhantomData;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::Path;
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use rusqlite::Connection;
use serde::de::DeserializeOwned;

use super::{
    cap, message_failed, message_span, panic_message, Claimed, DEFAULT_GRACE, DEFAULT_IDLE,
    DEFAULT_PRUNE_EVERY, MAX_BACKOFF, PRUNE_BATCH,
};
use crate::queue::sqlite::{
    claim_on, complete_on, fail_on, next_due_on, open_connection, prune_on, update_on,
};
use crate::queue::{Claim, QueueDefinition, QueueError};
use crate::{BoxError, Shutdown};

/// How many messages one drain of [`run_drain`] runs at most, so a stopping
/// worker waits for few.
pub const DEFAULT_DRAIN_LIMIT: usize = 16;

/// What a drain came to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Drained {
    /// How many messages it claimed and ran, whatever each came to.
    pub ran: usize,
    /// When the next message falls due: a ready message's due time or a
    /// claimed one's expiry, the earliest over the worker's queues. It is
    /// the time the drain was given when more is due already, and `None`
    /// when no message waits.
    pub next: Option<SystemTime>,
}

impl Drained {
    /// How long a host sleeps from `now` before it drains again: until the
    /// next message falls due, and `poll` at most, since a write another
    /// process made is heard at the next poll alone (D116).
    pub fn wait(&self, now: SystemTime, poll: Duration) -> Duration {
        match self.next {
            None => poll,
            Some(next) => next.duration_since(now).unwrap_or_default().min(poll),
        }
    }
}

/// The transaction a message of a [`SqliteWorker::queue_in_unit`] queue runs
/// in: the worker's connection, inside the IMMEDIATE transaction the drain
/// commits when the handler answers `Ok` and rolls back otherwise. It lends
/// the connection to one piece of work at a time, so a handler that awaits
/// may hold the unit across the await, and another thread may poll it.
#[derive(Clone, Copy)]
pub struct Unit<'a> {
    held: &'a (dyn Lend + 'a),
}

impl Unit<'_> {
    /// Runs `work` on the connection, inside the unit's transaction.
    pub fn with<T>(&self, work: impl FnOnce(&Connection) -> T) -> T {
        let mut work = Some(work);
        let mut out = None;
        self.held.lend(&mut |connection| {
            if let Some(work) = work.take() {
                out = Some(work(connection));
            }
        });
        match out {
            Some(out) => out,
            None => unreachable!("a unit lends its connection to each piece of work once"),
        }
    }
}

impl std::fmt::Debug for Unit<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Unit").finish_non_exhaustive()
    }
}

/// What a [`Unit`] lends its connection through, whatever the connection's
/// own lifetime.
trait Lend: Sync {
    fn lend(&self, work: &mut dyn FnMut(&Connection));
}

/// A drain's connection for the time of one unit, lent to one piece of
/// work at a time.
struct Held<'c>(Mutex<&'c mut Connection>);

impl Lend for Held<'_> {
    fn lend(&self, work: &mut dyn FnMut(&Connection)) {
        let connection = held(&self.0);
        work(&connection);
    }
}

/// A queue a worker drains.
trait Queue: Send + Sync {
    fn definition(&self) -> &QueueDefinition;

    /// When the queue was last pruned, by the times its drains were given.
    fn pruned(&self) -> &Mutex<Option<SystemTime>>;

    /// Claims one due message and runs it, and returns whether there was
    /// one. An error is a claim that failed; a handler's failure is its
    /// message's.
    fn run_one(&self, connection: &mut Connection, now: SystemTime) -> Result<bool, QueueError>;
}

/// A queue whose handler answers for its message, which the drain then
/// completes or fails.
struct Handled<M, H> {
    definition: QueueDefinition,
    handler: H,
    pruned: Mutex<Option<SystemTime>>,
    message: PhantomData<fn() -> M>,
}

impl<M, H> Queue for Handled<M, H>
where
    M: DeserializeOwned,
    H: Fn(M) -> Result<(), BoxError> + Send + Sync,
{
    fn definition(&self) -> &QueueDefinition {
        &self.definition
    }

    fn pruned(&self) -> &Mutex<Option<SystemTime>> {
        &self.pruned
    }

    fn run_one(&self, connection: &mut Connection, _now: SystemTime) -> Result<bool, QueueError> {
        let definition = &self.definition;
        let Some(claim) = claim_on::<M>(connection, definition, 1, definition.lease())?.pop()
        else {
            return Ok(false);
        };
        let Claim {
            id,
            token,
            attempt,
            message,
        } = claim;
        let started = Instant::now();
        let span = message_span(&definition.name, &id, attempt);
        let outcome = span.in_scope(|| caught(|| (self.handler)(message)));
        let took = started.elapsed().as_secs_f64();
        match outcome {
            Ok(()) => match complete_on(connection, definition, &id, &token) {
                Ok(()) => tracing::info!(message = id, attempt, took, "message handled"),
                Err(err) => tracing::error!(
                    message = id,
                    attempt,
                    took,
                    error = %err,
                    "message handled, but completing it failed"
                ),
            },
            Err(cause) => {
                message_failed(&span, &cause);
                fail(connection, definition, &id, &token, attempt, took, &cause);
            }
        }
        Ok(true)
    }
}

/// A queue whose handler completes its message itself, in the transaction
/// the drain runs it in (D103).
struct InUnit<M, H> {
    definition: QueueDefinition,
    handler: H,
    pruned: Mutex<Option<SystemTime>>,
    message: PhantomData<fn() -> M>,
}

impl<M, H> Queue for InUnit<M, H>
where
    M: DeserializeOwned,
    H: Fn(&Unit<'_>, Claimed<M>, SystemTime) -> Result<(), BoxError> + Send + Sync,
{
    fn definition(&self) -> &QueueDefinition {
        &self.definition
    }

    fn pruned(&self) -> &Mutex<Option<SystemTime>> {
        &self.pruned
    }

    fn run_one(&self, connection: &mut Connection, now: SystemTime) -> Result<bool, QueueError> {
        let definition = &self.definition;
        let Some(claim) = claim_on::<M>(connection, definition, 1, definition.lease())?.pop()
        else {
            return Ok(false);
        };
        let Claim {
            id,
            token,
            attempt,
            message,
        } = claim;
        if let Err(err) = connection.execute_batch("BEGIN IMMEDIATE") {
            // The message did not run: another connection held the write
            // lock past the busy timeout. It is given back, its attempt
            // not counted, and the host hears that the file is busy.
            let _ = update_on(
                connection,
                definition,
                "release",
                |s| &s.release,
                &id,
                &token,
                None,
            );
            return Err(QueueError::Failed(format!(
                "begin the transaction of message {id} on {}: {err}",
                definition.table
            )));
        }
        let started = Instant::now();
        let span = message_span(&definition.name, &id, attempt);
        let claimed = Claimed {
            id: id.clone(),
            token: token.clone(),
            message,
        };
        let outcome = {
            let held = Held(Mutex::new(&mut *connection));
            let unit = Unit { held: &held };
            span.in_scope(|| caught(|| (self.handler)(&unit, claimed, now)))
        };
        let outcome = outcome.and_then(|()| {
            connection
                .execute_batch("COMMIT")
                .map_err(|err| format!("commit the message's transaction: {err}"))
        });
        let took = started.elapsed().as_secs_f64();
        match outcome {
            Ok(()) => tracing::info!(message = id, attempt, took, "message handled"),
            Err(cause) => {
                // The handler's writes go with its transaction, and the
                // failure is recorded after it, in a statement of its own.
                if !connection.is_autocommit() {
                    let _ = connection.execute_batch("ROLLBACK");
                }
                message_failed(&span, &cause);
                fail(connection, definition, &id, &token, attempt, took, &cause);
            }
        }
        Ok(true)
    }
}

/// Runs a handler: its error, with its causes, or its panic, is the
/// message's cause, of at most the claim loop's length.
fn caught(handler: impl FnOnce() -> Result<(), BoxError>) -> Result<(), String> {
    match catch_unwind(AssertUnwindSafe(handler)) {
        Ok(Ok(())) => Ok(()),
        Ok(Err(err)) => Err(cap(crate::error::chain(err.as_ref()))),
        Err(panic) => Err(cap(format!("panic: {}", panic_message(panic.as_ref())))),
    }
}

/// Fails a message whose handler answered an error or panicked, and logs
/// what became of it, as the claim loop does.
fn fail(
    connection: &Connection,
    definition: &QueueDefinition,
    id: &str,
    token: &str,
    attempt: u32,
    took: f64,
    cause: &str,
) {
    match fail_on(connection, definition, id, token, cause) {
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

/// Deletes a queue's done messages past its retention when its last prune
/// is [`DEFAULT_PRUNE_EVERY`] before `now` or it has had none, a batch at a
/// time until a batch is short, and logs how many it deleted. A failed
/// batch ends the prune, which the next tries again.
fn prune(queue: &dyn Queue, connection: &Connection, now: SystemTime) {
    {
        let mut pruned = held(queue.pruned());
        let due = pruned.is_none_or(|last| {
            now.duration_since(last)
                .is_ok_and(|since| since >= DEFAULT_PRUNE_EVERY)
        });
        if !due {
            return;
        }
        *pruned = Some(now);
    }
    let mut pruned: u64 = 0;
    loop {
        match prune_on(connection, queue.definition(), PRUNE_BATCH) {
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

/// A lock, whoever panicked holding it: each holder only reads and writes
/// the value.
fn held<T>(lock: &Mutex<T>) -> MutexGuard<'_, T> {
    lock.lock().unwrap_or_else(PoisonError::into_inner)
}

/// A worker on a SQLite file: its queues, each with its handler, which
/// [`SqliteWorker::drain`] claims from and runs (D116).
#[derive(Default)]
pub struct SqliteWorker {
    queues: Vec<Box<dyn Queue>>,
}

impl std::fmt::Debug for SqliteWorker {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SqliteWorker")
            .field("queues", &self.name())
            .finish()
    }
}

impl SqliteWorker {
    /// A worker of no queue yet.
    pub fn new() -> Self {
        Self::default()
    }

    /// The worker with the queue `definition` describes, whose messages,
    /// read as `M`, `handler` handles: the drain completes a message whose
    /// handler answers `Ok` and fails one whose handler answers an error or
    /// panics. The handler runs outside any transaction, on the thread that
    /// drains; one that awaits drives its future there, with
    /// [`block_on`](crate::block_on). It refuses a descriptor with no
    /// statements on SQLite.
    pub fn queue<M, H>(
        mut self,
        definition: QueueDefinition,
        handler: H,
    ) -> Result<Self, QueueError>
    where
        M: DeserializeOwned + 'static,
        H: Fn(M) -> Result<(), BoxError> + Send + Sync + 'static,
    {
        runs_on_sqlite(&definition)?;
        self.queues.push(Box::new(Handled {
            definition,
            handler,
            pruned: Mutex::new(None),
            message: PhantomData,
        }));
        Ok(self)
    }

    /// The worker with the queue `definition` describes, whose handler
    /// completes each message itself (D103), in the [`Unit`] the drain runs
    /// it in: it takes the claimed message with its id and its claim's
    /// token, and the time the drain was given, and spends the message in
    /// the unit, which the drain commits when it answers `Ok`. An error or
    /// a panic rolls the unit back and fails the message. It refuses a
    /// descriptor with no statements on SQLite.
    pub fn queue_in_unit<M, H>(
        mut self,
        definition: QueueDefinition,
        handler: H,
    ) -> Result<Self, QueueError>
    where
        M: DeserializeOwned + 'static,
        H: Fn(&Unit<'_>, Claimed<M>, SystemTime) -> Result<(), BoxError> + Send + Sync + 'static,
    {
        runs_on_sqlite(&definition)?;
        self.queues.push(Box::new(InUnit {
            definition,
            handler,
            pruned: Mutex::new(None),
            message: PhantomData,
        }));
        Ok(self)
    }

    /// The worker's queues, as its log names them: `TicketDeadline,
    /// TicketChange`.
    pub fn name(&self) -> String {
        let names: Vec<&str> = self
            .queues
            .iter()
            .map(|queue| queue.definition().name.as_str())
            .collect();
        names.join(", ")
    }

    /// Claims and runs what is due of the worker's queues, each in turn, up
    /// to `limit` messages in all, on `connection`, the worker's own, which
    /// holds no transaction, and answers how many it ran and when the next
    /// falls due, read once everything ran, so a message a handler caused is
    /// counted. `now` is the host's time, as the module says. A drain of no
    /// due message only reads, but for its prune every five minutes, so it
    /// waits for no other connection's write.
    ///
    /// An error is a claim or a read that failed, the file held by another
    /// connection past the busy timeout among them; what ran before it
    /// stays run, and the host drains again later. A handler's failure is
    /// no error of the drain: its message is failed, and counted as run.
    pub fn drain(
        &self,
        connection: &mut Connection,
        now: SystemTime,
        limit: usize,
    ) -> Result<Drained, QueueError> {
        let mut ran = 0;
        for queue in &self.queues {
            let span = tracing::error_span!("worker", queue = queue.definition().name.as_str());
            let _entered = span.enter();
            prune(queue.as_ref(), connection, now);
            while ran < limit && queue.run_one(connection, now)? {
                ran += 1;
            }
        }
        let mut next: Option<i64> = None;
        for queue in &self.queues {
            let due = next_due_on(connection, queue.definition())?;
            next = match (next, due) {
                (Some(a), Some(b)) => Some(a.min(b)),
                (a, b) => a.or(b),
            };
        }
        let next = next.map(from_millis).map(|next| match next {
            // More is due than the limit let run, or a handler caused a
            // message since its queue was drained.
            next if next <= now && ran > 0 => now,
            // A message is due and was not claimed: one of an in-order
            // queue behind its key's head, or one the database's clock
            // has not reached. The next poll tries it.
            next if next <= now => now + DEFAULT_IDLE,
            next => next,
        });
        Ok(Drained { ran, next })
    }
}

/// Refuses a descriptor a drain cannot run: one with no statements on
/// SQLite.
fn runs_on_sqlite(definition: &QueueDefinition) -> Result<(), QueueError> {
    if definition.sqlite_statements.is_none() {
        return Err(QueueError::Definition(format!(
            "queue {} carries no statements on SQLite: its DB service lists no sqlite dialect, or its message holds a field the SQLite queue client cannot hold; see its types crate's queues.rs",
            definition.name
        )));
    }
    Ok(())
}

/// Epoch milliseconds as a time.
fn from_millis(millis: i64) -> SystemTime {
    match u64::try_from(millis) {
        Ok(after) => UNIX_EPOCH + Duration::from_millis(after),
        Err(_) => UNIX_EPOCH - Duration::from_millis(millis.unsigned_abs()),
    }
}

/// Opens a worker's connection to the SQLite file at `path`, the one its
/// drains run on: foreign keys on, write-ahead logging, and a statement
/// waiting up to five seconds for another connection's write lock, as a
/// [`SqliteQueue`](crate::queue::SqliteQueue) opens its own.
pub fn open(path: impl AsRef<Path>) -> Result<Connection, QueueError> {
    let path = path.as_ref();
    open_connection(path, &format!("open {} for a worker", path.display()))
}

/// What wakes a sleeping worker: a count of the commits its host made,
/// which a host's thread waits on with [`Wake::wait`] and the loop on tokio
/// with [`Wake::woken`]. The HTTP runtime's SQLite file notifies its own
/// (`orm::sqlite::Database::wake`) after each write it commits, so a write
/// of the worker's own process to a table a behavior hears is run at once,
/// and a worker woken for any other write reads that nothing is due and
/// sleeps again. A write another process made notifies no one here, and is
/// heard at the next poll (D116).
#[derive(Debug, Default)]
pub struct Wake {
    commits: Mutex<u64>,
    committed: Condvar,
    notify: tokio::sync::Notify,
}

impl Wake {
    /// A wake nothing has notified yet.
    pub fn new() -> Self {
        Self::default()
    }

    /// Says a write committed, which wakes every waiter.
    pub fn notify(&self) {
        *held(&self.commits) += 1;
        self.committed.notify_all();
        self.notify.notify_waiters();
    }

    /// How many commits it has been told of. A host reads it before it
    /// drains and waits on it after, so a commit made while the drain ran
    /// wakes it at once.
    pub fn seen(&self) -> u64 {
        *held(&self.commits)
    }

    /// Sleeps the calling thread until a commit after the `seen` one, or
    /// for `timeout`, and returns whether a commit woke it.
    pub fn wait(&self, seen: u64, timeout: Duration) -> bool {
        let (commits, _) = self
            .committed
            .wait_timeout_while(held(&self.commits), timeout, |commits| *commits == seen)
            .unwrap_or_else(PoisonError::into_inner);
        *commits != seen
    }

    /// Waits until a commit after the `seen` one. It needs no tokio
    /// runtime: any executor polls it.
    pub async fn woken(&self, seen: u64) {
        loop {
            let notified = self.notify.notified();
            let mut notified = std::pin::pin!(notified);
            // Registered before the count is read, so a commit between the
            // read and the wait is not missed.
            notified.as_mut().enable();
            if self.seen() != seen {
                return;
            }
            notified.await;
        }
    }
}

/// How [`run_drain`] runs. A zero in any of them is its default.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DrainOptions {
    /// How many messages one drain runs at most; [`DEFAULT_DRAIN_LIMIT`]
    /// by default.
    pub limit: usize,
    /// How long a stopping worker lets the running drain finish;
    /// [`DEFAULT_GRACE`](super::DEFAULT_GRACE) by default.
    pub grace: Duration,
    /// The longest it sleeps between drains, so a write another process
    /// made is heard; [`DEFAULT_IDLE`](super::DEFAULT_IDLE) by default.
    pub poll: Duration,
}

impl Default for DrainOptions {
    fn default() -> Self {
        Self {
            limit: DEFAULT_DRAIN_LIMIT,
            grace: DEFAULT_GRACE,
            poll: DEFAULT_IDLE,
        }
    }
}

impl DrainOptions {
    /// The options with each zero replaced by its default.
    fn or_defaults(self) -> Self {
        let defaults = Self::default();
        Self {
            limit: if self.limit == 0 {
                defaults.limit
            } else {
                self.limit
            },
            grace: if self.grace.is_zero() {
                defaults.grace
            } else {
                self.grace
            },
            poll: if self.poll.is_zero() {
                defaults.poll
            } else {
                self.poll
            },
        }
    }
}

/// The loop over [`SqliteWorker::drain`] on tokio (D116): what a stack's
/// worker on a SQLite file runs. Each of `connections`, the worker's own,
/// drains in turn on tokio's blocking pool, a message at a time, so the
/// worker handles as many messages at once as it has connections. After a
/// drain it sleeps until the next message falls due, the poll at most, or
/// until `wake` says its process committed a write, and after a drain that
/// failed, longer each time, doubling up to thirty seconds.
///
/// When `shutdown` is cancelled it starts no drain, and gives the running
/// ones the grace to finish. A drain cannot be dropped: a handler that
/// still runs when the grace is over is left to its claim's expiry, when
/// its message is due again. It returns `Ok` once it stopped, and an error
/// only for a worker it cannot run: one with no connection.
pub async fn run_drain(
    worker: Arc<SqliteWorker>,
    connections: Vec<Connection>,
    wake: Option<Arc<Wake>>,
    options: DrainOptions,
    shutdown: Shutdown,
) -> Result<(), QueueError> {
    if connections.is_empty() {
        return Err(QueueError::Definition(format!(
            "worker: no connection to drain {} on; it takes one or more",
            worker.name()
        )));
    }
    let options = options.or_defaults();
    let lanes: Vec<_> = connections
        .into_iter()
        .map(|connection| {
            tokio::spawn(lane(
                Arc::clone(&worker),
                connection,
                wake.clone(),
                options,
                shutdown.clone(),
            ))
        })
        .collect();
    for lane in lanes {
        if let Err(err) = lane.await {
            tracing::error!(error = %err, "a worker's drains ended early");
        }
    }
    tracing::info!("worker stopped");
    Ok(())
}

/// Drains on one connection until `shutdown` is cancelled.
async fn lane(
    worker: Arc<SqliteWorker>,
    mut connection: Connection,
    wake: Option<Arc<Wake>>,
    options: DrainOptions,
    shutdown: Shutdown,
) {
    let mut backoff = options.poll;
    while !shutdown.is_cancelled() {
        let seen = wake.as_ref().map(|wake| wake.seen());
        let Some((back, drained)) = drain_once(&worker, connection, options, &shutdown).await
        else {
            return;
        };
        connection = back;
        let wait = wait_after(drained, &mut backoff, options.poll);
        if wait.is_zero() {
            continue;
        }
        tokio::select! {
            () = shutdown.cancelled() => return,
            () = tokio::time::sleep(wait) => {}
            () = woken(wake.as_deref(), seen) => {}
        }
    }
}

/// Runs one drain on tokio's blocking pool and gives the connection back
/// with what it came to, or `None` when the lane ends: `shutdown` was
/// cancelled while it ran, and it finished within the grace or was left to
/// its claim's expiry, or it panicked outside a handler, and its connection
/// went with it.
async fn drain_once(
    worker: &Arc<SqliteWorker>,
    mut connection: Connection,
    options: DrainOptions,
    shutdown: &Shutdown,
) -> Option<(Connection, Result<Drained, QueueError>)> {
    let draining = Arc::clone(worker);
    let mut drain = tokio::task::spawn_blocking(move || {
        let drained = draining.drain(&mut connection, SystemTime::now(), options.limit);
        (connection, drained)
    });
    let joined = tokio::select! {
        joined = &mut drain => joined,
        () = shutdown.cancelled() => {
            stop(&mut drain, options.grace).await;
            return None;
        }
    };
    match joined {
        Ok(drained) => Some(drained),
        Err(err) => {
            tracing::error!(error = %err, "a drain did not finish");
            None
        }
    }
}

/// Gives a stopping lane's running drain the grace to finish, and says so
/// when it has not.
async fn stop<T>(drain: &mut tokio::task::JoinHandle<T>, grace: Duration) {
    tracing::info!(
        grace = grace.as_secs_f64(),
        "stopping: letting the running drain finish"
    );
    if tokio::time::timeout(grace, drain).await.is_err() {
        tracing::warn!(
            "the grace is over, and a handler still runs: its message is due again when its claim expires"
        );
    }
}

/// How long a lane sleeps after a drain: until the next message falls due,
/// the poll at most, or after one that failed, `backoff`, which doubles up
/// to thirty seconds until a drain succeeds.
fn wait_after(
    drained: Result<Drained, QueueError>,
    backoff: &mut Duration,
    poll: Duration,
) -> Duration {
    match drained {
        Ok(drained) => {
            *backoff = poll;
            drained.wait(SystemTime::now(), poll)
        }
        Err(err) => {
            let wait = *backoff;
            tracing::error!(retryIn = wait.as_secs_f64(), error = %err, "drain failed");
            *backoff = (wait * 2).min(MAX_BACKOFF);
            wait
        }
    }
}

/// Waits for a commit after the `seen` one, and for ever without a wake.
async fn woken(wake: Option<&Wake>, seen: Option<u64>) {
    match (wake, seen) {
        (Some(wake), Some(seen)) => wake.woken(seen).await,
        _ => std::future::pending().await,
    }
}
