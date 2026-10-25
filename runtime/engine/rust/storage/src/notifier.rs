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
//!
//! It counts wakes too: every commit's, and every signal's (D77) that an
//! event stream of this process kept ([`Notifier::stir`]). A waiter for
//! commits (the runner, `watch`) waits on the epoch, so a signal never
//! wakes it; a waiter for an event stream waits on the wakes, so a signal
//! reaches its stream as soon as a commit would.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Condvar, Mutex, PoisonError};
use std::time::{Duration, Instant};

/// A count of the commits that notified, which a waiter can block on.
#[derive(Debug, Default)]
pub struct Notifier {
    counts: Mutex<Counts>,
    moved: Condvar,
    listening: AtomicBool,
}

// The commits counted, and the wakes: every commit's and every signal's.
#[derive(Debug, Default)]
struct Counts {
    epoch: u64,
    wakes: u64,
}

impl Notifier {
    pub fn new() -> Self {
        Self::default()
    }

    /// The current epoch.
    pub fn epoch(&self) -> u64 {
        self.counts().epoch
    }

    /// The wakes counted: the epoch's commits and the signals
    /// [`Notifier::stir`] counted, so never behind the epoch's moves.
    pub fn wakes(&self) -> u64 {
        self.counts().wakes
    }

    /// Moves the epoch, and the wakes, on and wakes every waiter; returns
    /// the new epoch.
    pub fn bump(&self) -> u64 {
        let mut counts = self.counts();
        counts.epoch += 1;
        counts.wakes += 1;
        let now = counts.epoch;
        drop(counts);
        self.moved.notify_all();
        now
    }

    /// Moves the wakes on, and not the epoch, and wakes every waiter: a
    /// signal an event stream kept (D77). Returns the new count of wakes.
    pub fn stir(&self) -> u64 {
        let mut counts = self.counts();
        counts.wakes += 1;
        let now = counts.wakes;
        drop(counts);
        self.moved.notify_all();
        now
    }

    /// Waits until the epoch passes `after` or `timeout` elapses, and
    /// returns the epoch: past `after` when a commit woke it, `after` (or
    /// less) when the wait timed out.
    pub fn wait(&self, after: u64, timeout: Duration) -> u64 {
        self.wait_for(after, timeout, |counts| counts.epoch)
    }

    /// [`Notifier::wait`] for the wakes: until a commit or a signal moves
    /// them past `after`, or `timeout` elapses.
    pub fn wait_wakes(&self, after: u64, timeout: Duration) -> u64 {
        self.wait_for(after, timeout, |counts| counts.wakes)
    }

    fn wait_for(&self, after: u64, timeout: Duration, count: fn(&Counts) -> u64) -> u64 {
        let deadline = Instant::now() + timeout;
        let mut counts = self.counts();
        while count(&counts) <= after {
            let now = Instant::now();
            if now >= deadline {
                break;
            }
            counts = self
                .moved
                .wait_timeout(counts, deadline - now)
                .unwrap_or_else(PoisonError::into_inner)
                .0;
        }
        count(&counts)
    }

    fn counts(&self) -> std::sync::MutexGuard<'_, Counts> {
        self.counts.lock().unwrap_or_else(PoisonError::into_inner)
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

    #[test]
    fn a_stir_moves_the_wakes_and_not_the_epoch() {
        let notifier = Arc::new(Notifier::new());
        let (epoch, wakes) = (notifier.epoch(), notifier.wakes());
        let waiter = {
            let notifier = Arc::clone(&notifier);
            thread::spawn(move || notifier.wait_wakes(wakes, Duration::from_secs(10)))
        };
        thread::sleep(Duration::from_millis(20));
        assert_eq!(notifier.stir(), wakes + 1);
        assert_eq!(waiter.join().unwrap(), wakes + 1);
        assert_eq!(notifier.epoch(), epoch);
        assert_eq!(notifier.wait(epoch, Duration::from_millis(20)), epoch);
        // A commit moves both.
        notifier.bump();
        assert_eq!((notifier.epoch(), notifier.wakes()), (epoch + 1, wakes + 2));
    }
}
