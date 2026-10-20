//! An engine a server opens while it serves (D58, the `background`
//! feature), the twin of the Go binding's `engine.Background`: the server
//! starts while its database is not up yet, as its connection pools do,
//! and reports the engine unavailable, in its readiness and on the
//! engine's routes ([`crate::axum::background_router`]), until the engine
//! opens and its setup (a server's deploy, a runner's start) passes.
//!
//! [`Background::open`] tries on a task of the tokio runtime it is called
//! on, each attempt (the open, then the setup) on the blocking pool, since
//! the engine is synchronous and a Postgres client blocks on a runtime of
//! its own; between attempts it waits a second, doubling to thirty. Every
//! clone shares the one engine: [`Background::engine`] hands it out once it
//! opens, [`Background::wait`] waits for it, and [`Background::shutdown`]
//! stops trying, shuts the engine down and lets go of it, both on the
//! blocking pool.
//!
//! ```ignore
//! let engine = Background::open(
//!     move || options_of(&database),
//!     BackgroundOptions::default().setup(|engine| deploy(engine)),
//! );
//! let routes = superschematic_engine::axum::background_router(engine.clone(), Options::new("/engine/shop"))?;
//! // ... serve ...
//! engine.shutdown().await;
//! ```

use std::fmt;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use tokio::sync::{watch, Notify};

use crate::{Engine, EngineOptions};

/// The waits between attempts unless the options set them: a second,
/// doubling to thirty seconds.
pub const DEFAULT_RETRY_MIN: Duration = Duration::from_secs(1);
pub const DEFAULT_RETRY_MAX: Duration = Duration::from_secs(30);

/// Why an attempt failed: the engine's error, or a setup's own.
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

type Setup = Arc<dyn Fn(&Arc<Engine>) -> Result<(), BoxError> + Send + Sync>;
type OnError = Arc<dyn Fn(&BoxError, Duration) + Send + Sync>;
type OnOpen = Arc<dyn Fn(&Engine) + Send + Sync>;

/// How a [`Background`] opens its engine.
#[derive(Clone)]
pub struct BackgroundOptions {
    /// Runs on the engine once it opens, on the blocking pool, before the
    /// Background hands it out: a server's deploy, a runner's start. An
    /// error shuts the engine down, and the next attempt opens it again.
    pub setup: Option<Setup>,
    /// The bounds of the wait between attempts, which doubles from
    /// `retry_min` to `retry_max`.
    pub retry_min: Duration,
    pub retry_max: Duration,
    /// Told of each failed attempt and the wait before the next.
    pub on_error: Option<OnError>,
    /// Told of the engine once its setup passed, before
    /// [`Background::wait`] returns it.
    pub on_open: Option<OnOpen>,
}

impl Default for BackgroundOptions {
    fn default() -> Self {
        BackgroundOptions {
            setup: None,
            retry_min: DEFAULT_RETRY_MIN,
            retry_max: DEFAULT_RETRY_MAX,
            on_error: None,
            on_open: None,
        }
    }
}

impl fmt::Debug for BackgroundOptions {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("BackgroundOptions")
            .field("retry_min", &self.retry_min)
            .field("retry_max", &self.retry_max)
            .finish_non_exhaustive()
    }
}

impl BackgroundOptions {
    /// The setup an engine runs once it opens.
    pub fn setup(
        mut self,
        setup: impl Fn(&Arc<Engine>) -> Result<(), BoxError> + Send + Sync + 'static,
    ) -> Self {
        self.setup = Some(Arc::new(setup));
        self
    }

    /// What each failed attempt is told to.
    pub fn on_error(
        mut self,
        on_error: impl Fn(&BoxError, Duration) + Send + Sync + 'static,
    ) -> Self {
        self.on_error = Some(Arc::new(on_error));
        self
    }

    /// What the open engine is told to.
    pub fn on_open(mut self, on_open: impl Fn(&Engine) + Send + Sync + 'static) -> Self {
        self.on_open = Some(Arc::new(on_open));
        self
    }

    /// The bounds of the wait between attempts.
    pub fn retry(mut self, min: Duration, max: Duration) -> Self {
        self.retry_min = min;
        self.retry_max = max.max(min);
        self
    }
}

/// Why a [`Background`] has no engine to hand out.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum NotOpen {
    /// Not open yet: its database has not answered, or its setup has not
    /// passed; the last attempt's failure, if one failed.
    Opening(Option<String>),
    /// Shut down.
    Closed,
}

impl fmt::Display for NotOpen {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            NotOpen::Opening(None) => f.write_str("the engine is not open yet"),
            NotOpen::Opening(Some(last)) => write!(f, "the engine is not open yet: {last}"),
            NotOpen::Closed => f.write_str("the engine is closed"),
        }
    }
}

impl std::error::Error for NotOpen {}

/// An engine opened in the background. Cloning it is cheap, and every clone
/// is the one engine.
#[derive(Clone)]
pub struct Background {
    inner: Arc<Inner>,
}

struct Inner {
    state: Mutex<State>,
    /// Moves once the engine opens or the Background shuts down.
    settled: watch::Sender<bool>,
    /// Wakes the attempts' task from its wait when the Background shuts
    /// down.
    stop: Notify,
}

enum State {
    Opening(Option<String>),
    Open(Arc<Engine>),
    Closed,
}

impl fmt::Debug for Background {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let state = match &*self.state() {
            State::Opening(_) => "opening",
            State::Open(_) => "open",
            State::Closed => "closed",
        };
        f.debug_struct("Background").field("state", &state).finish()
    }
}

impl Background {
    /// Opens an engine on the options `options` builds, on a task of the
    /// current tokio runtime, trying again after each failure, the open's or
    /// the setup's, until it opens or the Background shuts down. It must be
    /// called within a tokio runtime.
    pub fn open<F>(options: F, background: BackgroundOptions) -> Background
    where
        F: Fn() -> crate::Result<EngineOptions> + Send + Sync + 'static,
    {
        let inner = Arc::new(Inner {
            state: Mutex::new(State::Opening(None)),
            settled: watch::Sender::new(false),
            stop: Notify::new(),
        });
        let task = Arc::clone(&inner);
        tokio::spawn(async move { run(task, Arc::new(options), background).await });
        Background { inner }
    }

    fn state(&self) -> std::sync::MutexGuard<'_, State> {
        self.inner
            .state
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
    }

    /// The engine once it opened and its setup passed, or why not.
    pub fn engine(&self) -> Result<Arc<Engine>, NotOpen> {
        match &*self.state() {
            State::Open(engine) => Ok(Arc::clone(engine)),
            State::Opening(last) => Err(NotOpen::Opening(last.clone())),
            State::Closed => Err(NotOpen::Closed),
        }
    }

    /// Waits for the engine to open, or for the Background to shut down.
    pub async fn wait(&self) -> Result<Arc<Engine>, NotOpen> {
        let mut settled = self.inner.settled.subscribe();
        // The sender lives as long as the Background, so the wait ends.
        let _ = settled.wait_for(|settled| *settled).await;
        self.engine()
    }

    /// Whether the engine is ready to serve: open, not shut down, not
    /// poisoned by a panic inside a transaction, and its database
    /// answering a read, which runs on the blocking pool. What a server's
    /// readiness reads.
    pub async fn ready(&self) -> Result<(), String> {
        let engine = self.engine().map_err(|why| why.to_string())?;
        if engine.is_closed() {
            return Err(NotOpen::Closed.to_string());
        }
        if engine.poisoned() {
            return Err(
                "the engine is poisoned: a panic escaped it inside a transaction".to_owned(),
            );
        }
        let read = tokio::task::spawn_blocking(move || {
            let read = engine.events().head().map(|_| ());
            drop(engine);
            read
        })
        .await;
        match read {
            Ok(Ok(())) => Ok(()),
            Ok(Err(error)) => Err(error.to_string()),
            Err(error) => Err(format!("the readiness read stopped: {error}")),
        }
    }

    /// Stops trying, then shuts the engine down if it opened (its runner
    /// stops and every event stream ends) and lets go of the Background's
    /// hold on it, on the blocking pool; the engine closes its storage once
    /// nothing else holds it. A later [`Background::engine`] is `Closed`.
    pub async fn shutdown(&self) {
        let previous = std::mem::replace(&mut *self.state(), State::Closed);
        self.inner.stop.notify_one();
        self.inner.settled.send_replace(true);
        if let State::Open(engine) = previous {
            let _ = tokio::task::spawn_blocking(move || {
                engine.shutdown();
                drop(engine);
            })
            .await;
        }
    }
}

// run tries until the engine opens or the Background shuts down.
async fn run<F>(inner: Arc<Inner>, options: Arc<F>, background: BackgroundOptions)
where
    F: Fn() -> crate::Result<EngineOptions> + Send + Sync + 'static,
{
    let mut wait = background.retry_min;
    loop {
        let closed = matches!(
            *inner.state.lock().unwrap_or_else(PoisonError::into_inner),
            State::Closed
        );
        if closed {
            return;
        }
        let options = Arc::clone(&options);
        let setup = background.setup.clone();
        let attempt = tokio::task::spawn_blocking(move || attempt(&*options, setup.as_ref())).await;
        let failure: BoxError = match attempt {
            Ok(Ok(engine)) => {
                let closed = {
                    let mut state = inner.state.lock().unwrap_or_else(PoisonError::into_inner);
                    let closed = matches!(*state, State::Closed);
                    if !closed {
                        *state = State::Open(Arc::clone(&engine));
                    }
                    closed
                };
                if closed {
                    // Shut down while it opened: let it go off the runtime.
                    let _ = tokio::task::spawn_blocking(move || {
                        engine.shutdown();
                        drop(engine);
                    })
                    .await;
                    return;
                }
                // on_open runs before the waiters wake, so a wait that
                // returns finds it told.
                if let Some(on_open) = &background.on_open {
                    on_open(&engine);
                }
                inner.settled.send_replace(true);
                // The task's own hold goes off the runtime too.
                let _ = tokio::task::spawn_blocking(move || drop(engine)).await;
                return;
            }
            Ok(Err(error)) => error,
            Err(panicked) => format!("the attempt to open the engine panicked: {panicked}").into(),
        };
        {
            let mut state = inner.state.lock().unwrap_or_else(PoisonError::into_inner);
            if matches!(*state, State::Closed) {
                return;
            }
            *state = State::Opening(Some(failure.to_string()));
        }
        if let Some(on_error) = &background.on_error {
            on_error(&failure, wait);
        }
        tokio::select! {
            () = tokio::time::sleep(wait) => {}
            () = inner.stop.notified() => return,
        }
        wait = (wait * 2).min(background.retry_max);
    }
}

// attempt opens the engine and sets it up, on the blocking pool; a setup
// that fails shuts the engine down before it goes.
fn attempt<F>(options: &F, setup: Option<&Setup>) -> Result<Arc<Engine>, BoxError>
where
    F: Fn() -> crate::Result<EngineOptions>,
{
    let engine = Engine::open_shared(options()?)?;
    if let Some(setup) = setup {
        if let Err(error) = setup(&engine) {
            engine.shutdown();
            return Err(error);
        }
    }
    Ok(engine)
}
