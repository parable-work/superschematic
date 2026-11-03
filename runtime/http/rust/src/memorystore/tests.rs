use super::*;
use std::sync::atomic::AtomicI64;

/// A clock a test moves by hand, in milliseconds from the epoch.
#[derive(Clone, Default)]
struct TestClock(Arc<AtomicI64>);

impl TestClock {
    fn at(ms: i64) -> Self {
        Self(Arc::new(AtomicI64::new(ms)))
    }

    fn advance(&self, by: Duration) {
        self.0
            .fetch_add(i64::try_from(by.as_millis()).unwrap(), Ordering::SeqCst);
    }

    fn store(&self, name: &str) -> InProcessMemoryStore {
        let clock = self.clone();
        InProcessMemoryStore::with_clock(name, move || {
            SystemTime::UNIX_EPOCH
                + Duration::from_millis(u64::try_from(clock.0.load(Ordering::SeqCst)).unwrap())
        })
    }
}

#[tokio::test]
async fn a_counter_counts_until_its_ttl_from_the_first_increment() {
    let clock = TestClock::at(1_000_000);
    let store = clock.store("shop-cache");
    let key = Key::new("views", "views:a", Duration::from_secs(10));
    assert_eq!(key.count(&store).await, Ok(0));
    assert_eq!(key.increment(&store).await, Ok(1));
    clock.advance(Duration::from_secs(6));
    assert_eq!(key.increment(&store).await, Ok(2));
    assert_eq!(key.count(&store).await, Ok(2));
    // A later increment keeps the first one's expiry.
    clock.advance(Duration::from_secs(4));
    assert_eq!(key.count(&store).await, Ok(0));
    assert_eq!(key.increment(&store).await, Ok(1));
}

#[tokio::test]
async fn one_claim_wins_until_it_expires_or_is_given_back() {
    let clock = TestClock::at(0);
    let store = clock.store("shop-cache");
    let key = Key::new("receipt-sent", "receipt-sent:o1", Duration::from_secs(600));
    assert_eq!(key.claim(&store).await, Ok(true));
    assert_eq!(key.claim(&store).await, Ok(false));
    key.delete(&store).await.unwrap();
    assert_eq!(key.claim(&store).await, Ok(true));
    clock.advance(Duration::from_secs(599));
    assert_eq!(key.claim(&store).await, Ok(false));
    clock.advance(Duration::from_secs(1));
    assert_eq!(key.claim(&store).await, Ok(true));
}

#[tokio::test]
async fn a_ttl_out_of_range_is_refused() {
    let store = InProcessMemoryStore::new("s");
    let r = Ref::new("k", "k");
    assert!(matches!(
        store.increment(&r, Duration::ZERO).await,
        Err(MemoryStoreError::Ttl(_))
    ));
    assert!(matches!(
        store.claim(&r, MAX_TTL + Duration::from_millis(1)).await,
        Err(MemoryStoreError::Ttl(_))
    ));
    assert!(key_ttl("views", Duration::from_secs(10), Duration::from_secs(5)).is_err());
    assert_eq!(
        key_ttl("views", Duration::from_micros(1500), Duration::from_secs(5)),
        Ok(Duration::from_millis(1))
    );
}

#[tokio::test]
async fn names_carry_the_prefix_so_two_stores_do_not_meet() {
    let clock = TestClock::at(0);
    let a = clock.store("a");
    let key = Key::new("k", "k", Duration::from_secs(1));
    key.increment(&a).await.unwrap();
    assert_eq!(a.full_name(key.reference()), "a:k");
}

#[tokio::test]
async fn a_channel_delivers_to_each_subscription_until_it_stops() {
    let store = InProcessMemoryStore::new("shop-cache");
    let channel: Channel<Vec<i64>> = Channel::new("samples", "samples:p");
    let got = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::clone(&got);
    let first = channel
        .subscribe(&store, move |m| seen.lock().unwrap().push(m))
        .await
        .unwrap();
    let count = Arc::new(AtomicI64::new(0));
    let counted = Arc::clone(&count);
    let second = channel
        .subscribe(&store, move |_| {
            counted.fetch_add(1, Ordering::SeqCst);
        })
        .await
        .unwrap();
    channel.publish(&store, &vec![1, 2]).await.unwrap();
    // A message that does not decode is dropped.
    store
        .publish(channel.reference(), b"not json")
        .await
        .unwrap();
    first.unsubscribe();
    channel.publish(&store, &vec![3]).await.unwrap();
    assert_eq!(*got.lock().unwrap(), vec![vec![1, 2]]);
    assert_eq!(count.load(Ordering::SeqCst), 2);
    drop(second);
    assert!(store.state().channels.is_empty());
}

#[tokio::test]
async fn a_signal_is_any_message_and_encodes_as_an_empty_object() {
    let store = InProcessMemoryStore::new("s");
    let channel = Channel::signal("stock-changed", "stock-changed:p");
    let count = Arc::new(AtomicI64::new(0));
    let counted = Arc::clone(&count);
    let _sub = channel
        .subscribe(&store, move |Signal| {
            counted.fetch_add(1, Ordering::SeqCst);
        })
        .await
        .unwrap();
    channel.publish(&store, &Signal).await.unwrap();
    store.publish(channel.reference(), b"\x00").await.unwrap();
    assert_eq!(count.load(Ordering::SeqCst), 2);
    assert_eq!(serde_json::to_string(&Signal).unwrap(), "{}");
    assert_eq!(
        serde_json::from_str::<Signal>(r#"{"a":[1]}"#).unwrap(),
        Signal
    );
}

#[tokio::test]
async fn a_message_over_64_kib_is_refused() {
    let store = InProcessMemoryStore::new("s");
    let channel: Channel<String> = Channel::new("big", "big");
    let message = "x".repeat(MAX_MESSAGE_BYTES);
    assert!(matches!(
        channel.publish(&store, &message).await,
        Err(MemoryStoreError::TooLarge(n)) if n == MAX_MESSAGE_BYTES + 2
    ));
}

#[tokio::test]
async fn a_closed_or_disabled_store_is_unavailable() {
    let store = InProcessMemoryStore::new("s");
    store.close().await;
    assert!(!store.available());
    let key = Key::new("k", "k", Duration::from_secs(1));
    assert_eq!(
        key.increment(&store).await,
        Err(MemoryStoreError::Unavailable)
    );
    let disabled = DisabledMemoryStore::new("s");
    assert!(!disabled.available());
    assert_eq!(disabled.name(), "s");
    assert_eq!(
        key.claim(&disabled).await,
        Err(MemoryStoreError::Unavailable)
    );
    assert_eq!(
        Channel::signal("c", "c").publish(&disabled, &Signal).await,
        Err(MemoryStoreError::Unavailable)
    );
    assert!(Channel::signal("c", "c")
        .subscribe(&disabled, |_| {})
        .await
        .is_err());
}

#[test]
fn a_part_is_its_wire_string() {
    assert_eq!(part("a b").unwrap(), "a b");
    assert_eq!(part(&-42i64).unwrap(), "-42");
    assert_eq!(part(&true).unwrap(), "true");
    assert!(part(&1.5f64).is_err());
    assert!(part(&vec![1]).is_err());
}

#[test]
fn a_name_refuses_a_segment_without_naming_its_value() {
    let err = name("views", &["ok", "a:secret"]).unwrap_err();
    assert!(
        matches!(&err, MemoryStoreError::Name(m) if m.contains("part 1") && !m.contains("secret"))
    );
    assert!(prefix("shop-cache", Some("pr-7")).unwrap() == "shop-cache@pr-7");
    assert!(prefix("shop-cache", Some("a@b")).is_err());
}
