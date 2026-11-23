//! The host's signals (D77's primitive 2): what each request's committed
//! unit sent, handed to the event streams open with signals whose filters
//! keep it and whose caller may read its type, each stream's queue at most
//! [`SIGNAL_BUFFER`] long, the oldest dropped when it is full. Nothing is
//! stored: a stream opened after a signal never sees it, and delivery is at
//! most once and in no order with the events.
//!
//! The hub is the process's: development mode serves one store from one
//! process, so its streams hear every signal its requests send. A second
//! process over the same Postgres schema hears none of this one's (the
//! engine's `pg_notify` channel is not development mode's).

use std::collections::{BTreeMap, VecDeque};
use std::sync::{Arc, Mutex, PoisonError};

use superschematic_rules::Caller;
use superschematic_rules_handler::events::EventsQuery;
use superschematic_rules_handler::signals::SIGNAL_BUFFER;
use superschematic_rules_handler::HostSignal;

/// One stream's subscription: its caller, its filters and what it holds.
struct Subscriber {
    caller: Caller,
    query: EventsQuery,
    queue: VecDeque<HostSignal>,
}

/// The streams open with signals, by the id each was given.
#[derive(Default)]
pub struct Hub {
    next: u64,
    open: BTreeMap<u64, Subscriber>,
}

/// The hub as the host and its transport share it.
pub type SharedHub = Arc<Mutex<Hub>>;

impl Hub {
    /// Opens a stream's subscription: its id, which the stream drains by
    /// and closes.
    pub fn subscribe(&mut self, caller: &Caller, query: &EventsQuery) -> u64 {
        self.next += 1;
        self.open.insert(
            self.next,
            Subscriber {
                caller: caller.clone(),
                query: query.clone(),
                queue: VecDeque::new(),
            },
        );
        self.next
    }

    /// Closes a subscription; its queue goes with it.
    pub fn unsubscribe(&mut self, id: u64) {
        self.open.remove(&id);
    }

    /// What a subscription holds, in the order it was sent, taken.
    pub fn drain(&mut self, id: u64) -> Vec<HostSignal> {
        self.open
            .get_mut(&id)
            .map(|subscriber| subscriber.queue.drain(..).collect())
            .unwrap_or_default()
    }

    /// How many subscriptions are open.
    pub fn len(&self) -> usize {
        self.open.len()
    }

    /// Whether none is open.
    pub fn is_empty(&self) -> bool {
        self.open.is_empty()
    }

    /// Hands each signal to every subscription whose stream delivers it, as
    /// `delivers` answers for the stream's caller and filters.
    pub fn publish(
        &mut self,
        signals: &[HostSignal],
        delivers: impl Fn(&Caller, &EventsQuery, &HostSignal) -> bool,
    ) {
        for signal in signals {
            for subscriber in self.open.values_mut() {
                if !delivers(&subscriber.caller, &subscriber.query, signal) {
                    continue;
                }
                if subscriber.queue.len() >= SIGNAL_BUFFER {
                    subscriber.queue.pop_front();
                }
                subscriber.queue.push_back(signal.clone());
            }
        }
    }
}

/// A subscription a stream holds: closed when the stream drops it, the
/// client gone.
pub struct Subscription {
    hub: SharedHub,
    id: u64,
}

impl Subscription {
    /// Opens one on a shared hub.
    pub fn open(hub: &SharedHub, caller: &Caller, query: &EventsQuery) -> Self {
        let id = hub
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .subscribe(caller, query);
        Subscription {
            hub: Arc::clone(hub),
            id,
        }
    }

    /// What it holds, taken.
    pub fn drain(&self) -> Vec<HostSignal> {
        self.hub
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .drain(self.id)
    }
}

impl Drop for Subscription {
    fn drop(&mut self) {
        self.hub
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .unsubscribe(self.id);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use superschematic_rules_handler::events::After;

    fn signal(schema: &str, n: i64) -> HostSignal {
        HostSignal {
            namespace: "default".into(),
            schema: schema.into(),
            instance_id: "n1".into(),
            behavior: "Comments".into(),
            use_id: None,
            name: "typing".into(),
            data: json!({ "n": n }),
            from: "bob".into(),
            at: n,
        }
    }

    #[test]
    fn a_subscription_holds_what_its_stream_delivers_and_drops_the_oldest_past_its_buffer() {
        let hub: SharedHub = Arc::default();
        let query = EventsQuery {
            after: After::Head,
            schema: Some("Note".into()),
            instance_id: None,
            behaviors: Vec::new(),
            kinds: Vec::new(),
            limit: 100,
            stream: true,
            signals: true,
        };
        let alice = Caller::new("alice", Vec::<String>::new());
        let subscription = Subscription::open(&hub, &alice, &query);
        let sent: Vec<HostSignal> = (0..300).map(|n| signal("Note", n)).collect();
        hub.lock()
            .unwrap()
            .publish(&sent, |_, query, signal| query.keeps_signal(signal));
        hub.lock()
            .unwrap()
            .publish(&[signal("Task", 1)], |_, query, signal| {
                query.keeps_signal(signal)
            });
        let held = subscription.drain();
        assert_eq!(held.len(), SIGNAL_BUFFER);
        assert_eq!(held[0].at, 300 - SIGNAL_BUFFER as i64);
        assert!(subscription.drain().is_empty());
        assert_eq!(hub.lock().unwrap().len(), 1);
        drop(subscription);
        assert!(hub.lock().unwrap().is_empty());
    }
}
