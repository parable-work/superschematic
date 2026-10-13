//! What wakes a waiter when a write commits (D58): the event stream,
//! `watch` and the runner block on it rather than polling the database.
//!
//! The notifier counts commits that notified ([`crate::Tx::notify`]) as
//! an epoch. A waiter reads the epoch, reads the database, and waits for
//! the epoch to pass the one it read; a commit between the two readings
//! has already moved it, so no commit is missed. On SQLite one process
//! writes the file and the commit moves the epoch itself. On Postgres a
//! commit also sends `pg_notify` on the engine's channel inside its
//! transaction, so Postgres delivers it only if the transaction commits,
//! and each process's listener ([`crate::Db::open_postgres`]) moves its
//! own epoch when one arrives, so a write on any instance of a server
//! wakes every instance. A listener that reconnects moves the epoch too,
//! since it may have missed a notification while it was away.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Condvar, Mutex, PoisonError};
use std::time::{Duration, Instant};

/// A count of the commits that notified, which a waiter can block on.
#[derive(Debug, Default)]
pub struct Notifier {
    epoch: Mutex<u64>,
    moved: Condvar,
    listening: AtomicBool,
}

impl Notifier {
    pub fn new() -> Self {
        Self::default()
    }

    /// The current epoch.
    pub fn epoch(&self) -> u64 {
        *self.epoch.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Moves the epoch on and wakes every waiter; returns the new epoch.
    pub fn bump(&self) -> u64 {
        let mut epoch = self.epoch.lock().unwrap_or_else(PoisonError::into_inner);
        *epoch += 1;
        let now = *epoch;
        drop(epoch);
        self.moved.notify_all();
        now
    }

    /// Waits until the epoch passes `after` or `timeout` elapses, and
    /// returns the epoch: past `after` when a commit woke it, `after` (or
    /// less) when the wait timed out.
    pub fn wait(&self, after: u64, timeout: Duration) -> u64 {
        let deadline = Instant::now() + timeout;
        let mut epoch = self.epoch.lock().unwrap_or_else(PoisonError::into_inner);
        while *epoch <= after {
            let now = Instant::now();
            if now >= deadline {
                break;
            }
            epoch = self
                .moved
                .wait_timeout(epoch, deadline - now)
                .unwrap_or_else(PoisonError::into_inner)
                .0;
        }
        *epoch
    }

    /// Whether a Postgres listener is connected and listening, so a commit
    /// on another connection or process moves this epoch; always false on
    /// SQLite, where none is needed.
    pub fn listening(&self) -> bool {
        self.listening.load(Ordering::SeqCst)
    }

    #[cfg_attr(not(feature = "postgres"), allow(dead_code))]
    pub(crate) fn set_listening(&self, listening: bool) {
        self.listening.store(listening, Ordering::SeqCst);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::thread;

    #[test]
    fn a_waiter_wakes_when_the_epoch_moves_and_not_before() {
        let notifier = Arc::new(Notifier::new());
        let start = notifier.epoch();
        assert_eq!(notifier.wait(start, Duration::from_millis(20)), start);
        let waiter = {
            let notifier = Arc::clone(&notifier);
            thread::spawn(move || notifier.wait(start, Duration::from_secs(10)))
        };
        thread::sleep(Duration::from_millis(20));
        assert_eq!(notifier.bump(), start + 1);
        assert_eq!(waiter.join().unwrap(), start + 1);
        // A commit before the wait has moved the epoch already.
        assert_eq!(notifier.wait(start, Duration::ZERO), start + 1);
        assert!(!notifier.listening());
    }
}
