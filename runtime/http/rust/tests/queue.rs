//! The queue client (D65) on a real Postgres, the one
//! `SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL` names (the tests skip
//! without it). Each test makes a schema of its own and creates in it the
//! tables of `testdata/queue/create.sql`, a copy of sqlgen's golden DDL of
//! the fixture-queue-db schema, and reads the descriptors of its queues
//! from `order_placed.json` and `ping.json`, tsgen's golden `queues.ts`
//! for the same schema as JSON, which a Rust types crate's `queues.rs`
//! holds: `OrderPlaced`'s backoff doubles from a minute up to 200 s and
//! keeps a done message an hour, and `Ping` keeps none. The worker loop
//! runs over the same queues at the end.

use std::collections::BTreeSet;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::{json, Value};
use superschematic_http_runtime::postgres::{connect, Pool};
use superschematic_http_runtime::queue::{
    enqueue, Claim, PostgresQueue, QueueDefinition, QueueError, CLAIM_LOST,
};
use superschematic_http_runtime::stackconfig::Database;
use superschematic_http_runtime::worker::{run_worker, Options};
use superschematic_http_runtime::{BoxError, Shutdown};

const URL_ENV: &str = "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL";

const LEASE: Duration = Duration::from_secs(60);

fn testdata(name: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/testdata/queue")
        .join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()))
}

fn order_placed() -> QueueDefinition {
    QueueDefinition::parse(&testdata("order_placed.json")).unwrap()
}

fn ping() -> QueueDefinition {
    QueueDefinition::parse(&testdata("ping.json")).unwrap()
}

/// A schema of the test's own, holding the fixture's tables, and a pool
/// whose connections search it first.
struct Db {
    admin: Pool,
    pool: Pool,
    schema: String,
}

impl Db {
    async fn open() -> Option<Db> {
        let url = std::env::var(URL_ENV).ok().filter(|url| !url.is_empty());
        let Some(url) = url else {
            eprintln!("skipped: {URL_ENV} is unset");
            return None;
        };
        let schema = format!("queue_test_{}", uuid::Uuid::new_v4().simple());
        let admin = connect(&Database::Url(url.clone())).unwrap();
        admin
            .get()
            .await
            .unwrap()
            .batch_execute(&format!("CREATE SCHEMA {schema}"))
            .await
            .unwrap();
        let separator = if url.contains('?') { '&' } else { '?' };
        let scoped = format!("{url}{separator}options=-csearch_path%3D{schema}%2Cpublic");
        let pool = connect(&Database::Url(scoped)).unwrap();
        // gen_random_uuid is built in from Postgres 13; the extension the
        // DDL creates for older ones is the database's, which tests that
        // run at once must not race to create.
        let ddl: String = testdata("create.sql")
            .lines()
            .filter(|line| !line.starts_with("CREATE EXTENSION"))
            .map(|line| format!("{line}\n"))
            .collect();
        pool.get().await.unwrap().batch_execute(&ddl).await.unwrap();
        Some(Db {
            admin,
            pool,
            schema,
        })
    }

    async fn close(self) {
        self.pool.close();
        let client = self.admin.get().await.unwrap();
        client
            .batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema))
            .await
            .unwrap();
    }

    async fn execute(&self, sql: &str) {
        self.pool
            .get()
            .await
            .unwrap()
            .batch_execute(sql)
            .await
            .unwrap();
    }

    /// A message's state, attempts and last error.
    async fn row(&self, table: &str, id: &str) -> (String, i32, Option<String>) {
        let row = self
            .pool
            .get()
            .await
            .unwrap()
            .query_one(
                &format!(
                    "SELECT state, attempts, last_error FROM {table} WHERE id = $1::text::uuid"
                ),
                &[&id],
            )
            .await
            .unwrap();
        (row.get(0), row.get(1), row.get(2))
    }

    /// Seconds from now until a message's claim expires.
    async fn expires_in(&self, table: &str, id: &str) -> f64 {
        self.pool
            .get()
            .await
            .unwrap()
            .query_one(
                &format!(
                    "SELECT extract(epoch FROM claim_expires_at - now())::float8 FROM {table} WHERE id = $1::text::uuid"
                ),
                &[&id],
            )
            .await
            .unwrap()
            .get(0)
    }

    /// Waits until `n` messages of `table` are as `condition` says, for up
    /// to ten seconds.
    async fn until(&self, table: &str, condition: &str, n: i64) {
        let start = Instant::now();
        let sql = format!("SELECT count(*) FROM {table} WHERE {condition}");
        loop {
            let held: i64 = self
                .pool
                .get()
                .await
                .unwrap()
                .query_one(&sql, &[])
                .await
                .unwrap()
                .get(0);
            if held == n {
                return;
            }
            assert!(
                start.elapsed() < Duration::from_secs(10),
                "{held} messages of {table} are as {condition} says, not {n}"
            );
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    }

    async fn enqueue(&self, queue: &QueueDefinition, message: &(impl Serialize + Sync)) {
        let client = self.pool.get().await.unwrap();
        enqueue(&client, queue, message).await.unwrap();
    }
}

/// `Identity.UUID` as superscalar's Rust type holds it: base62 on the wire,
/// read in either form.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Base62(uuid::Uuid);

const ALPHABET: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

impl Base62 {
    fn encode(self) -> String {
        let mut number = self.0.as_u128();
        let mut digits = Vec::new();
        while number > 0 {
            digits.push(ALPHABET[usize::try_from(number % 62).unwrap()]);
            number /= 62;
        }
        digits.reverse();
        String::from_utf8(digits).unwrap()
    }
}

impl Serialize for Base62 {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.encode())
    }
}

impl<'de> Deserialize<'de> for Base62 {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let text = String::deserialize(deserializer)?;
        if let Ok(uuid) = uuid::Uuid::parse_str(&text) {
            return Ok(Base62(uuid));
        }
        let mut number: u128 = 0;
        for c in text.bytes() {
            let digit = ALPHABET
                .iter()
                .position(|d| *d == c)
                .ok_or_else(|| serde::de::Error::custom(format!("{text} is no UUID")))?;
            number = number * 62 + u128::try_from(digit).unwrap();
        }
        Ok(Base62(uuid::Uuid::from_u128(number)))
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct Address {
    line1: String,
    city: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum Priority {
    Low,
    High,
}

/// The fixture's `OrderPlaced`, as rustgen writes a type.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct OrderPlaced {
    #[serde(rename = "orderId")]
    order_id: Base62,
    #[serde(rename = "placedAt")]
    placed_at: String,
    priority: Priority,
    quantity: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    note: Option<String>,
    #[serde(rename = "shipTo")]
    ship_to: Address,
    tags: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct Ping {
    #[serde(rename = "sentAt", default, skip_serializing_if = "Option::is_none")]
    sent_at: Option<String>,
}

fn an_order(n: i64) -> OrderPlaced {
    OrderPlaced {
        order_id: Base62(uuid::Uuid::new_v4()),
        placed_at: "2026-10-14T12:00:00.5Z".to_string(),
        priority: Priority::High,
        quantity: n,
        note: Some("leave it at the door".to_string()),
        ship_to: Address {
            line1: "1 Main St".to_string(),
            city: "Springfield".to_string(),
        },
        tags: vec!["gift".to_string()],
    }
}

fn queue<M: serde::de::DeserializeOwned>(db: &Db, definition: QueueDefinition) -> PostgresQueue<M> {
    PostgresQueue::new(db.pool.clone(), definition)
}

#[tokio::test]
async fn a_message_commits_with_its_transaction_and_reads_back_as_it_was_written() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    let orders = queue::<OrderPlaced>(&db, def.clone());

    let mut client = db.pool.get().await.unwrap();
    let tx = client.transaction().await.unwrap();
    enqueue(&tx, &def, &an_order(1)).await.unwrap();
    tx.rollback().await.unwrap();
    assert!(
        orders.claim(10, LEASE).await.unwrap().is_empty(),
        "rolled back"
    );

    let order = an_order(9_007_199_254_740_993);
    let tx = client.transaction().await.unwrap();
    enqueue(&tx, &def, &order).await.unwrap();
    assert!(
        orders.claim(10, LEASE).await.unwrap().is_empty(),
        "not yet committed"
    );
    tx.commit().await.unwrap();
    let claims = orders.claim(10, LEASE).await.unwrap();
    assert_eq!(claims.len(), 1);
    let Claim {
        id,
        token,
        attempt,
        message,
    } = &claims[0];
    assert_eq!(*attempt, 1);
    assert_eq!(*message, order);
    assert!(uuid::Uuid::parse_str(id).is_ok() && uuid::Uuid::parse_str(token).is_ok());

    // A message the Go ORM's way: an unset enum takes its default, a null
    // member its column's, and each value crosses as it is.
    let uuid = uuid::Uuid::new_v4();
    let written = json!({
        "orderId": uuid.hyphenated().to_string(),
        "placedAt": "2026-10-14T14:00:00.123456+02:00",
        "priority": "",
        "quantity": -4,
        "note": null,
        "shipTo": { "line1": "2 \"Quoted\" Rd", "city": "Back\\slash" },
        "tags": ["a", "b \"c\"", "back\\slash", "x,y", "{}", "NULL", "", "ünïcode"],
    });
    db.enqueue(&def, &written).await;
    let claims = orders.claim(10, LEASE).await.unwrap();
    assert_eq!(claims.len(), 1);
    assert_eq!(
        claims[0].message,
        OrderPlaced {
            order_id: Base62(uuid),
            placed_at: "2026-10-14T12:00:00.123456Z".to_string(),
            priority: Priority::Low,
            quantity: -4,
            note: None,
            ship_to: Address {
                line1: "2 \"Quoted\" Rd".to_string(),
                city: "Back\\slash".to_string(),
            },
            tags: [
                "a",
                "b \"c\"",
                "back\\slash",
                "x,y",
                "{}",
                "NULL",
                "",
                "ünïcode"
            ]
            .map(str::to_string)
            .to_vec(),
        }
    );

    // A message that sets no column writes the table's defaults.
    let pings = queue::<Ping>(&db, ping());
    db.enqueue(&ping(), &Ping { sent_at: None }).await;
    let claims = pings.claim(10, LEASE).await.unwrap();
    assert_eq!(claims.len(), 1);
    assert_eq!(claims[0].message, Ping { sent_at: None });
    db.close().await;
}

#[tokio::test]
async fn a_claim_takes_each_due_message_once_up_to_n() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    for n in 0..5 {
        db.enqueue(&def, &an_order(n)).await;
    }
    let (a, b) = (
        queue::<OrderPlaced>(&db, def.clone()),
        queue::<OrderPlaced>(&db, def),
    );
    assert!(a.claim(0, LEASE).await.unwrap().is_empty());
    let (first, second) = tokio::join!(a.claim(3, LEASE), b.claim(3, LEASE));
    let (first, second) = (first.unwrap(), second.unwrap());
    let mut sizes = [first.len(), second.len()];
    sizes.sort_unstable();
    assert_eq!(sizes, [2, 3]);
    let ids: BTreeSet<_> = first.iter().chain(&second).map(|c| c.id.clone()).collect();
    assert_eq!(ids.len(), 5, "no message is claimed twice");
    assert!(
        a.claim(3, LEASE).await.unwrap().is_empty(),
        "each is claimed"
    );
    db.close().await;
}

#[tokio::test]
async fn a_completed_message_is_done_and_its_token_claims_it_no_more() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    db.enqueue(orders.definition(), &an_order(1)).await;
    let claim = orders.claim(1, LEASE).await.unwrap().remove(0);
    orders.complete(&claim.id, &claim.token).await.unwrap();
    assert_eq!(
        db.row("order_placed_queue", &claim.id).await,
        ("done".into(), 1, None)
    );

    let lost = orders.complete(&claim.id, &claim.token).await.unwrap_err();
    assert_eq!(
        lost,
        QueueError::ClaimLost(format!(
            "complete message {} on order_placed_queue: {CLAIM_LOST}",
            claim.id
        ))
    );
    assert!(orders
        .extend(&claim.id, &claim.token, LEASE)
        .await
        .unwrap_err()
        .is_claim_lost());
    assert!(orders
        .release(&claim.id, &claim.token)
        .await
        .unwrap_err()
        .is_claim_lost());
    let lost = orders
        .fail(&claim.id, &claim.token, "late")
        .await
        .unwrap_err();
    assert_eq!(
        lost.to_string(),
        format!(
            "fail message {} on order_placed_queue: {CLAIM_LOST}",
            claim.id
        )
    );
    assert!(orders.claim(1, LEASE).await.unwrap().is_empty());
    db.close().await;
}

#[tokio::test]
async fn a_failed_message_is_due_after_its_backoff_until_its_retries_are_spent() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    db.enqueue(orders.definition(), &an_order(1)).await;
    let claim = orders.claim(1, LEASE).await.unwrap().remove(0);
    assert!(!orders
        .fail(&claim.id, &claim.token, "out of stock")
        .await
        .unwrap());
    assert_eq!(
        db.row("order_placed_queue", &claim.id).await,
        ("ready".into(), 1, Some("out of stock".into()))
    );
    assert!(
        orders.claim(1, LEASE).await.unwrap().is_empty(),
        "due after a minute's backoff"
    );

    // With no backoff and one retry, the second failure is the last.
    let mut def = order_placed();
    def.retries = 1;
    def.backoff_seconds = 0;
    let quick = queue::<OrderPlaced>(&db, def);
    db.execute("UPDATE order_placed_queue SET due_at = now()")
        .await;
    let claim = quick.claim(1, LEASE).await.unwrap().remove(0);
    assert_eq!(claim.attempt, 2);
    assert!(
        quick
            .fail(&claim.id, &claim.token, "still out")
            .await
            .unwrap(),
        "dead"
    );
    assert_eq!(
        db.row("order_placed_queue", &claim.id).await,
        ("dead".into(), 2, Some("still out".into()))
    );
    assert!(
        quick.claim(1, LEASE).await.unwrap().is_empty(),
        "a dead message is never due"
    );
    db.close().await;
}

/// Seconds from now until a message is due.
async fn due_in(db: &Db, id: &str) -> f64 {
    db.pool
        .get()
        .await
        .unwrap()
        .query_one(
            "SELECT extract(epoch FROM due_at - now())::float8 FROM order_placed_queue WHERE id = $1::text::uuid",
            &[&id],
        )
        .await
        .unwrap()
        .get(0)
}

#[tokio::test]
async fn each_failure_doubles_the_backoff_up_to_the_maximum() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    assert_eq!(
        (
            orders.definition().backoff(),
            orders.definition().max_backoff()
        ),
        (Duration::from_secs(60), Duration::from_secs(200))
    );
    db.enqueue(orders.definition(), &an_order(1)).await;
    // Attempt 1 waits the backoff, 2 twice it, and 3 the maximum, under
    // four times it.
    for (attempt, wait) in [(1, 60.0), (2, 120.0), (3, 200.0)] {
        db.execute("UPDATE order_placed_queue SET due_at = now() WHERE state = 'ready'")
            .await;
        let claim = orders.claim(1, LEASE).await.unwrap().remove(0);
        assert_eq!(claim.attempt, attempt);
        assert!(!orders.fail(&claim.id, &claim.token, "later").await.unwrap());
        let left = due_in(&db, &claim.id).await;
        assert!(
            (wait - 5.0..=wait).contains(&left),
            "attempt {attempt} is due in {left}s, want about {wait}s"
        );
    }
    db.close().await;
}

/// Marks a message done `age` ago, as a complete that long ago left it.
async fn done_ago(db: &Db, table: &str, id: &str, age: &str) {
    db.pool
        .get()
        .await
        .unwrap()
        .execute(
            &format!("UPDATE {table} SET state = 'done', claim_token = NULL, claim_expires_at = NULL, updated_at = now() - $2::text::interval WHERE id = $1::text::uuid"),
            &[&id, &age],
        )
        .await
        .unwrap();
}

#[tokio::test]
async fn a_prune_deletes_a_batch_of_messages_done_past_the_retention_and_no_dead_one() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    assert_eq!(orders.definition().retain(), Duration::from_secs(3600));
    for n in 0..5 {
        db.enqueue(orders.definition(), &an_order(n)).await;
    }
    let claims = orders.claim(5, LEASE).await.unwrap();
    let ids: Vec<&str> = claims.iter().map(|c| c.id.as_str()).collect();
    // Three done past the hour's retention, one within it, one dead long
    // since.
    for id in &ids[..3] {
        done_ago(&db, "order_placed_queue", id, "2 hours").await;
    }
    orders.complete(ids[3], &claims[3].token).await.unwrap();
    db.execute(&format!(
        "UPDATE order_placed_queue SET state = 'dead', updated_at = now() - interval '30 days' WHERE id = '{}'",
        ids[4]
    ))
    .await;
    assert_eq!(orders.prune(0).await.unwrap(), 0);
    assert_eq!(orders.prune(2).await.unwrap(), 2, "a batch of two");
    assert_eq!(orders.prune(2).await.unwrap(), 1, "the third");
    assert_eq!(orders.prune(2).await.unwrap(), 0);
    db.until("order_placed_queue", "true", 2).await;
    assert_eq!(db.row("order_placed_queue", ids[3]).await.0, "done");
    assert_eq!(db.row("order_placed_queue", ids[4]).await.0, "dead");
    db.close().await;
}

#[tokio::test]
async fn a_queue_that_keeps_no_done_message_deletes_it_as_it_completes() {
    let Some(db) = Db::open().await else { return };
    let pings = queue::<Ping>(&db, ping());
    assert_eq!(pings.definition().retain(), Duration::ZERO);
    db.enqueue(pings.definition(), &Ping { sent_at: None })
        .await;
    let claim = pings.claim(1, LEASE).await.unwrap().remove(0);
    pings.complete(&claim.id, &claim.token).await.unwrap();
    db.until("ping_queue", "true", 0).await;
    assert!(pings
        .complete(&claim.id, &claim.token)
        .await
        .unwrap_err()
        .is_claim_lost());
    db.close().await;
}

#[tokio::test]
async fn an_expired_claim_is_claimed_again_and_the_first_token_is_lost() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    db.enqueue(orders.definition(), &an_order(1)).await;
    let first = orders.claim(1, LEASE).await.unwrap().remove(0);
    assert!(orders.claim(1, LEASE).await.unwrap().is_empty(), "claimed");
    db.execute("UPDATE order_placed_queue SET claim_expires_at = now() - interval '1 second'")
        .await;
    let second = orders.claim(1, LEASE).await.unwrap().remove(0);
    assert_eq!((second.id.as_str(), second.attempt), (first.id.as_str(), 2));
    assert_ne!(second.token, first.token);
    for lost in [
        orders.complete(&first.id, &first.token).await,
        orders.extend(&first.id, &first.token, LEASE).await,
        orders.release(&first.id, &first.token).await,
        orders
            .fail(&first.id, &first.token, "late")
            .await
            .map(|_| ()),
    ] {
        assert!(lost.unwrap_err().is_claim_lost());
    }
    orders.complete(&second.id, &second.token).await.unwrap();
    db.close().await;
}

#[tokio::test]
async fn a_message_whose_last_attempt_expired_is_dead_before_a_claim() {
    let Some(db) = Db::open().await else { return };
    let mut def = order_placed();
    def.retries = 0;
    let orders = queue::<OrderPlaced>(&db, def);
    db.enqueue(orders.definition(), &an_order(1)).await;
    let claim = orders.claim(1, LEASE).await.unwrap().remove(0);
    db.execute("UPDATE order_placed_queue SET claim_expires_at = now() - interval '1 second'")
        .await;
    assert!(orders.claim(1, LEASE).await.unwrap().is_empty());
    assert_eq!(
        db.row("order_placed_queue", &claim.id).await,
        (
            "dead".into(),
            1,
            Some("the claim of its last attempt expired before its handler finished".into())
        )
    );
    db.close().await;
}

#[tokio::test]
async fn a_released_message_is_due_at_once_its_attempt_uncounted() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    db.enqueue(orders.definition(), &an_order(1)).await;
    let claim = orders.claim(1, LEASE).await.unwrap().remove(0);
    orders.release(&claim.id, &claim.token).await.unwrap();
    assert_eq!(
        db.row("order_placed_queue", &claim.id).await,
        ("ready".into(), 0, None)
    );
    let again = orders.claim(1, LEASE).await.unwrap().remove(0);
    assert_eq!((again.id, again.attempt), (claim.id, 1));
    db.close().await;
}

#[tokio::test]
async fn an_extension_holds_the_claim_for_the_lease_from_now() {
    let Some(db) = Db::open().await else { return };
    let orders = queue::<OrderPlaced>(&db, order_placed());
    db.enqueue(orders.definition(), &an_order(1)).await;
    let claim = orders
        .claim(1, Duration::from_millis(1500))
        .await
        .unwrap()
        .remove(0);
    let left = db.expires_in("order_placed_queue", &claim.id).await;
    assert!((0.0..=1.5).contains(&left), "{left}");
    orders
        .extend(&claim.id, &claim.token, Duration::from_secs(3600))
        .await
        .unwrap();
    let left = db.expires_in("order_placed_queue", &claim.id).await;
    assert!((3590.0..=3600.0).contains(&left), "{left}");
    db.close().await;
}

#[tokio::test]
async fn a_message_that_does_not_read_as_its_type_fails_the_claim() {
    let Some(db) = Db::open().await else { return };
    db.enqueue(&ping(), &json!({ "sentAt": "2026-10-14T12:00:00Z" }))
        .await;
    let wrong = queue::<OrderPlaced>(&db, ping());
    let err = wrong.claim(1, LEASE).await.unwrap_err();
    assert!(matches!(err, QueueError::Failed(_)), "{err}");
    assert!(
        err.to_string()
            .contains("of ping_queue: missing field `orderId`"),
        "{err}"
    );
    let client = db.pool.get().await.unwrap();
    let err = enqueue(&client, &ping(), &json!([1])).await.unwrap_err();
    drop(client);
    assert_eq!(
        err.to_string(),
        "enqueue on ping_queue: the message is [1], not an object"
    );
    db.close().await;
}

/// A queue whose columns are each type a message's field may be, its
/// statements the ping queue's with its table and columns.
const KINDS: &str = "
CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy');
CREATE DOMAIN short_text AS TEXT CHECK (length(VALUE) < 10);
CREATE TABLE kinds_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  flag BOOLEAN, small SMALLINT, whole INTEGER, big BIGINT, ratio DOUBLE PRECISION,
  single REAL, amount NUMERIC(12, 4), day DATE, at_time TIME, at TIMESTAMPTZ,
  naive TIMESTAMP, key UUID, address INET, network CIDR, doc JSONB, raw JSON,
  label VARCHAR(20), mood mood, short short_text, keys UUID[], counts BIGINT[],
  moods mood[],
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);
";

/// Each column of the kinds queue: its name, the message's member, and
/// whether it holds JSON or a list.
const KIND_COLUMNS: [(&str, bool, bool); 22] = [
    ("flag", false, false),
    ("small", false, false),
    ("whole", false, false),
    ("big", false, false),
    ("ratio", false, false),
    ("single", false, false),
    ("amount", false, false),
    ("day", false, false),
    ("at_time", false, false),
    ("at", false, false),
    ("naive", false, false),
    ("key", false, false),
    ("address", false, false),
    ("network", false, false),
    ("doc", true, false),
    ("raw", true, false),
    ("label", false, false),
    ("mood", false, false),
    ("short", false, false),
    ("keys", false, true),
    ("counts", false, true),
    ("moods", false, true),
];

fn kinds() -> QueueDefinition {
    let mut text = testdata("ping.json").replace("ping_queue", "kinds_queue");
    let returned: Vec<String> = KIND_COLUMNS
        .iter()
        .map(|(c, _, _)| format!("q.{c}"))
        .collect();
    text = text.replace("q.sent_at", &returned.join(", "));
    let mut def = QueueDefinition::parse(&text).unwrap();
    let fields: Vec<Value> = KIND_COLUMNS
        .iter()
        .map(|(c, json, list)| json!({ "name": c, "column": c, "json": json, "list": list }))
        .collect();
    def.fields = serde_json::from_value(Value::Array(fields)).unwrap();
    def
}

#[tokio::test]
async fn a_column_of_each_type_reads_back_as_the_value_written() {
    let Some(db) = Db::open().await else { return };
    db.execute(KINDS).await;
    let def = kinds();
    let key = uuid::Uuid::new_v4();
    let written = json!({
        "flag": true, "small": 7, "whole": -3, "big": 9_007_199_254_740_993_i64,
        "ratio": 0.25, "single": 1.5, "amount": 12.34, "day": "2026-10-14",
        "at_time": "13:05:09.25", "at": "2026-10-14T12:00:00.000001Z",
        "naive": "2026-10-14T12:00:00", "key": Base62(key), "address": "10.0.0.1",
        "network": "10.0.0.0/8", "doc": { "a": [1, "x", null] }, "raw": "text",
        "label": "abc", "mood": "ok", "short": "tiny", "keys": [Base62(key), null],
        "counts": [1, 2], "moods": ["sad", "happy"],
    });
    db.enqueue(&def, &written).await;
    let kinds = queue::<Value>(&db, def);
    let mut read = kinds.claim(1, LEASE).await.unwrap().remove(0).message;
    let amount = read.as_object_mut().unwrap().remove("amount").unwrap();
    assert_eq!(amount.as_f64(), Some(12.34), "a NUMERIC is its number");
    let mut want = written.clone();
    let members = want.as_object_mut().unwrap();
    members.remove("amount");
    members.insert("key".into(), json!(key.hyphenated().to_string()));
    members.insert("keys".into(), json!([key.hyphenated().to_string(), null]));
    assert_eq!(read, want);

    // A null member reads back absent.
    let empty = queue::<Value>(&db, kinds.definition().clone());
    db.enqueue(kinds.definition(), &json!({ "flag": null }))
        .await;
    assert_eq!(
        empty.claim(1, LEASE).await.unwrap().remove(0).message,
        json!({})
    );
    db.close().await;
}

/// A worker's options for a test: a short idle wait.
fn quick(concurrency: usize, grace: Duration) -> Options {
    Options {
        concurrency,
        grace,
        idle: Duration::from_millis(50),
        ..Options::default()
    }
}

#[tokio::test]
async fn a_worker_handles_every_message_no_more_than_its_concurrency_at_a_time() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    for n in 0..6 {
        db.enqueue(&def, &an_order(n)).await;
    }
    let (now, most) = (Arc::new(AtomicUsize::new(0)), Arc::new(AtomicUsize::new(0)));
    let (counting, peak) = (Arc::clone(&now), Arc::clone(&most));
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue::<OrderPlaced>(&db, def),
        move |_order: OrderPlaced| {
            let (now, most) = (Arc::clone(&counting), Arc::clone(&peak));
            async move {
                most.fetch_max(now.fetch_add(1, Ordering::SeqCst) + 1, Ordering::SeqCst);
                tokio::time::sleep(Duration::from_millis(100)).await;
                now.fetch_sub(1, Ordering::SeqCst);
                Ok::<(), BoxError>(())
            }
        },
        quick(2, Duration::from_secs(1)),
        shutdown.clone(),
    ));
    db.until("order_placed_queue", "state = 'done'", 6).await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(most.load(Ordering::SeqCst), 2);
    db.close().await;
}

#[tokio::test]
async fn a_handlers_error_or_panic_is_recorded_as_its_messages_failure() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    db.enqueue(&def, &an_order(1)).await;
    db.enqueue(&def, &an_order(2)).await;
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue::<OrderPlaced>(&db, def),
        |order: OrderPlaced| async move {
            if order.quantity == 1 {
                panic!("order {} has no lines", order.quantity);
            }
            Err::<(), BoxError>("the warehouse is closed".into())
        },
        quick(2, Duration::from_secs(1)),
        shutdown.clone(),
    ));
    db.until("order_placed_queue", "state = 'ready' AND attempts = 1", 2)
        .await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    let client = db.pool.get().await.unwrap();
    let rows = client
        .query(
            "SELECT quantity, attempts, last_error, due_at > now() + interval '50 seconds' \
             FROM order_placed_queue ORDER BY quantity",
            &[],
        )
        .await
        .unwrap();
    let read: Vec<(i64, i32, String, bool)> = rows
        .iter()
        .map(|row| (row.get(0), row.get(1), row.get(2), row.get(3)))
        .collect();
    assert_eq!(
        read,
        [
            (1, 1, "panic: order 1 has no lines".to_string(), true),
            (2, 1, "the warehouse is closed".to_string(), true),
        ],
        "each retried after the minute's backoff"
    );
    drop(client);
    db.close().await;
}

#[tokio::test]
async fn a_long_handler_keeps_its_message_by_extending_its_claim() {
    let Some(db) = Db::open().await else { return };
    let mut def = order_placed();
    def.lease_seconds = 1;
    db.enqueue(&def, &an_order(1)).await;
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue::<OrderPlaced>(&db, def.clone()),
        |_order: OrderPlaced| async {
            tokio::time::sleep(Duration::from_millis(2500)).await;
            Ok::<(), BoxError>(())
        },
        quick(1, Duration::from_secs(1)),
        shutdown.clone(),
    ));
    db.until("order_placed_queue", "state = 'claimed'", 1).await;
    // Another worker finds nothing to claim while the claim is extended,
    // though the lease is a second.
    let other = queue::<OrderPlaced>(&db, def);
    let start = Instant::now();
    while start.elapsed() < Duration::from_millis(2000) {
        assert!(other.claim(1, LEASE).await.unwrap().is_empty());
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    db.until("order_placed_queue", "state = 'done'", 1).await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    let id: String = db
        .pool
        .get()
        .await
        .unwrap()
        .query_one("SELECT id::text FROM order_placed_queue", &[])
        .await
        .unwrap()
        .get(0);
    assert_eq!(
        db.row("order_placed_queue", &id).await,
        ("done".into(), 1, None)
    );
    db.close().await;
}

#[tokio::test]
async fn a_worker_prunes_the_done_messages_past_the_retention_as_it_starts() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    for n in 0..3 {
        db.enqueue(&def, &an_order(n)).await;
    }
    let orders = queue::<OrderPlaced>(&db, def.clone());
    let claims = orders.claim(3, LEASE).await.unwrap();
    for claim in &claims[..2] {
        done_ago(&db, "order_placed_queue", &claim.id, "1 day").await;
    }
    done_ago(&db, "order_placed_queue", &claims[2].id, "1 minute").await;
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue::<OrderPlaced>(&db, def),
        |_order: OrderPlaced| async { Ok::<(), BoxError>(()) },
        quick(1, Duration::from_secs(1)),
        shutdown.clone(),
    ));
    db.until("order_placed_queue", "true", 1).await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(
        db.row("order_placed_queue", &claims[2].id).await.0,
        "done",
        "within the retention"
    );
    db.close().await;
}

#[tokio::test]
async fn a_stopping_worker_releases_the_messages_its_handlers_did_not_finish() {
    let Some(db) = Db::open().await else { return };
    let def = order_placed();
    for n in 1..=3 {
        db.enqueue(&def, &an_order(n)).await;
    }
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue::<OrderPlaced>(&db, def),
        |order: OrderPlaced| async move {
            if order.quantity == 1 {
                return Ok::<(), BoxError>(());
            }
            std::future::pending::<()>().await;
            Ok(())
        },
        quick(3, Duration::from_millis(200)),
        shutdown.clone(),
    ));
    db.until("order_placed_queue", "state = 'done'", 1).await;
    db.until("order_placed_queue", "state = 'claimed'", 2).await;
    let stopping = Instant::now();
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert!(
        stopping.elapsed() >= Duration::from_millis(200),
        "the grace"
    );
    let client = db.pool.get().await.unwrap();
    let rows = client
        .query(
            "SELECT quantity, state, attempts, claim_token IS NULL FROM order_placed_queue ORDER BY quantity",
            &[],
        )
        .await
        .unwrap();
    let read: Vec<(i64, String, i32, bool)> = rows
        .iter()
        .map(|row| (row.get(0), row.get(1), row.get(2), row.get(3)))
        .collect();
    assert_eq!(
        read,
        [
            (1, "done".to_string(), 1, true),
            (2, "ready".to_string(), 0, true),
            (3, "ready".to_string(), 0, true),
        ],
        "released, due at once, their attempts uncounted"
    );
    drop(client);
    db.close().await;
}
