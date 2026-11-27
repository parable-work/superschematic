//! What a command runs with (D103): the core, the layout, the clock the
//! wrappers read a command's time from, the request ids they give it, and
//! the wake of a behavior worker's queues (D145, D151).

use std::time::{SystemTime, UNIX_EPOCH};

use superschematic_rules::Core;

use crate::layout::Layout;

/// What a command runs with: the core a server built with the behaviors
/// its project composes, the layout its generator wrote, the time a
/// command the wrappers build runs at, in epoch milliseconds, and its
/// request id, unique per command. A `Core` is `Send` and `Sync`, so one
/// core serves every task; a host borrows it and the layout, and costs
/// nothing to make per request.
pub struct Host<'a> {
    pub(crate) core: &'a Core,
    pub(crate) layout: &'a Layout,
    clock: Box<dyn Fn() -> i64 + Send + Sync + 'a>,
    requests: Box<dyn Fn() -> String + Send + Sync + 'a>,
    wake: Option<Wake<'a>>,
}

impl<'a> Host<'a> {
    /// A host on a core and a layout, its clock the system's in epoch
    /// milliseconds and its request ids random version 4 UUIDs' text.
    pub fn new(core: &'a Core, layout: &'a Layout) -> Self {
        Host {
            core,
            layout,
            clock: Box::new(system_millis),
            requests: Box::new(|| uuid::Uuid::new_v4().to_string()),
            wake: None,
        }
    }

    /// The host with a clock of its own, in epoch milliseconds.
    pub fn with_clock(mut self, now: impl Fn() -> i64 + Send + Sync + 'a) -> Self {
        self.clock = Box::new(now);
        self
    }

    /// The host with request ids of its own, each unique per command.
    pub fn with_requests(mut self, request: impl Fn() -> String + Send + Sync + 'a) -> Self {
        self.requests = Box::new(request);
        self
    }

    /// The host with the wake of a behavior worker's queues (D145, D151):
    /// once a command's unit has applied, before the caller's transaction
    /// commits, the adapter calls `wake` with the `@queue` class of each
    /// queue the unit gave a message its worker should not wait for its
    /// next start to find, each once: the event queue of each event, the
    /// deadline queue of each deadline armed due within
    /// [`WAKE_HORIZON_MS`] of the command's time, and the change queue of
    /// each row it wrote, whose trigger writes the message. A server hands
    /// it the HTTP runtime's queue wake, which sends a queue's at most once
    /// in five seconds; `wake` returns at once and fails nothing. A host
    /// without one wakes nothing.
    pub fn with_wake(mut self, wake: impl Fn(&str) + Send + Sync + 'a) -> Self {
        self.wake = Some(Box::new(wake));
        self
    }

    /// Wakes the worker of `queue`, when the host has a wake: the run loop
    /// does, which a build with no store leaves out.
    #[cfg_attr(
        not(any(feature = "postgres", feature = "sqlite", test)),
        allow(dead_code)
    )]
    pub(crate) fn wake(&self, queue: &str) {
        if let Some(wake) = &self.wake {
            wake(queue);
        }
    }

    /// The core it runs.
    pub fn core(&self) -> &'a Core {
        self.core
    }

    /// The layout it maps the core's names by.
    pub fn layout(&self) -> &'a Layout {
        self.layout
    }

    /// The time a command runs at, by the host's clock.
    pub fn now(&self) -> i64 {
        (self.clock)()
    }

    /// A new request id.
    pub fn request(&self) -> String {
        (self.requests)()
    }
}

impl std::fmt::Debug for Host<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Host")
            .field("core", self.core)
            .field("layout", &self.layout.types.keys().collect::<Vec<_>>())
            .finish_non_exhaustive()
    }
}

/// How far ahead of a command's time a deadline it arms wakes its worker
/// (D145): a worker in drain mode that found its queue idle waits for a
/// message due within this long, five minutes, and its tick finds one due
/// later. Go's adapter and the TypeScript runtime's HTTP wake look as far.
pub const WAKE_HORIZON_MS: i64 = 5 * 60 * 1000;

/// What [`Host::with_wake`] takes: the wake of a queue's worker, by the
/// queue's `@queue` class.
type Wake<'a> = Box<dyn Fn(&str) + Send + Sync + 'a>;

/// The system's time in epoch milliseconds.
fn system_millis() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
        Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
    }
}
