//! The loop's timing, on tokio's paused clock, over a queue in memory: each
//! test's clock starts at zero and jumps to the next timer whenever every
//! task waits, so a wait of seconds takes none.

use super::*;
use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

/// What the queue in memory was asked, and when.
#[derive(Clone, Debug, PartialEq)]
enum Call {
    Claim(usize),
    Extend(String),
    Complete(String),
    Fail(String, String),
    Release(String),
    Prune(usize),
}

/// What a prune does: delete so many messages, fail, or never answer.
#[derive(Clone, Copy, Debug)]
enum PruneDoes {
    Delete(u64),
    Fail,
    Hang,
}

/// What a claim does.
#[derive(Clone, Copy, Debug)]
enum ClaimDoes {
    Fail,
    Hang,
}

#[derive(Default)]
struct State {
    /// The messages due, each its ID and its value.
    ready: VecDeque<(String, u32)>,
    /// What the next claims do, before they claim as a queue does.
    script: VecDeque<ClaimDoes>,
    /// What the next prunes do; once they run out, a prune deletes none.
    prunes: VecDeque<PruneDoes>,
    next_token: usize,
    calls: Vec<(Duration, Call)>,
}

/// A queue in memory, whose every change succeeds.
#[derive(Clone)]
struct Memory {
    lease: Duration,
    start: Instant,
    state: Arc<Mutex<State>>,
}

impl Memory {
    fn new(lease: Duration, messages: u32) -> Self {
        let memory = Memory {
            lease,
            start: Instant::now(),
            state: Arc::default(),
        };
        memory.state.lock().unwrap().ready = (1..=messages).map(|n| (format!("m{n}"), n)).collect();
        memory
    }

    fn script(&self, does: impl IntoIterator<Item = ClaimDoes>) {
        self.state.lock().unwrap().script.extend(does);
    }

    fn prunes(&self, does: impl IntoIterator<Item = PruneDoes>) {
        self.state.lock().unwrap().prunes.extend(does);
    }

    fn record(&self, call: Call) {
        let at = self.start.elapsed();
        self.state.lock().unwrap().calls.push((at, call));
    }

    fn calls(&self) -> Vec<(Duration, Call)> {
        self.state.lock().unwrap().calls.clone()
    }

    /// The seconds at which each call `pick` takes was made.
    fn times(&self, pick: impl Fn(&Call) -> bool) -> Vec<f64> {
        self.calls()
            .iter()
            .filter(|(_, call)| pick(call))
            .map(|(at, _)| at.as_secs_f64())
            .collect()
    }

    fn of(&self, pick: impl Fn(&Call) -> Option<String>) -> Vec<String> {
        self.calls()
            .iter()
            .filter_map(|(_, call)| pick(call))
            .collect()
    }
}

#[async_trait]
impl WorkerQueue for Memory {
    type Message = u32;

    fn name(&self) -> &str {
        "Memory"
    }

    fn lease(&self) -> Duration {
        self.lease
    }

    async fn claim(&self, n: usize, _lease: Duration) -> Result<Vec<Claim<u32>>, QueueError> {
        self.record(Call::Claim(n));
        let does = self.state.lock().unwrap().script.pop_front();
        match does {
            Some(ClaimDoes::Fail) => return Err(QueueError::Failed("the database is down".into())),
            Some(ClaimDoes::Hang) => std::future::pending::<()>().await,
            None => {}
        }
        let mut state = self.state.lock().unwrap();
        let mut claims = Vec::new();
        while claims.len() < n {
            let Some((id, message)) = state.ready.pop_front() else {
                break;
            };
            state.next_token += 1;
            claims.push(Claim {
                id,
                token: format!("t{}", state.next_token),
                attempt: 1,
                message,
            });
        }
        Ok(claims)
    }

    async fn extend(&self, id: &str, _token: &str, _lease: Duration) -> Result<(), QueueError> {
        self.record(Call::Extend(id.into()));
        Ok(())
    }

    async fn complete(&self, id: &str, _token: &str) -> Result<(), QueueError> {
        self.record(Call::Complete(id.into()));
        Ok(())
    }

    async fn fail(&self, id: &str, _token: &str, cause: &str) -> Result<bool, QueueError> {
        self.record(Call::Fail(id.into(), cause.into()));
        Ok(false)
    }

    async fn release(&self, id: &str, _token: &str) -> Result<(), QueueError> {
        self.record(Call::Release(id.into()));
        Ok(())
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        self.record(Call::Prune(limit));
        let does = self.state.lock().unwrap().prunes.pop_front();
        match does {
            Some(PruneDoes::Delete(n)) => Ok(n),
            Some(PruneDoes::Fail) => Err(QueueError::Failed("the database is down".into())),
            Some(PruneDoes::Hang) => std::future::pending().await,
            None => Ok(0),
        }
    }
}

fn completed(call: &Call) -> Option<String> {
    match call {
        Call::Complete(id) => Some(id.clone()),
        _ => None,
    }
}

fn released(call: &Call) -> Option<String> {
    match call {
        Call::Release(id) => Some(id.clone()),
        _ => None,
    }
}

fn is_claim(call: &Call) -> bool {
    matches!(call, Call::Claim(_))
}

fn is_prune(call: &Call) -> bool {
    matches!(call, Call::Prune(_))
}

/// Runs the worker over `queue` in a task of its own, with `handler`.
fn spawn<H, F>(
    queue: &Memory,
    handler: H,
    options: Options,
) -> (Shutdown, tokio::task::JoinHandle<Result<(), QueueError>>)
where
    H: Fn(u32) -> F + Send + Sync + 'static,
    F: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue.clone(),
        handler,
        options,
        shutdown.clone(),
    ));
    (shutdown, run)
}

async fn wait(seconds: f64) {
    tokio::time::sleep(Duration::from_secs_f64(seconds)).await;
}

const LEASE: Duration = Duration::from_secs(30);

#[tokio::test(start_paused = true)]
async fn the_running_handlers_never_pass_the_concurrency() {
    let queue = Memory::new(LEASE, 5);
    let (now, most) = (Arc::new(AtomicUsize::new(0)), Arc::new(AtomicUsize::new(0)));
    let (counting, peak) = (Arc::clone(&now), Arc::clone(&most));
    let (shutdown, run) = spawn(
        &queue,
        move |_| {
            let (now, most) = (Arc::clone(&counting), Arc::clone(&peak));
            async move {
                most.fetch_max(now.fetch_add(1, Ordering::SeqCst) + 1, Ordering::SeqCst);
                wait(10.0).await;
                now.fetch_sub(1, Ordering::SeqCst);
                Ok(())
            }
        },
        Options {
            concurrency: 2,
            ..Options::default()
        },
    );
    wait(31.0).await;
    assert_eq!(most.load(Ordering::SeqCst), 2);
    let mut done = queue.of(completed);
    done.sort();
    assert_eq!(done, ["m1", "m2", "m3", "m4", "m5"]);
    // Three rounds of two, two and one, each ten seconds.
    let last = queue.times(|call| matches!(call, Call::Complete(_)));
    assert_eq!(last.last().copied(), Some(30.0));
    for (_, call) in queue.calls() {
        if let Call::Claim(n) = call {
            assert!((1..=2).contains(&n), "a claim for the free slots: {n}");
        }
    }
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn an_empty_queue_is_claimed_again_each_idle_wait() {
    let queue = Memory::new(LEASE, 0);
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, Options::default());
    wait(3.5).await;
    assert_eq!(queue.times(is_claim), [0.0, 1.0, 2.0, 3.0]);
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(queue.times(is_claim).len(), 4, "no claim after the stop");
}

#[tokio::test(start_paused = true)]
async fn a_failed_claim_waits_longer_each_time_up_to_thirty_seconds() {
    let queue = Memory::new(LEASE, 0);
    queue.script([ClaimDoes::Fail; 8]);
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, Options::default());
    wait(123.5).await;
    // Waits of 1, 2, 4, 8, 16 and then 30 seconds, and once a claim
    // succeeds, the idle wait again.
    assert_eq!(
        queue.times(is_claim),
        [0.0, 1.0, 3.0, 7.0, 15.0, 31.0, 61.0, 91.0, 121.0, 122.0, 123.0]
    );
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn a_claim_that_does_not_answer_fails_after_five_seconds() {
    let queue = Memory::new(LEASE, 0);
    queue.script([ClaimDoes::Hang, ClaimDoes::Hang]);
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, Options::default());
    wait(14.5).await;
    assert_eq!(queue.times(is_claim), [0.0, 6.0, 13.0, 14.0]);
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn a_running_handler_extends_its_claim_every_third_of_the_lease() {
    let queue = Memory::new(Duration::from_secs(3), 1);
    let (shutdown, run) = spawn(
        &queue,
        |_| async {
            wait(10.5).await;
            Ok(())
        },
        Options::default(),
    );
    wait(12.0).await;
    let extended = queue.times(|call| matches!(call, Call::Extend(_)));
    assert_eq!(
        extended,
        [1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0]
    );
    assert_eq!(
        queue.times(|call| matches!(call, Call::Complete(_))),
        [10.5]
    );
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(
        queue.times(|call| matches!(call, Call::Extend(_))).len(),
        10,
        "none after"
    );
}

/// An error with a cause, as a handler's error chain carries one.
#[derive(Debug)]
struct Outer(std::io::Error);

impl std::fmt::Display for Outer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("the supplier refused the order")
    }
}

impl std::error::Error for Outer {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&self.0)
    }
}

#[tokio::test(start_paused = true)]
async fn an_error_or_a_panic_fails_the_message_with_its_cause() {
    let queue = Memory::new(LEASE, 5);
    let (shutdown, run) = spawn(
        &queue,
        |n| async move {
            match n {
                1 => Err("out of stock".into()),
                2 => panic!("boom {n}"),
                3 => Err(Box::new(Outer(std::io::Error::other("no such product"))) as BoxError),
                4 => Err("é".repeat(MAX_CAUSE + 10).into()),
                _ => Ok(()),
            }
        },
        Options {
            concurrency: 5,
            ..Options::default()
        },
    );
    wait(0.5).await;
    let mut failed: Vec<(String, String)> = queue
        .calls()
        .into_iter()
        .filter_map(|(_, call)| match call {
            Call::Fail(id, cause) => Some((id, cause)),
            _ => None,
        })
        .collect();
    failed.sort();
    assert_eq!(
        failed,
        [
            ("m1".to_string(), "out of stock".to_string()),
            ("m2".to_string(), "panic: boom 2".to_string()),
            (
                "m3".to_string(),
                "the supplier refused the order: no such product".to_string()
            ),
            ("m4".to_string(), "é".repeat(MAX_CAUSE)),
        ]
    );
    assert_eq!(queue.of(completed), ["m5"]);
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn a_handler_that_panics_before_its_future_fails_its_message() {
    let queue = Memory::new(LEASE, 1);
    let (shutdown, run) = spawn(
        &queue,
        |n| -> std::future::Ready<Result<(), BoxError>> { panic!("no future for {n}") },
        Options::default(),
    );
    wait(0.5).await;
    let calls: Vec<Call> = queue
        .calls()
        .into_iter()
        .map(|(_, call)| call)
        .filter(|call| !is_prune(call))
        .collect();
    assert_eq!(
        calls[..2],
        [
            Call::Claim(1),
            Call::Fail("m1".into(), "panic: no future for 1".into())
        ]
    );
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn a_stopping_worker_lets_its_handlers_finish_within_the_grace() {
    let queue = Memory::new(LEASE, 3);
    let (shutdown, run) = spawn(
        &queue,
        |_| async {
            wait(2.0).await;
            Ok(())
        },
        Options {
            concurrency: 2,
            ..Options::default()
        },
    );
    wait(0.5).await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(Instant::now() - queue.start, Duration::from_secs(2));
    let mut done = queue.of(completed);
    done.sort();
    assert_eq!(done, ["m1", "m2"], "the third is never claimed");
    assert_eq!(queue.of(released), [] as [String; 0]);
    assert_eq!(queue.times(is_claim), [0.0]);
}

/// Sets its flag when it is dropped.
struct Dropped(Arc<AtomicBool>);

impl Drop for Dropped {
    fn drop(&mut self) {
        self.0.store(true, Ordering::SeqCst);
    }
}

#[tokio::test(start_paused = true)]
async fn after_the_grace_the_unfinished_handlers_are_dropped_and_their_messages_released() {
    let queue = Memory::new(LEASE, 3);
    let dropped = Arc::new(AtomicBool::new(false));
    let flags = Arc::clone(&dropped);
    let (shutdown, run) = spawn(
        &queue,
        move |n| {
            let flag = Arc::clone(&flags);
            async move {
                if n == 1 {
                    wait(1.0).await;
                    return Ok(());
                }
                let _guard = Dropped(flag);
                std::future::pending::<()>().await;
                Ok(())
            }
        },
        Options {
            concurrency: 3,
            grace: Duration::from_secs(3),
            ..Options::default()
        },
    );
    wait(0.5).await;
    assert!(!dropped.load(Ordering::SeqCst));
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(Instant::now() - queue.start, Duration::from_millis(3500));
    assert_eq!(queue.of(completed), ["m1"]);
    let mut gone = queue.of(released);
    gone.sort();
    assert_eq!(gone, ["m2", "m3"]);
    tokio::task::yield_now().await;
    assert!(
        dropped.load(Ordering::SeqCst),
        "the handler's future is dropped"
    );
    assert!(
        !queue
            .calls()
            .iter()
            .any(|(_, call)| matches!(call, Call::Fail(..))),
        "a released message is not failed"
    );
}

#[tokio::test(start_paused = true)]
async fn a_worker_prunes_as_it_starts_and_then_every_five_minutes() {
    let queue = Memory::new(LEASE, 0);
    let batch = PRUNE_BATCH as u64;
    // The first prune deletes two whole batches and a short one; the second
    // fails; the third deletes three.
    queue.prunes([
        PruneDoes::Delete(batch),
        PruneDoes::Delete(batch),
        PruneDoes::Delete(7),
        PruneDoes::Fail,
        PruneDoes::Delete(3),
    ]);
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, Options::default());
    wait(900.5).await;
    assert_eq!(
        queue.times(is_prune),
        [0.0, 0.0, 0.0, 300.0, 600.0, 900.0],
        "batches while one is whole, then a prune every five minutes, a failed one too"
    );
    assert!(queue
        .calls()
        .iter()
        .all(|(_, call)| !matches!(call, Call::Prune(n) if *n != PRUNE_BATCH)));
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(queue.times(is_prune).len(), 6, "no prune after the stop");
}

#[tokio::test(start_paused = true)]
async fn a_prune_that_does_not_answer_fails_after_five_seconds_and_the_next_runs() {
    let queue = Memory::new(LEASE, 0);
    queue.prunes([PruneDoes::Hang, PruneDoes::Delete(1)]);
    let options = Options {
        prune_every: Duration::from_secs(60),
        ..Options::default()
    };
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, options);
    wait(70.0).await;
    assert_eq!(queue.times(is_prune), [0.0, 65.0]);
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

#[tokio::test(start_paused = true)]
async fn a_stopping_worker_stops_its_prune() {
    let queue = Memory::new(LEASE, 0);
    queue.prunes([PruneDoes::Hang]);
    let (shutdown, run) = spawn(&queue, |_| async { Ok(()) }, Options::default());
    wait(1.5).await;
    shutdown.cancel();
    tokio::time::timeout(Duration::from_millis(1), run)
        .await
        .expect("the worker stops at once, its prune with it")
        .unwrap()
        .unwrap();
    assert_eq!(queue.times(is_prune), [0.0]);
}

#[tokio::test(start_paused = true)]
async fn a_queue_without_a_lease_is_refused() {
    let queue = Memory::new(Duration::ZERO, 1);
    let err = run_worker(
        queue,
        |_| async { Ok(()) },
        Options::default(),
        Shutdown::new(),
    )
    .await
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        "worker: queue Memory has a lease of 0 s; it is positive"
    );
}

#[test]
fn a_zero_option_is_its_default() {
    let options = Options {
        concurrency: 0,
        grace: Duration::ZERO,
        idle: Duration::ZERO,
        prune_every: Duration::ZERO,
        drain: None,
    }
    .or_defaults();
    assert_eq!(options, Options::default());
    let set = Options {
        concurrency: 4,
        grace: Duration::from_secs(2),
        idle: Duration::from_millis(10),
        prune_every: Duration::from_secs(60),
        drain: Some(Drain {
            idle: Duration::from_secs(45),
            slots: 3,
            horizon: Duration::from_secs(60),
        }),
    };
    assert_eq!(set.or_defaults(), set);
    let zero = Options {
        drain: Some(Drain {
            idle: Duration::ZERO,
            slots: 0,
            horizon: Duration::ZERO,
        }),
        ..Options::default()
    };
    assert_eq!(zero.or_defaults().drain, Some(Drain::default()));
}

#[test]
fn the_concurrency_is_the_environments_whole_number() {
    let vars = |value: &str| HashMap::from([(CONCURRENCY_VARIABLE.to_string(), value.to_string())]);
    assert_eq!(concurrency_from(3, &HashMap::new()), Ok(3));
    assert_eq!(concurrency_from(3, &vars("")), Ok(3));
    assert_eq!(concurrency_from(3, &vars("8")), Ok(8));
    for refused in ["0", "-1", "x", " 2", "1.5"] {
        let err = concurrency_from(3, &vars(refused)).unwrap_err();
        assert_eq!(
            err.to_string(),
            format!(
                "WORKER_CONCURRENCY is {}; it is a whole number, one or more",
                serde_json::to_string(refused).unwrap()
            )
        );
    }
}

#[test]
fn a_cause_is_cut_at_its_characters() {
    assert_eq!(cap("short".into()), "short");
    let long = format!("{}tail", "ü".repeat(MAX_CAUSE));
    assert_eq!(cap(long), "ü".repeat(MAX_CAUSE));
}

#[tokio::test(start_paused = true)]
async fn a_self_completing_queue_hands_each_handler_its_claim_and_completes_nothing_itself() {
    let queue = Memory::new(LEASE, 3);
    let seen = Arc::new(Mutex::new(Vec::new()));
    let saw = Arc::clone(&seen);
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        SelfCompleting::new(queue.clone()),
        move |claimed: Claimed<u32>| {
            let saw = Arc::clone(&saw);
            async move {
                let failing = claimed.message == 2;
                saw.lock()
                    .unwrap()
                    .push((claimed.id, claimed.token.is_empty(), claimed.message));
                if failing {
                    return Err::<(), BoxError>("the deadline's unit failed".into());
                }
                Ok(())
            }
        },
        Options::default(),
        shutdown.clone(),
    ));
    wait(2.0).await;
    let mut seen = seen.lock().unwrap().clone();
    seen.sort();
    assert_eq!(
        seen,
        [
            ("m1".to_owned(), false, 1),
            ("m2".to_owned(), false, 2),
            ("m3".to_owned(), false, 3)
        ]
    );
    // The handlers completed their messages; the loop completed none, and
    // failed the one whose handler failed.
    assert!(queue.of(completed).is_empty(), "{:?}", queue.calls());
    let failed: Vec<String> = queue
        .calls()
        .into_iter()
        .filter_map(|(_, call)| match call {
            Call::Fail(id, _) => Some(id),
            _ => None,
        })
        .collect();
    assert_eq!(failed, ["m2"]);
    shutdown.cancel();
    run.await.unwrap().unwrap();
}

// Drain mode (D145).

impl Memory {
    /// Adds a message, due at once.
    fn push(&self, id: &str, message: u32) {
        self.state
            .lock()
            .unwrap()
            .ready
            .push_back((id.to_string(), message));
    }
}

/// The locks of one queue, as a database keeps them: a slot takes one of
/// as many as it is asked for, and none when all are held.
#[derive(Default)]
struct Locks {
    held: AtomicUsize,
    asked: Mutex<Vec<usize>>,
    broken: AtomicBool,
}

/// A held lock of [`Locks`], given back as it drops.
struct Lock(Arc<Locks>);

impl Drop for Lock {
    fn drop(&mut self) {
        self.0.held.fetch_sub(1, Ordering::SeqCst);
    }
}

impl Locks {
    fn holds(&self) -> usize {
        self.held.load(Ordering::SeqCst)
    }

    fn asked(&self) -> Vec<usize> {
        self.asked.lock().unwrap().clone()
    }
}

/// When the next message of a [`Drained`] queue falls due.
type NextDue = Arc<dyn Fn() -> Result<Option<Duration>, QueueError> + Send + Sync>;

/// A queue in memory with slots and a next due time, which a worker in
/// drain mode reads.
#[derive(Clone)]
struct Drained {
    memory: Memory,
    locks: Arc<Locks>,
    next: Option<NextDue>,
    /// Before this time since the start, a claim finds nothing.
    due_from: Duration,
}

impl Drained {
    fn new(memory: &Memory, locks: &Arc<Locks>) -> Self {
        Drained {
            memory: memory.clone(),
            locks: Arc::clone(locks),
            next: None,
            due_from: Duration::ZERO,
        }
    }
}

#[async_trait]
impl WorkerQueue for Drained {
    type Message = u32;

    fn name(&self) -> &str {
        "Memory"
    }

    fn lease(&self) -> Duration {
        self.memory.lease
    }

    async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<u32>>, QueueError> {
        if self.memory.start.elapsed() < self.due_from {
            self.memory.record(Call::Claim(n));
            return Ok(Vec::new());
        }
        self.memory.claim(n, lease).await
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        self.memory.extend(id, token, lease).await
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.memory.complete(id, token).await
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        self.memory.fail(id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.memory.release(id, token).await
    }

    async fn slot(&self, slots: usize) -> Result<Option<Slot>, QueueError> {
        self.locks.asked.lock().unwrap().push(slots);
        if self.locks.broken.load(Ordering::SeqCst) {
            return Err(QueueError::Failed("the database is down".into()));
        }
        let taken = self
            .locks
            .held
            .try_update(Ordering::SeqCst, Ordering::SeqCst, |held| {
                (held < slots).then_some(held + 1)
            });
        Ok(taken
            .is_ok()
            .then(|| Slot::holding(Lock(Arc::clone(&self.locks)))))
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        match &self.next {
            Some(next) => next(),
            None => Ok(None),
        }
    }
}

fn drain(idle: f64) -> Options {
    Options {
        drain: Some(Drain {
            idle: Duration::from_secs_f64(idle),
            ..Drain::default()
        }),
        ..Options::default()
    }
}

fn run_drained<H, F>(
    queue: Drained,
    handler: H,
    options: Options,
) -> tokio::task::JoinHandle<Result<(), QueueError>>
where
    H: Fn(u32) -> F + Send + Sync + 'static,
    F: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    tokio::spawn(run_worker(queue, handler, options, Shutdown::new()))
}

#[tokio::test(start_paused = true)]
async fn a_drain_worker_returns_once_its_queue_is_idle_and_holds_its_slot_until_then() {
    let memory = Memory::new(LEASE, 2);
    let locks = Arc::new(Locks::default());
    // The first message's handler holds it for 100 s, past the idle time.
    let run = run_drained(
        Drained::new(&memory, &locks),
        |message| async move {
            if message == 1 {
                wait(100.0).await;
            }
            Ok(())
        },
        Options {
            concurrency: 2,
            ..drain(30.0)
        },
    );
    wait(50.0).await;
    assert!(
        !run.is_finished(),
        "the worker returned while a handler held a message"
    );
    assert_eq!(locks.holds(), 1, "the worker holds its slot while it runs");
    run.await.unwrap().unwrap();
    let mut done = memory.of(completed);
    done.sort();
    assert_eq!(done, ["m1", "m2"]);
    // The last handler returned at 100 s; the worker waits its idle time
    // after it, and stops at the claim after that.
    let claims = memory.times(is_claim);
    let last = claims.last().copied().unwrap();
    assert!(
        (130.0..=131.0).contains(&last),
        "the last claim at {last} s"
    );
    assert_eq!(locks.holds(), 0, "the slot is given back");
    assert_eq!(locks.asked(), [1]);
}

#[tokio::test(start_paused = true)]
async fn a_drain_worker_keeps_claiming_while_messages_come() {
    let memory = Memory::new(LEASE, 0);
    let locks = Arc::new(Locks::default());
    let run = run_drained(
        Drained::new(&memory, &locks),
        |_| async { Ok(()) },
        drain(30.0),
    );
    for (n, id) in ["a", "b", "c"].into_iter().enumerate() {
        wait(20.0).await;
        memory.push(id, u32::try_from(n).unwrap());
    }
    run.await.unwrap().unwrap();
    assert_eq!(memory.of(completed), ["a", "b", "c"]);
    // Three messages twenty seconds apart, and the idle time after the
    // last, which came at 60 s.
    let last = memory.times(is_claim).last().copied().unwrap();
    assert!(last >= 90.0, "the worker stopped at {last} s");
}

#[tokio::test(start_paused = true)]
async fn a_drain_worker_takes_a_slot_or_returns_at_once() {
    let locks = Arc::new(Locks::default());
    let first = Memory::new(LEASE, 1);
    let held = Arc::new(Notify::new());
    let gate = Arc::clone(&held);
    let first_run = run_drained(
        Drained::new(&first, &locks),
        move |_| {
            let gate = Arc::clone(&gate);
            async move {
                gate.notified().await;
                Ok(())
            }
        },
        drain(30.0),
    );
    wait(1.0).await;
    assert_eq!(locks.holds(), 1);

    // A second worker of one slot finds it taken: it claims nothing and
    // returns at once, whatever its idle time.
    let second = Memory::new(LEASE, 1);
    let started = Instant::now();
    run_worker(
        Drained::new(&second, &locks),
        |_| async { Ok(()) },
        drain(600.0),
        Shutdown::new(),
    )
    .await
    .unwrap();
    assert_eq!(started.elapsed(), Duration::ZERO);
    assert!(second.calls().is_empty(), "{:?}", second.calls());

    // With two slots, a third runs beside the first.
    let third = Memory::new(LEASE, 1);
    let third_run = run_drained(
        Drained::new(&third, &locks),
        |_| async { Ok(()) },
        Options {
            drain: Some(Drain {
                idle: Duration::from_secs(30),
                slots: 2,
                horizon: DRAIN_HORIZON,
            }),
            ..Options::default()
        },
    );
    wait(1.0).await;
    assert_eq!(third.of(completed), ["m1"]);
    assert_eq!(locks.holds(), 2, "two workers hold two slots");
    held.notify_one();
    first_run.await.unwrap().unwrap();
    third_run.await.unwrap().unwrap();
    assert_eq!(locks.holds(), 0, "every slot is given back");
    assert_eq!(locks.asked(), [1, 1, 2]);

    // A slot that cannot be asked for is the worker's error: it does not
    // drain unguarded.
    let broken = Arc::new(Locks::default());
    broken.broken.store(true, Ordering::SeqCst);
    let err = run_worker(
        Drained::new(&Memory::new(LEASE, 1), &broken),
        |_| async { Ok(()) },
        drain(30.0),
        Shutdown::new(),
    )
    .await
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        "worker: take a slot of queue Memory: the database is down"
    );

    // A pool's worker takes none.
    let pool = Arc::new(Locks::default());
    let shutdown = Shutdown::new();
    let pooled = tokio::spawn(run_worker(
        Drained::new(&Memory::new(LEASE, 0), &pool),
        |_| async { Ok(()) },
        Options::default(),
        shutdown.clone(),
    ));
    wait(5.0).await;
    shutdown.cancel();
    pooled.await.unwrap().unwrap();
    assert!(pool.asked().is_empty());
}

#[tokio::test(start_paused = true)]
async fn a_drain_worker_waits_for_a_message_due_within_its_horizon() {
    let memory = Memory::new(LEASE, 1);
    let locks = Arc::new(Locks::default());
    // The message falls due at 150 s; once it is handled the next is due
    // in an hour, past the horizon.
    let handled = Arc::new(AtomicBool::new(false));
    let read = Arc::clone(&handled);
    let start = Instant::now();
    let mut queue = Drained::new(&memory, &locks);
    queue.due_from = Duration::from_secs(150);
    queue.next = Some(Arc::new(move || {
        if read.load(Ordering::SeqCst) {
            return Ok(Some(Duration::from_secs(3600)));
        }
        Ok(Duration::from_secs(150).checked_sub(start.elapsed()))
    }));
    let done = Arc::clone(&handled);
    run_drained(
        queue,
        move |_| {
            done.store(true, Ordering::SeqCst);
            async { Ok(()) }
        },
        drain(20.0),
    )
    .await
    .unwrap()
    .unwrap();
    assert_eq!(memory.of(completed), ["m1"]);
    let handled_at = memory.times(|call| matches!(call, Call::Complete(_)));
    assert!(handled_at[0] >= 150.0, "handled at {} s", handled_at[0]);

    // A queue that reads no due time, one past the horizon, or one whose
    // read fails stops once idle.
    let nexts: [NextDue; 3] = [
        Arc::new(|| Ok(None)),
        Arc::new(|| Ok(Some(Duration::from_secs(600)))),
        Arc::new(|| Err(QueueError::Failed("the database is down".into()))),
    ];
    for next in nexts {
        let memory = Memory::new(LEASE, 0);
        let mut queue = Drained::new(&memory, &locks);
        queue.next = Some(next);
        run_drained(queue, |_| async { Ok(()) }, drain(20.0))
            .await
            .unwrap()
            .unwrap();
        let last = memory.times(is_claim).last().copied().unwrap();
        assert!(last <= 21.0, "the worker stopped at {last} s");
    }
}

#[tokio::test(start_paused = true)]
async fn a_drain_worker_whose_claims_fail_stops_with_an_error_and_gives_its_slot_back() {
    let memory = Memory::new(LEASE, 0);
    memory.script([ClaimDoes::Fail; 64]);
    let locks = Arc::new(Locks::default());
    let err = run_drained(
        Drained::new(&memory, &locks),
        |_| async { Ok(()) },
        drain(60.0),
    )
    .await
    .unwrap()
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        "worker: every claim of queue Memory failed for 60s, so the worker in drain mode stops: the database is down"
    );
    assert_eq!(locks.holds(), 0);

    // Claims that fail and recover within the idle time keep it draining.
    let recovering = Memory::new(LEASE, 1);
    recovering.script([ClaimDoes::Fail; 2]);
    run_drained(
        Drained::new(&recovering, &locks),
        |_| async { Ok(()) },
        drain(60.0),
    )
    .await
    .unwrap()
    .unwrap();
    assert_eq!(recovering.of(completed), ["m1"]);
}

#[tokio::test(start_paused = true)]
async fn a_groups_queues_drain_together_under_one_slot() {
    let (quiet, busy) = (Memory::new(LEASE, 0), Memory::new(LEASE, 1));
    let locks = Arc::new(Locks::default());
    let gate = Arc::new(Notify::new());
    let held = Arc::clone(&gate);
    let shutdown = Shutdown::new();
    let (quiet_queue, busy_queue) = (Drained::new(&quiet, &locks), Drained::new(&busy, &locks));
    let run = tokio::spawn(async move {
        let group = Group::new(&shutdown);
        assert!(group.take_slot(&quiet_queue, drain(30.0)).await?);
        let ran = tokio::join!(
            group.run(quiet_queue, |_| async { Ok(()) }, drain(30.0)),
            group.run(
                busy_queue,
                move |_| {
                    let held = Arc::clone(&held);
                    async move {
                        held.notified().await;
                        Ok(())
                    }
                },
                drain(30.0)
            ),
        );
        ran.0?;
        ran.1
    });
    wait(90.0).await;
    // The empty queue is still claimed from while the other's handler
    // runs: a message for it is handled.
    assert_eq!(locks.holds(), 1, "one slot for the group");
    quiet.push("late", 7);
    wait(2.0).await;
    assert_eq!(quiet.of(completed), ["late"]);
    gate.notify_one();
    run.await.unwrap().unwrap();
    assert_eq!(locks.holds(), 0);
    assert_eq!(
        locks.asked(),
        [1],
        "the group took one slot, of its first queue"
    );

    // A second group finds the slot taken and runs nothing.
    let other = Shutdown::new();
    let group = Group::new(&other);
    let first = Drained::new(&Memory::new(LEASE, 0), &locks);
    let _held = first.slot(1).await.unwrap().unwrap();
    assert!(!group.take_slot(&first, drain(30.0)).await.unwrap());
}

#[test]
fn the_drain_is_the_environments() {
    let vars = |pairs: &[(&str, &str)]| -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(name, value)| ((*name).to_string(), (*value).to_string()))
            .collect()
    };
    for mode in ["", MODE_POOL] {
        assert_eq!(drain_from(&vars(&[(MODE_VARIABLE, mode)])), Ok(None));
    }
    assert_eq!(drain_from(&HashMap::new()), Ok(None));
    assert_eq!(
        drain_from(&vars(&[(MODE_VARIABLE, MODE_DRAIN)])),
        Ok(Some(Drain::default()))
    );
    assert_eq!(
        drain_from(&vars(&[
            (MODE_VARIABLE, MODE_DRAIN),
            (IDLE_SECONDS_VARIABLE, "45"),
            (SLOTS_VARIABLE, "3")
        ])),
        Ok(Some(Drain {
            idle: Duration::from_secs(45),
            slots: 3,
            horizon: DRAIN_HORIZON,
        }))
    );
    for (name, value) in [(IDLE_SECONDS_VARIABLE, "soon"), (SLOTS_VARIABLE, "0")] {
        let err = drain_from(&vars(&[(MODE_VARIABLE, MODE_DRAIN), (name, value)])).unwrap_err();
        assert_eq!(
            err.to_string(),
            format!(
                "{name} is {}; it is a whole number, one or more",
                serde_json::to_string(value).unwrap()
            )
        );
    }
    assert_eq!(
        drain_from(&vars(&[(MODE_VARIABLE, "burst")]))
            .unwrap_err()
            .to_string(),
        r#"WORKER_MODE is "burst"; it is "pool" or "drain""#
    );
}
