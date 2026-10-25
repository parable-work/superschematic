//! What carries a signal (D77): a message about an instance that the engine
//! delivers to the event streams open on it and keeps nowhere. The storage
//! carries a signal's payload, text it does not read, from the process
//! that sends it to every process's sinks ([`Signals::subscribe`]); the
//! engine writes the payload and reads it at its sinks.
//!
//! A signal is sent in a transaction ([`crate::Tx::signal`]) and delivered
//! only if the transaction commits, in the order sent; a savepoint that
//! rolls back drops what it sent. On SQLite, which one process writes, the
//! commit hands each payload to this process's sinks. On Postgres the
//! transaction sends it with `pg_notify` on the signal channel, a channel of
//! its own beside the commit notifier's and distinct per engine schema, so
//! Postgres delivers it only if the transaction commits, and every
//! process's listener hands it to that process's sinks, the sender's
//! included; a storage opened with no listener (`listen: false`) hands it to
//! its own sinks at the commit instead. Delivery is at most once: a
//! listener that is reconnecting misses what is sent meanwhile.
//!
//! Postgres refuses a notification's payload of 8000 bytes or more, so a
//! payload is at most [`MAX_SIGNAL_BYTES`]. Postgres also folds identical
//! notifications of one transaction into one, so each carries a number of
//! the sending process's before its payload, which the listener strips.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, PoisonError, Weak};

/// The longest payload a signal carries, in bytes: under the 8000 bytes of
/// a Postgres notification, with room for the number that tags it.
pub const MAX_SIGNAL_BYTES: usize = 7900;

/// What a signal's payload is handed to in a process.
pub type SignalSink = dyn Fn(&str) + Send + Sync;

/// The sinks of a storage's signals in this process.
#[derive(Default)]
pub struct Signals {
    sinks: Mutex<Vec<Weak<SignalSink>>>,
    sent: AtomicU64,
}

impl std::fmt::Debug for Signals {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Signals").finish_non_exhaustive()
    }
}

impl Signals {
    pub fn new() -> Self {
        Self::default()
    }

    /// Hands every signal the storage delivers from now on to `sink`, on
    /// the thread that delivers it (the committer's, or the listener's),
    /// until the sink is dropped: the storage holds it weakly.
    pub fn subscribe(&self, sink: &Arc<SignalSink>) {
        self.sinks().push(Arc::downgrade(sink));
    }

    /// Hands a payload to every sink that is still held.
    pub(crate) fn deliver(&self, payload: &str) {
        let live: Vec<Arc<SignalSink>> = {
            let mut sinks = self.sinks();
            sinks.retain(|sink| sink.strong_count() > 0);
            sinks.iter().filter_map(Weak::upgrade).collect()
        };
        for sink in live {
            let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| sink(payload)));
        }
    }

    /// A payload as a Postgres notification carries it: a number of this
    /// process's, a space, then the payload.
    #[cfg_attr(not(feature = "postgres"), allow(dead_code))]
    pub(crate) fn tag(&self, payload: &str) -> String {
        let n = self.sent.fetch_add(1, Ordering::Relaxed);
        format!("{n:x} {payload}")
    }

    fn sinks(&self) -> std::sync::MutexGuard<'_, Vec<Weak<SignalSink>>> {
        self.sinks.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// The payload a tagged notification carries ([`Signals::tag`]).
#[cfg_attr(not(feature = "postgres"), allow(dead_code))]
pub(crate) fn untag(tagged: &str) -> &str {
    tagged
        .split_once(' ')
        .map_or(tagged, |(_, payload)| payload)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_payload_reaches_every_sink_still_held() {
        let signals = Signals::new();
        let seen = Arc::new(Mutex::new(Vec::new()));
        let kept: Arc<SignalSink> = {
            let seen = Arc::clone(&seen);
            Arc::new(move |payload: &str| seen.lock().unwrap().push(payload.to_owned()))
        };
        let dropped: Arc<SignalSink> = Arc::new(|_: &str| panic!("dropped"));
        signals.subscribe(&kept);
        signals.subscribe(&dropped);
        drop(dropped);
        signals.deliver("a");
        assert_eq!(*seen.lock().unwrap(), ["a"]);
        let tagged = signals.tag("{\"x\": 1}");
        assert_ne!(tagged, signals.tag("{\"x\": 1}"));
        assert_eq!(untag(&tagged), "{\"x\": 1}");
    }
}
