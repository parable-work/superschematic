//! fixture-comments-inbox-db end to end in the Rust repositories and
//! runtime (#713, D153, D154, D157), run by TestRustCommentsInboxEndToEnd:
//! tickets, some private to their owner, whose Comments emits its mentions
//! and replies, and the notifier's tables and dispatch queue. The generated
//! create.sql is applied in a schema of the test's own at
//! COMMENTS_TEST_DATABASE_URL, and the cases are the TypeScript
//! repositories' (tsgen's comments_inbox.test.ts). deliver() runs what a
//! behavior worker and the dispatch queue's worker run: each event the core
//! wrote to the ticket's event queue, handed to the notifier and spent in
//! one transaction, then each due message of the dispatch queue, handed to
//! Notifier::handle.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use runtime::orm::notify::{same_id, Notifier, RecipientRef};
use runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
use runtime::Principal;
use runtime::orm::{AccessCaller, Caller, Values, WriteOptions};
use runtime::queue::QueueDefinition;
use runtime::worker::WorkerQueue;
use runtime::BoxError;
use serde_json::{json, Value};
use types::orm::postgres::Repositories;

fn values(value: Value) -> Values {
    value.as_object().cloned().unwrap()
}

fn case(name: &str) {
    println!("case: {name}");
}

fn text(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

struct World {
    pool: deadpool_postgres::Pool,
    admin: tokio_postgres::Client,
    schema: String,
    notifier: Notifier,
    ann: String,
    ben: String,
    cal: String,
    /// A ticket of Ann's that everyone reads.
    open: String,
    /// A ticket of Ann's that she alone reads.
    closed: String,
}

async fn world() -> World {
    let url = std::env::var("COMMENTS_TEST_DATABASE_URL").expect("COMMENTS_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("comments_inbox_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(8).build().unwrap();
    let dispatch = QueueDefinition::parse(types::queues::NOTICE_WORK_QUEUE).unwrap();
    let notifier = Notifier::new(types::orm::postgres::tables(pool.clone()), dispatch).unwrap();
    let mut w = World {
        pool,
        admin,
        schema,
        notifier,
        ann: String::new(),
        ben: String::new(),
        cal: String::new(),
        open: String::new(),
        closed: String::new(),
    };
    let (ann, ben, cal, open, closed) = w
        .system(async |r| {
            let account = |email: &str| values(json!({ "email": email }));
            let ann = text(&r.untyped().table("Account")?.create_one(&account("ann@example.test")).await?, "id");
            let ben = text(&r.untyped().table("Account")?.create_one(&account("ben@example.test")).await?, "id");
            let cal = text(&r.untyped().table("Account")?.create_one(&account("cal@example.test")).await?, "id");
            let tickets = r.untyped().table("Ticket")?;
            let open = tickets.create_one(&values(json!({ "title": "Boiler pressure low", "owner": ann }))).await?;
            let closed = tickets
                .create_one(&values(json!({ "title": "Annual review", "owner": ann, "audience": "private" })))
                .await?;
            // The app's routes: a mention and a reply each reach the
            // subjects the event names (#713).
            let routes = r.untyped().table("NoticeRoute")?;
            routes
                .create_one(&values(json!({ "name": "Mentions", "eventKind": "comments.mentioned", "recipients": [{ "payload": "subjects" }] })))
                .await?;
            routes
                .create_one(&values(json!({ "name": "Replies", "eventKind": "comments.replied", "urgency": "digest", "recipients": [{ "payload": "subjects" }] })))
                .await?;
            Ok::<_, BoxError>((ann, ben, cal, text(&open, "id"), text(&closed, "id")))
        })
        .await
        .unwrap();
    w.ann = ann;
    w.ben = ben;
    w.cal = cal;
    w.open = open;
    w.closed = closed;
    w
}

impl World {
    /// Runs `work` over the system's repositories in a transaction it
    /// commits.
    async fn system<T>(&self, work: impl AsyncFnOnce(&Repositories<'_, deadpool_postgres::Transaction<'_>>) -> T) -> T {
        self.as_caller(None, work).await
    }

    /// Runs `work` over the repositories acting as `caller`, its subject
    /// the user it reads as, whose reads keep to what it may see (D156), in
    /// a transaction it commits.
    async fn as_caller<T>(
        &self,
        caller: Option<Caller>,
        work: impl AsyncFnOnce(&Repositories<'_, deadpool_postgres::Transaction<'_>>) -> T,
    ) -> T {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let repos = match caller {
            None => Repositories::new(&tx).unwrap().as_system(),
            Some(caller) => {
                let reader = Principal::new(caller.subject.clone(), Vec::<String>::new());
                Repositories::new(&tx)
                    .unwrap()
                    .acting(Some(caller.subject.clone()))
                    .with_access(AccessCaller::of(Some(&reader)))
                    .with_caller(caller)
            }
        };
        let out = work(&repos).await;
        drop(repos);
        tx.commit().await.unwrap();
        out
    }

    /// Runs `operation` of the ticket's Comments as `caller`.
    async fn call(&self, caller: Caller, ticket: &str, operation: &str, params: Value) -> Result<Value, runtime::orm::Error> {
        self.as_caller(Some(caller), async |r| {
            r.untyped().table("Ticket")?.operation("comments", operation, &json!(ticket), &params, &WriteOptions::default()).await
        })
        .await
    }

    async fn post(&self, user: &str, ticket: &str, params: Value) -> Value {
        self.call(as_user(user), ticket, "post", params).await.unwrap()
    }

    /// The caller's unread on the ticket, read through the core as it.
    async fn unread(&self, user: &str, ticket: &str) -> Result<Value, runtime::orm::Error> {
        self.as_caller(Some(as_user(user)), async |r| r.untyped().table("Ticket")?.view("comments", &json!(ticket)).await)
            .await
            .map(|view| view.get("unread").cloned().unwrap_or(Value::Null))
    }

    /// Hands every event the tickets' Comments emitted to the notifier,
    /// each in the transaction that spends it, then runs the dispatch
    /// queue until it is empty; returns the kinds handed.
    async fn deliver(&self) -> Vec<String> {
        let mut kinds = Vec::new();
        {
            let mut client = self.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            let events = tx
                .query("SELECT id::text AS id, row_key::text AS row_key, kind, payload::text AS payload FROM ticket_event_queue WHERE state = 'ready' ORDER BY created_at, id FOR UPDATE", &[])
                .await
                .unwrap();
            for event in &events {
                let (id, key, kind): (String, String, String) = (event.get("id"), event.get("row_key"), event.get("kind"));
                let payload: Option<Value> = event.get::<_, Option<String>>("payload").map(|text| serde_json::from_str(&text).unwrap());
                kinds.push(kind.clone());
                self.notifier
                    .from_behavior_event(&tx, "Ticket", "comments", &kind, &key, payload.as_ref())
                    .await
                    .unwrap();
                tx.execute("DELETE FROM ticket_event_queue WHERE id::text = $1", &[&id]).await.unwrap();
            }
            tx.commit().await.unwrap();
        }
        let queue = self.notifier.worker_queue();
        for _ in 0..50 {
            let claims = queue.claim(20, Duration::from_secs(30)).await.unwrap();
            if claims.is_empty() {
                return kinds;
            }
            for claim in claims {
                self.notifier.handle(claim.message).await.unwrap();
                queue.complete(&claim.id, &claim.token).await.unwrap();
            }
        }
        panic!("the dispatch queue did not drain");
    }

    /// The user's notices, oldest first: each one's kind, ticket and
    /// message.
    async fn notices_of(&self, user: &str) -> Vec<(String, String, i64)> {
        let mut notices = self.notifier.inbox(&RecipientRef::user(user), &[], None).await.unwrap();
        notices.sort_by_key(|n| n.get("reference").and_then(Value::as_i64).unwrap_or_default());
        notices
            .iter()
            .map(|n| (text(n, "eventKind"), same_id(&text(n, "subjectKey")), payload_of(n)["message"].as_i64().unwrap_or_default()))
            .collect()
    }

    async fn payloads_of(&self, user: &str) -> Vec<Value> {
        let mut notices = self.notifier.inbox(&RecipientRef::user(user), &[], None).await.unwrap();
        notices.sort_by_key(|n| n.get("reference").and_then(Value::as_i64).unwrap_or_default());
        notices.iter().map(payload_of).collect()
    }

    async fn drop(self) {
        let World { pool, admin, schema, .. } = self;
        pool.close();
        admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
    }
}

/// A notice's payload, the JSON its column holds.
fn payload_of(notice: &Values) -> Value {
    match notice.get("payload") {
        Some(Value::String(text)) => serde_json::from_str(text).unwrap_or(Value::Null),
        Some(other) => other.clone(),
        None => Value::Null,
    }
}

fn as_user(user: &str) -> Caller {
    Caller {
        subject: user.to_owned(),
        ..Caller::default()
    }
}

fn id_of(message: &Value) -> i64 {
    message["id"].as_i64().unwrap()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn comments_inbox() {
    if std::env::var("COMMENTS_TEST_DATABASE_URL").is_err() {
        panic!("set COMMENTS_TEST_DATABASE_URL");
    }

    {
        case("a mention reaches the mentioned principal's inbox, and a principal who cannot read the row is not notified");
        let w = world().await;
        let posted = w
            .post(&w.ann, &w.open, json!({ "body": "Can you look at the gauge?", "mentions": [w.ben, w.cal, w.ann] }))
            .await;
        // Ben may not read the closed ticket: mentioned there, he hears nothing.
        w.post(&w.ann, &w.closed, json!({ "body": "Notes for the review", "mentions": [w.ben] })).await;
        assert_eq!(w.deliver().await, ["mentioned", "mentioned"]);
        let want = vec![("comments.mentioned".to_owned(), same_id(&w.open), id_of(&posted))];
        assert_eq!(w.notices_of(&w.ben).await, want);
        assert_eq!(w.notices_of(&w.cal).await, want);
        assert!(w.notices_of(&w.ann).await.is_empty(), "the author is never among a message's subjects");
        let payload = w.payloads_of(&w.ben).await.remove(0);
        assert_eq!(payload["author"], json!(w.ann));
        assert_eq!(payload["subjects"], json!([w.ben, w.cal]));
        assert_eq!(payload["excerpt"], json!("Can you look at the gauge?"));
        let client = w.pool.get().await.unwrap();
        let held = client
            .query("SELECT subject_key FROM notice", &[])
            .await
            .unwrap()
            .iter()
            .filter(|row| same_id(&row.get::<_, String>(0)) == same_id(&w.closed))
            .count();
        assert_eq!(held, 0, "nothing records that Ben was skipped on the closed ticket");
        drop(client);
        w.drop().await;
    }

    {
        case("a reply reaches the thread's followers, a follower it mentions once, and never its author");
        let w = world().await;
        let top = w.post(&w.ben, &w.open, json!({ "body": "The pump hums." })).await;
        let second = w.post(&w.ann, &w.open, json!({ "body": "Since when?", "thread": id_of(&top) })).await;
        let third = w
            .post(&w.cal, &w.open, json!({ "body": "Since Monday.", "thread": id_of(&top), "mentions": [w.ann] }))
            .await;
        assert_eq!(w.deliver().await, ["replied", "mentioned", "replied"]);
        assert_eq!(
            w.notices_of(&w.ben).await,
            vec![
                ("comments.replied".to_owned(), same_id(&w.open), id_of(&second)),
                ("comments.replied".to_owned(), same_id(&w.open), id_of(&third)),
            ]
        );
        assert_eq!(w.notices_of(&w.ann).await, vec![("comments.mentioned".to_owned(), same_id(&w.open), id_of(&third))]);
        assert!(w.notices_of(&w.cal).await.is_empty());
        let notices = w.notifier.inbox(&RecipientRef::user(&w.ben), &[], None).await.unwrap();
        assert!(notices.iter().all(|n| text(n, "urgency") == "digest"), "{notices:?}");
        w.drop().await;
    }

    {
        case("markRead clears a reader's unread, and follow turns a thread's replies on and off");
        let w = world().await;
        let first = w.post(&w.ann, &w.open, json!({ "body": "Gauge reads 0.8 bar." })).await;
        // No marker yet: nothing counts as unread until Cal first reads.
        assert_eq!(w.unread(&w.cal, &w.open).await.unwrap(), json!(0));
        let read = w.call(as_user(&w.cal), &w.open, "markRead", json!({ "upTo": id_of(&first) })).await.unwrap();
        assert_eq!(read, json!({ "readUpTo": 1, "unread": 0 }));
        let second = w.post(&w.ann, &w.open, json!({ "body": "Refilled to 1.4." })).await;
        let third = w.post(&w.ann, &w.open, json!({ "body": "Holding." })).await;
        assert_eq!(w.unread(&w.cal, &w.open).await.unwrap(), json!(2));
        assert_eq!(w.unread(&w.ann, &w.open).await.unwrap(), json!(0), "Ann wrote them");
        let read = w.call(as_user(&w.cal), &w.open, "markRead", json!({ "upTo": id_of(&second) })).await.unwrap();
        assert_eq!(read, json!({ "readUpTo": 2, "unread": 1 }));
        let read = w.call(as_user(&w.cal), &w.open, "markRead", json!({ "upTo": id_of(&third) })).await.unwrap();
        assert_eq!(read, json!({ "readUpTo": 3, "unread": 0 }));
        assert_eq!(w.unread(&w.cal, &w.open).await.unwrap(), json!(0));
        // The view reads as its caller: Ben may not read the closed ticket.
        assert!(w.unread(&w.ben, &w.closed).await.is_err(), "Ben read the closed ticket's view");

        let on = w.call(as_user(&w.cal), &w.open, "follow", json!({ "thread": id_of(&first), "on": true })).await.unwrap();
        assert_eq!(on, json!({ "thread": id_of(&first), "following": true }));
        let reply = w.post(&w.ben, &w.open, json!({ "body": "Valve was shut.", "thread": id_of(&first) })).await;
        let off = w.call(as_user(&w.cal), &w.open, "follow", json!({ "thread": id_of(&first), "on": false })).await.unwrap();
        assert_eq!(off, json!({ "thread": id_of(&first), "following": false }));
        let later = w.post(&w.ben, &w.open, json!({ "body": "Opened it.", "thread": id_of(&first) })).await;
        w.deliver().await;
        assert_eq!(w.notices_of(&w.cal).await, vec![("comments.replied".to_owned(), same_id(&w.open), id_of(&reply))]);
        assert_eq!(
            w.notices_of(&w.ann).await,
            vec![
                ("comments.replied".to_owned(), same_id(&w.open), id_of(&reply)),
                ("comments.replied".to_owned(), same_id(&w.open), id_of(&later)),
            ]
        );
        w.drop().await;
    }

    {
        case("a message posted for a sender over a channel records its via and its for");
        let w = world().await;
        // An agent run writes in Ben's name a text Ben sent over SMS.
        let agent = Caller {
            subject: "run-1".to_owned(),
            kind: Some("agent".to_owned()),
            acting_for: Some(w.ben.clone()),
            channel: Some("sms".to_owned()),
            ..Caller::default()
        };
        let posted = w
            .call(agent, &w.open, "post", json!({ "body": "Running late, there by 4.", "mentions": [w.cal, w.ben] }))
            .await
            .unwrap();
        assert_eq!((posted["author"].clone(), posted["for"].clone(), posted["via"].clone()), (json!("run-1"), json!(w.ben), json!("sms")));
        let read = w.call(as_user(&w.cal), &w.open, "getMessage", json!({ "message": id_of(&posted) })).await.unwrap();
        assert_eq!((read["for"].clone(), read["via"].clone()), (json!(w.ben), json!("sms")));
        let client = w.pool.get().await.unwrap();
        let row = client.query_one("SELECT author, for_subject, via FROM ticket_comments_messages", &[]).await.unwrap();
        let stored: (String, String, String) = (row.get(0), row.get(1), row.get(2));
        assert_eq!(stored, ("run-1".to_owned(), w.ben.clone(), "sms".to_owned()));
        drop(client);
        // A call over the API names no channel.
        let plain = w.post(&w.ann, &w.open, json!({ "body": "Thanks." })).await;
        assert!(plain.get("via").is_none() && plain.get("for").is_none(), "{plain}");
        w.deliver().await;
        assert!(w.notices_of(&w.ben).await.is_empty(), "Ben, whom it was written for, hears nothing of it");
        let payload = w.payloads_of(&w.cal).await.remove(0);
        assert_eq!(payload["author"], json!("run-1"));
        assert_eq!(payload["forSubject"], json!(w.ben));
        assert_eq!(payload["via"], json!("sms"));
        assert_eq!(payload["subjects"], json!([w.cal]));
        w.drop().await;
    }
}
