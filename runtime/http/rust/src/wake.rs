//! The wake a queue client sends after an enqueue (D145; section 8.8 of
//! `docs/stack-model.md`), Go's `wake` package and the TypeScript runtime's
//! `queue-wake.ts` ported. A worker in drain mode runs as a job that empties
//! its queue and exits, so a queue that is empty runs nothing, and an
//! enqueue has to start it. A platform that can start one gives each
//! deployable that may enqueue to the queue its wakes in [`VARIABLE`], a
//! JSON object keyed by the queue's `@queue` class, each member the list of
//! the wakes of the workers that drain it: an HTTP POST each, with a bearer
//! token when the wake says where one comes from. The module names no
//! provider: on gcp the wake is the Cloud Run job's run method and the
//! token the metadata server's.
//!
//! A wake is a hint, never part of the write. [`enqueued`] sends it after
//! the enqueue's statement, in a task of its own, at most once per queue in
//! [`INTERVAL`] per process, and one that fails is logged as a warning and
//! nothing else: the worker's tick finds the message. A message due past
//! [`HORIZON`], the time a drain worker that found its queue idle waits for
//! one, sends none, since the tick finds it first, as TypeScript's HTTP
//! wake does. The queue client's [`enqueue`](crate::queue::enqueue) calls
//! it, and so does the generated code that runs the behaviors' core, for a
//! command's events, the deadlines it arms and the rows it writes (D151).
//! The process's waker reads [`VARIABLE`] the first time it is told of an
//! enqueue; a value that does not read is logged once, and the process then
//! sends no wake. A job awaits [`settled`] before it exits, so a wake sent
//! just before the end still goes out.

use std::collections::HashMap;
use std::sync::{Arc, Mutex, OnceLock, PoisonError};
use std::time::{Duration, Instant};

use serde::Deserialize;
use tokio_util::task::TaskTracker;

/// The environment variable that holds the wakes, as a platform sets it
/// (`ir.QueueWakesVariable`).
pub const VARIABLE: &str = "QUEUE_WAKES";

/// The least time between two wakes of one queue from one process.
pub const INTERVAL: Duration = Duration::from_secs(5);

/// How far ahead a wake looks: a drain worker that found its queue idle
/// waits for a message due within this long (the worker's `DRAIN_HORIZON`),
/// and its tick finds one due later.
pub const HORIZON: Duration = Duration::from_secs(5 * 60);

/// How long each of a wake's requests, the token's and the wake's, may take.
const TIMEOUT: Duration = Duration::from_secs(10);

/// How a queue's worker is started (`ir.QueueWake`): a POST of an empty
/// JSON object to `url`, with a bearer token when `token` says where one
/// comes from.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct Wake {
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub token: Option<Token>,
}

/// Where a wake's bearer token comes from: a GET of `url` with `headers`
/// answers a JSON object whose `access_token` member is the token and whose
/// `expires_in` member is how many seconds it lasts.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct Token {
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub headers: HashMap<String, String>,
}

/// Reads the value of [`VARIABLE`]: a JSON object that gives each queue the
/// list of its wakes. An empty value is no wakes. It refuses a value that
/// is no such object, and a wake with no URL, as Go's `wake.Parse` does.
pub fn parse(value: &str) -> Result<HashMap<String, Vec<Wake>>, String> {
    if value.trim().is_empty() {
        return Ok(HashMap::new());
    }
    let wakes: HashMap<String, Vec<Wake>> = serde_json::from_str(value)
        .map_err(|err| format!("{VARIABLE} is no JSON object of lists of wakes by queue: {err}"))?;
    for (queue, list) in &wakes {
        for wake in list {
            if wake.url.is_empty() {
                return Err(format!("{VARIABLE}: a wake of queue {queue} has no url"));
            }
            if wake
                .token
                .as_ref()
                .is_some_and(|token| token.url.is_empty())
            {
                return Err(format!(
                    "{VARIABLE}: a wake of queue {queue} names a token with no url"
                ));
            }
        }
    }
    Ok(wakes)
}

/// Sends the wakes of a set of queues, each queue's at most once in its
/// interval, [`INTERVAL`] unless a test says.
pub struct Waker {
    wakes: HashMap<String, Vec<Wake>>,
    client: Result<reqwest::Client, String>,
    interval: Duration,
    /// When each queue's wake was last sent.
    last: Mutex<HashMap<String, Instant>>,
    tokens: Tokens,
    tracker: TaskTracker,
}

/// The tokens a waker holds, by their URL, each with when it stops using
/// it.
type Tokens = Arc<Mutex<HashMap<String, (String, Instant)>>>;

impl std::fmt::Debug for Waker {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Waker")
            .field("queues", &self.wakes.keys().collect::<Vec<_>>())
            .finish_non_exhaustive()
    }
}

impl Waker {
    /// A waker of `wakes`.
    pub fn new(wakes: HashMap<String, Vec<Wake>>) -> Self {
        Self {
            wakes,
            client: reqwest::Client::builder()
                .timeout(TIMEOUT)
                .build()
                .map_err(|err| format!("building the HTTP client: {err}")),
            interval: INTERVAL,
            last: Mutex::default(),
            tokens: Tokens::default(),
            tracker: TaskTracker::new(),
        }
    }

    /// The waker of the wakes the environment gives in [`VARIABLE`]; one of
    /// none when it gives none.
    pub fn from_env() -> Result<Self, String> {
        let value = match std::env::var(VARIABLE) {
            Ok(value) => value,
            Err(std::env::VarError::NotPresent) => String::new(),
            Err(std::env::VarError::NotUnicode(_)) => {
                return Err(format!("{VARIABLE} is not UTF-8"))
            }
        };
        Ok(Self::new(parse(&value)?))
    }

    /// The waker with an interval of its own between two wakes of a queue,
    /// for a test.
    pub fn with_interval(mut self, interval: Duration) -> Self {
        self.interval = interval;
        self
    }

    /// Says a message was enqueued on `queue`, named by its `@queue` class,
    /// due after `delay`. It returns at once: the queue's wakes, when it has
    /// any, its message is due within [`HORIZON`] and none was sent within
    /// the interval, go out in tasks of their own, and a failure is logged.
    /// Outside a tokio runtime it sends none.
    pub fn enqueued(&self, queue: &str, delay: Duration) {
        let Some(wakes) = self.wakes.get(queue).filter(|wakes| !wakes.is_empty()) else {
            return;
        };
        if delay > HORIZON {
            return;
        }
        let Ok(runtime) = tokio::runtime::Handle::try_current() else {
            return;
        };
        let now = Instant::now();
        {
            let mut last = self.last.lock().unwrap_or_else(PoisonError::into_inner);
            if last
                .get(queue)
                .is_some_and(|sent| now.duration_since(*sent) < self.interval)
            {
                return;
            }
            last.insert(queue.to_string(), now);
        }
        for wake in wakes {
            let (wake, queue) = (wake.clone(), queue.to_string());
            let (client, tokens) = (self.client.clone(), Arc::clone(&self.tokens));
            self.tracker.spawn_on(
                async move {
                    if let Err(err) = send(client, &tokens, &wake).await {
                        tracing::warn!(
                            queue = queue.as_str(),
                            error = err.as_str(),
                            "the wake of a queue's worker failed: its tick finds the message"
                        );
                    }
                },
                &runtime,
            );
        }
    }

    /// Returns once every wake sent so far has been answered, for a process
    /// that is about to exit and a test.
    pub async fn settled(&self) {
        self.tracker.close();
        self.tracker.wait().await;
        self.tracker.reopen();
    }
}

/// POSTs the wake, with its token when it names one.
async fn send(
    client: Result<reqwest::Client, String>,
    tokens: &Tokens,
    wake: &Wake,
) -> Result<(), String> {
    let client = client?;
    let mut request = client
        .post(&wake.url)
        .header("content-type", "application/json")
        .body("{}");
    if let Some(token) = &wake.token {
        let bearer = token_of(&client, tokens, token)
            .await
            .map_err(|err| format!("the wake's token: {err}"))?;
        request = request.bearer_auth(bearer);
    }
    let response = request.send().await.map_err(|err| err.to_string())?;
    let status = response.status();
    if !status.is_success() {
        return Err(format!("the wake answered {status}"));
    }
    Ok(())
}

/// The bearer token `token` gives, the one the waker holds while that
/// lasts.
async fn token_of(
    client: &reqwest::Client,
    tokens: &Tokens,
    token: &Token,
) -> Result<String, String> {
    let held = tokens
        .lock()
        .unwrap_or_else(PoisonError::into_inner)
        .get(&token.url)
        .filter(|(_, until)| *until > Instant::now())
        .map(|(value, _)| value.clone());
    if let Some(value) = held {
        return Ok(value);
    }
    let mut request = client.get(&token.url);
    for (name, value) in &token.headers {
        request = request.header(name, value);
    }
    let response = request.send().await.map_err(|err| err.to_string())?;
    let status = response.status();
    if status != reqwest::StatusCode::OK {
        return Err(format!("the token request answered {status}"));
    }
    #[derive(Deserialize)]
    struct Answer {
        #[serde(default)]
        access_token: String,
        #[serde(default)]
        expires_in: f64,
    }
    let body = response.bytes().await.map_err(|err| err.to_string())?;
    let answer: Answer = serde_json::from_slice(&body).map_err(|err| err.to_string())?;
    if answer.access_token.is_empty() {
        return Err("the token request answered no access_token".to_string());
    }
    // A token is used until a minute before it ends, or for a minute when
    // it does not say.
    let lasts = if answer.expires_in > 120.0 {
        Duration::try_from_secs_f64(answer.expires_in - 60.0).unwrap_or(Duration::from_secs(60))
    } else {
        Duration::from_secs(60)
    };
    tokens
        .lock()
        .unwrap_or_else(PoisonError::into_inner)
        .insert(
            token.url.clone(),
            (answer.access_token.clone(), Instant::now() + lasts),
        );
    Ok(answer.access_token)
}

/// The process's waker, which reads [`VARIABLE`] once.
static PROCESS: OnceLock<Waker> = OnceLock::new();

fn process() -> &'static Waker {
    PROCESS.get_or_init(|| {
        Waker::from_env().unwrap_or_else(|err| {
            // Said once: the process then sends no wake, and its enqueues go
            // on.
            tracing::warn!(
                error = err.as_str(),
                "the wakes of the queues could not be read"
            );
            Waker::new(HashMap::new())
        })
    })
}

/// Says a message was enqueued on `queue`, named by its `@queue` class, due
/// after `delay`, to the process's waker, as [`Waker::enqueued`] says. It
/// never fails, and nothing waits for the wake.
pub fn enqueued(queue: &str, delay: Duration) {
    process().enqueued(queue, delay);
}

/// Returns once every wake the process's waker has sent has been answered:
/// a job awaits it before it exits.
pub async fn settled() {
    if let Some(waker) = PROCESS.get() {
        waker.settled().await;
    }
}

#[cfg(test)]
mod tests;
