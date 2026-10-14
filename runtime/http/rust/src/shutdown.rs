//! A process's shutdown (D65): the cancellation a stack's Rust job watches
//! and a worker's claim loop stops on. A generated job's method takes a
//! [`Shutdown`] and returns once it is cancelled, or sooner; a worker's
//! entrypoint passes one to `worker::run_worker`, which claims no more once
//! it is cancelled and gives the running handlers their grace.
//!
//! [`Shutdown::on_signals`] is the one an entrypoint makes: cancelled on
//! SIGTERM, which Cloud Run and the local provisioner send, or on SIGINT,
//! which a terminal's Ctrl-C sends. A second signal ends the process at
//! once, with 128 and the signal's number, as a generated server's second
//! signal does.

use std::io;

/// The token a [`Shutdown`] is, re-exported so a caller that needs more of
/// it (a child token, a drop guard) names the version this crate builds
/// with.
pub use tokio_util::sync::CancellationToken;

/// A cloneable cancellation: every clone sees the one [`Shutdown::cancel`],
/// which cannot be undone. A [`Shutdown::child`] is cancelled with its
/// parent, and may be cancelled alone.
#[derive(Clone, Debug, Default)]
pub struct Shutdown {
    token: CancellationToken,
}

impl Shutdown {
    /// A shutdown that nothing has cancelled yet.
    pub fn new() -> Self {
        Self::default()
    }

    /// A shutdown cancelled on the process's first SIGTERM or SIGINT, which
    /// it logs (`"shutting down"`, with the signal's name). A second signal
    /// ends the process at once, with status 143 for SIGTERM and 130 for
    /// SIGINT. On a platform without SIGTERM, Ctrl-C alone stops it.
    ///
    /// It registers the signals before it returns, so a signal that arrives
    /// after it is never lost, and spawns the task that waits for them: it
    /// is called inside a tokio runtime. It fails when the signals cannot be
    /// registered.
    pub fn on_signals() -> io::Result<Self> {
        let shutdown = Self::new();
        let mut signals = Signals::new()?;
        let token = shutdown.token.clone();
        tokio::spawn(async move {
            let (name, _) = signals.next().await;
            tracing::info!(signal = name, "shutting down");
            token.cancel();
            let (_, status) = signals.next().await;
            std::process::exit(status);
        });
        Ok(shutdown)
    }

    /// Cancels the shutdown, and every clone and child of it.
    pub fn cancel(&self) {
        self.token.cancel();
    }

    /// Whether the shutdown is cancelled.
    pub fn is_cancelled(&self) -> bool {
        self.token.is_cancelled()
    }

    /// Resolves once the shutdown is cancelled, at once if it is.
    pub async fn cancelled(&self) {
        self.token.cancelled().await;
    }

    /// [`Shutdown::cancelled`] over an owned shutdown, a future that
    /// borrows nothing, for a task of its own.
    pub async fn cancelled_owned(self) {
        self.token.cancelled_owned().await;
    }

    /// A shutdown cancelled when this one is, which can also be cancelled
    /// alone without cancelling this one.
    #[must_use]
    pub fn child(&self) -> Self {
        Self {
            token: self.token.child_token(),
        }
    }

    /// The token the shutdown is.
    pub fn token(&self) -> &CancellationToken {
        &self.token
    }
}

impl From<CancellationToken> for Shutdown {
    fn from(token: CancellationToken) -> Self {
        Self { token }
    }
}

impl From<Shutdown> for CancellationToken {
    fn from(shutdown: Shutdown) -> Self {
        shutdown.token
    }
}

/// SIGTERM and SIGINT.
#[cfg(unix)]
struct Signals {
    terminate: tokio::signal::unix::Signal,
    interrupt: tokio::signal::unix::Signal,
}

#[cfg(unix)]
impl Signals {
    fn new() -> io::Result<Self> {
        use tokio::signal::unix::{signal, SignalKind};
        Ok(Self {
            terminate: signal(SignalKind::terminate())?,
            interrupt: signal(SignalKind::interrupt())?,
        })
    }

    /// The next signal's name, and the status a process it ends at once
    /// exits with: 128 and its number.
    async fn next(&mut self) -> (&'static str, i32) {
        tokio::select! {
            _ = self.terminate.recv() => ("SIGTERM", 143),
            _ = self.interrupt.recv() => ("SIGINT", 130),
        }
    }
}

/// Ctrl-C, where there is no SIGTERM.
#[cfg(not(unix))]
struct Signals;

#[cfg(not(unix))]
impl Signals {
    fn new() -> io::Result<Self> {
        Ok(Self)
    }

    async fn next(&mut self) -> (&'static str, i32) {
        // A Ctrl-C that cannot be waited for never stops the process; its
        // platform still ends it.
        if tokio::signal::ctrl_c().await.is_err() {
            std::future::pending::<()>().await;
        }
        ("SIGINT", 130)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[tokio::test]
    async fn a_cancel_reaches_every_clone_and_child() {
        let shutdown = Shutdown::new();
        let clone = shutdown.clone();
        let child = shutdown.child();
        assert!(!clone.is_cancelled() && !child.is_cancelled());
        let waiting = tokio::spawn(clone.clone().cancelled_owned());
        shutdown.cancel();
        assert!(clone.is_cancelled() && child.is_cancelled());
        clone.cancelled().await;
        child.cancelled().await;
        tokio::time::timeout(Duration::from_secs(1), waiting)
            .await
            .unwrap()
            .unwrap();
    }

    #[tokio::test]
    async fn a_child_is_cancelled_alone_without_its_parent() {
        let parent = Shutdown::new();
        let child = parent.child();
        child.cancel();
        assert!(child.is_cancelled());
        assert!(!parent.is_cancelled());
        let token: CancellationToken = parent.clone().into();
        token.cancel();
        assert!(Shutdown::from(token).is_cancelled() && parent.is_cancelled());
    }
}
