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
    let calls: Vec<Call> = queue.calls().into_iter().map(|(_, call)| call).collect();
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
    }
    .or_defaults();
    assert_eq!(options, Options::default());
    let set = Options {
        concurrency: 4,
        grace: Duration::from_secs(2),
        idle: Duration::from_millis(10),
    };
    assert_eq!(set.or_defaults(), set);
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
