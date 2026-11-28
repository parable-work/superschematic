//! fixture-notifications-db end to end in the Rust repositories and runtime
//! (D153, D191), run by TestRustNotificationsEndToEnd: tickets that raise
//! events, some named to a few readers, approvals asked of those who hold
//! their permission, the notifier's tables, its dispatch queue and the
//! channel identities replies resolve through. The generated create.sql is
//! applied in a schema of the test's own at NOTIFY_TEST_DATABASE_URL, and
//! the cases are the TypeScript repositories' (tsgen's
//! notifications.test.ts), through the runtime's orm::notify over the
//! generated repositories, with an SMS relay and a mail relay as stand-in
//! channel adapters (D159). drain() runs what the dispatch queue's worker
//! runs: each due message, handed to Notifier::handle.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use runtime::identity::{
    hash_password, verify_password, Argon2Params, Config, NewUser, PostgresPool, Scalars, Service, SqlStore, Store,
};
use runtime::orm::notify::{
    approval_replies, AddIdentity, Notifier, NotifierError, RaiseInput, RecipientRef, ReplyAction, ReplyContext,
    ReplyInput, PasswordReset,
};
use runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
use runtime::orm::rowfeatures::RowRef;
use runtime::orm::{FindOptions, OrderBy, Values, WriteOptions};
use runtime::providers::{
    providers_from, ChannelAdapter, ChannelHealth, ConfigValue, DeliveryState, DeliveryStatus, InboundMessage,
    OutboundMessage, ProviderDefinition, ProviderInstance, ProvidersOptions, RuntimeInterface, SendResult,
    WebhookRequest,
};
use runtime::queue::{PostgresQueue, QueueDefinition};
use runtime::worker::WorkerQueue;
use runtime::BoxError;
use serde_json::{json, Value};
use types::orm::postgres::Repositories;

const RELAY_TOKEN: &str = "relay-token";

const PHONE_ANN: &str = "+15550100001";
const PHONE_BEN: &str = "+15550100002";
const PHONE_CAL: &str = "+15550100003";
const EMAIL_ANN: &str = "ann@example.test";
const EMAIL_BEN: &str = "ben@example.test";
const EMAIL_CAL: &str = "cal@example.test";

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

/// What a stand-in relay was sent.
#[derive(Clone, Debug)]
struct Sent {
    to: String,
    subject: String,
    text: String,
    id: String,
}

/// The switches that make the relays fail.
#[derive(Default)]
struct RelayState {
    sms_down: bool,
    fail_to: BTreeSet<String>,
    n: u64,
}

#[derive(Clone, Default)]
struct Relays {
    sms: Arc<Mutex<Vec<Sent>>>,
    mail: Arc<Mutex<Vec<Sent>>>,
    state: Arc<Mutex<RelayState>>,
}

impl Relays {
    fn sms(&self) -> Vec<Sent> {
        self.sms.lock().unwrap().clone()
    }

    fn mail(&self) -> Vec<Sent> {
        self.mail.lock().unwrap().clone()
    }

    fn to(list: &[Sent]) -> Vec<String> {
        let mut to: Vec<String> = list.iter().map(|m| m.to.clone()).collect();
        to.sort();
        to
    }
}

/// A stand-in SMS relay: it records what it sends, fails a send to an
/// address it is told to, is down when told, and reads its callbacks'
/// statuses and inbound messages, holding each to the relay's token.
struct SmsRelay(Relays);

fn checked(request: &WebhookRequest) -> Result<Value, BoxError> {
    if request.headers.get("x-relay-token").map(String::as_str) != Some(RELAY_TOKEN) {
        return Err("the callback holds no relay token".into());
    }
    Ok(serde_json::from_str(&request.body)?)
}

fn at_of(value: &Value) -> SystemTime {
    let millis = value.as_u64().unwrap_or(0);
    UNIX_EPOCH + Duration::from_millis(millis)
}

#[async_trait]
impl ChannelAdapter for SmsRelay {
    fn kind(&self) -> &str {
        "sms"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let mut state = self.0.state.lock().unwrap();
        if state.fail_to.contains(&message.to) {
            return Err("the relay answered 500".into());
        }
        state.n += 1;
        let id = format!("sms-{}", state.n);
        self.0.sms.lock().unwrap().push(Sent {
            to: message.to.clone(),
            subject: String::new(),
            text: message.body.clone(),
            id: id.clone(),
        });
        Ok(SendResult {
            provider_message_id: id,
            state: DeliveryState::Queued,
        })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        let down = self.0.state.lock().unwrap().sms_down;
        Ok(ChannelHealth {
            ok: !down,
            detail: down.then(|| "the relay answered 503".to_owned()),
        })
    }

    async fn status(&self, request: &WebhookRequest) -> Result<Vec<DeliveryStatus>, BoxError> {
        let body = checked(request)?;
        let mut out = Vec::new();
        for event in body["events"].as_array().cloned().unwrap_or_default() {
            out.push(DeliveryStatus {
                provider_message_id: event["id"].as_str().unwrap_or_default().to_owned(),
                state: match event["status"].as_str() {
                    Some("delivered") => DeliveryState::Delivered,
                    Some("read") => DeliveryState::Read,
                    Some("failed") => DeliveryState::Failed,
                    _ => DeliveryState::Sent,
                },
                at: at_of(&event["at"]),
                detail: None,
            });
        }
        Ok(out)
    }

    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        let body = checked(request)?;
        let mut out = Vec::new();
        for message in body["messages"].as_array().cloned().unwrap_or_default() {
            out.push(InboundMessage {
                from: message["from"].as_str().unwrap_or_default().to_owned(),
                body: message["text"].as_str().unwrap_or_default().to_owned(),
                received_at: at_of(&message["at"]),
                provider_message_id: message["id"].as_str().unwrap_or_default().to_owned(),
                in_reply_to: None,
                message_id: None,
                references: Vec::new(),
                to: Vec::new(),
                subject: None,
                authentication: None,
                attachments: Vec::new(),
                opt_out: None,
            });
        }
        Ok(out)
    }
}

/// A stand-in mail relay: it records what it sends, and reports no
/// statuses.
struct MailRelay(Relays);

#[async_trait]
impl ChannelAdapter for MailRelay {
    fn kind(&self) -> &str {
        "email"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let mut state = self.0.state.lock().unwrap();
        state.n += 1;
        let id = format!("mail-{}", state.n);
        self.0.mail.lock().unwrap().push(Sent {
            to: message.to.clone(),
            subject: message.subject.clone().unwrap_or_default(),
            text: message.body.clone(),
            id: id.clone(),
        });
        Ok(SendResult {
            provider_message_id: id,
            state: DeliveryState::Sent,
        })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        Ok(ChannelHealth { ok: true, detail: None })
    }
}

/// The reply action of `done`: marks the ticket done as the replier, and
/// puts its status back on undo.
struct MarkDone;

#[async_trait]
impl ReplyAction for MarkDone {
    fn permission(&self) -> Option<&str> {
        Some("tickets.work")
    }

    fn describe(&self) -> Option<&str> {
        Some("Marked done")
    }

    async fn run(&self, ctx: &ReplyContext<'_, '_>) -> Result<Value, BoxError> {
        let key = json!(ctx.subject_key().ok_or("no ticket")?);
        let tickets = ctx.repositories.table("Ticket")?;
        let before = tickets.get_one(&key).await?;
        tickets
            .update_one(&key, &values(json!({ "status": "done" })), &WriteOptions::default())
            .await?;
        Ok(json!({ "status": before["status"] }))
    }

    fn undoable(&self) -> bool {
        true
    }

    async fn undo(&self, ctx: &ReplyContext<'_, '_>, data: &Value) -> Result<(), BoxError> {
        let key = json!(ctx.subject_key().ok_or("no ticket")?);
        ctx.repositories
            .table("Ticket")?
            .update_one(&key, &values(json!({ "status": data["status"] })), &WriteOptions::default())
            .await?;
        Ok(())
    }
}

/// The scalars the identity store parses logins and keys with.
struct Catalog;

impl Scalars for Catalog {
    fn parse(&self, scalar: &str, value: &str) -> Option<Result<String, String>> {
        match scalar {
            "Contact.Email" => Some(Ok(value.trim().to_lowercase())),
            "Identity.UUID" => Some(Ok(value.to_owned())),
            _ => None,
        }
    }
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

type Expand = runtime::orm::notify::Expand;

/// A schema of its own holding fixture-notifications-db's tables, the
/// notifier over it, and the people and the ticket of every case.
struct World {
    pool: deadpool_postgres::Pool,
    admin: tokio_postgres::Client,
    schema: String,
    notifier: Notifier,
    relays: Relays,
    ann: String,
    ben: String,
    cal: String,
    ticket: String,
}

#[derive(Default)]
struct Options {
    now: Option<SystemTime>,
    expand: Option<Expand>,
}

async fn world(options: Options) -> World {
    let url = std::env::var("NOTIFY_TEST_DATABASE_URL").expect("NOTIFY_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("notify_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(8).build().unwrap();

    let relays = Relays::default();
    let (sms, mail) = (relays.clone(), relays.clone());
    let providers = providers_from(ProvidersOptions {
        slots: BTreeMap::from([("SMS".to_owned(), "channel".to_owned()), ("MAIL".to_owned(), "channel".to_owned())]),
        config: BTreeMap::from([
            ("SMS".to_owned(), ConfigValue::Text("sms-relay".to_owned())),
            ("MAIL".to_owned(), ConfigValue::Text("mail-relay".to_owned())),
        ]),
        definitions: vec![
            ProviderDefinition::new("sms-relay", RuntimeInterface::Channel, move |_, _| {
                Ok(ProviderInstance::Channel(Arc::new(SmsRelay(sms.clone()))))
            })
            .with_operations(["status", "inbound"]),
            ProviderDefinition::new("mail-relay", RuntimeInterface::Channel, move |_, _| {
                Ok(ProviderInstance::Channel(Arc::new(MailRelay(mail.clone()))))
            }),
        ],
        manifests: BTreeMap::new(),
    })
    .unwrap();
    let dispatch = QueueDefinition::parse(types::queues::NOTICE_WORK_QUEUE).unwrap();
    let [(approve, approve_action), (deny, deny_action)] = approval_replies("Approval", "agents.approve");
    let mut notifier = Notifier::new(types::orm::postgres::tables(pool.clone()), dispatch)
        .unwrap()
        .with_providers(Arc::new(providers))
        .with_reply("ticket.overdue", "done", Arc::new(MarkDone))
        .with_reply("ticket.assigned", "done", Arc::new(MarkDone))
        .with_reply("approval.requested", approve, approve_action)
        .with_reply("approval.requested", deny, deny_action);
    if let Some(now) = options.now {
        notifier = notifier.with_clock(Arc::new(move || now));
    }
    if let Some(expand) = options.expand {
        notifier = notifier.with_expand(expand);
    }

    let mut w = World {
        pool,
        admin,
        schema,
        notifier,
        relays,
        ann: String::new(),
        ben: String::new(),
        cal: String::new(),
        ticket: String::new(),
    };
    let (ann, ben, cal, ticket) = w
        .tx(async |r| {
            let account = |email: &str| values(json!({ "email": email }));
            let ann = text(&r.untyped().table("Account")?.create_one(&account(EMAIL_ANN)).await?, "id");
            let ben = text(&r.untyped().table("Account")?.create_one(&account(EMAIL_BEN)).await?, "id");
            let cal = text(&r.untyped().table("Account")?.create_one(&account(EMAIL_CAL)).await?, "id");
            let worker = r
                .untyped()
                .table("Role")?
                .create_one(&values(json!({ "name": "worker", "permissions": ["tickets.work"] })))
                .await?;
            for user in [&ann, &ben] {
                r.untyped()
                    .table("UserRoleGrant")?
                    .create_one(&values(json!({ "userId": user, "roleId": worker["id"], "grantedAt": "2026-01-01T00:00:00Z" })))
                    .await?;
            }
            // Ann owns the ticket; Ben is named on it; Cal watches it and
            // may not read it.
            let ticket = r
                .untyped()
                .table("Ticket")?
                .create_one(&values(json!({
                    "title": "Boiler pressure low", "status": "open", "owner": ann,
                    "watchers": [ben, cal], "audience": "named", "audienceNamed": [ben],
                })))
                .await?;
            for (who, phone, email) in [(&ann, PHONE_ANN, EMAIL_ANN), (&ben, PHONE_BEN, EMAIL_BEN), (&cal, PHONE_CAL, EMAIL_CAL)] {
                for (kind, address) in [("phone", phone), ("email", email)] {
                    r.untyped()
                        .table("ChannelIdentity")?
                        .create_one(&values(json!({
                            "kind": kind, "address": address, "principal": who,
                            "trust": "verified", "verifiedAt": "2026-01-01T00:00:00Z",
                        })))
                        .await?;
                }
            }
            // Everyone's preference (SMS, then email) and each channel
            // kind's trust (SMS low, email standard) are the schema's seed
            // rows (#625), which create.sql wrote.
            r.untyped()
                .table("NoticeRoute")?
                .create_one(&values(json!({
                    "name": "Overdue tickets page their people",
                    "eventKind": "ticket.overdue",
                    "recipients": [{ "path": "owner" }, { "path": "watchers" }],
                    "escalation": [{ "afterSeconds": 600, "channels": ["email"] }],
                })))
                .await?;
            Ok::<_, BoxError>((ann, ben, cal, text(&ticket, "id")))
        })
        .await
        .unwrap();
    w.ann = ann;
    w.ben = ben;
    w.cal = cal;
    w.ticket = ticket;
    w
}

impl World {
    /// Runs `work` over the system's repositories in a transaction it
    /// commits.
    async fn tx<T>(&self, work: impl AsyncFnOnce(&Repositories<'_, deadpool_postgres::Transaction<'_>>) -> T) -> T {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let repos = Repositories::new(&tx).unwrap().as_system();
        let out = work(&repos).await;
        drop(repos);
        tx.commit().await.unwrap();
        out
    }

    /// The rows of `table` that `filter` finds, read as the system, in
    /// the order of `order`.
    async fn rows(&self, table: &str, filter: Value, order: Option<&str>) -> Vec<Values> {
        let client = self.pool.get().await.unwrap();
        let repos = Repositories::new(&client).unwrap().as_system();
        let options = FindOptions {
            order_by: order.map(OrderBy::asc).into_iter().collect(),
            ..FindOptions::default()
        };
        repos.untyped().table(table).unwrap().find_many(Some(&values(filter)), &options).await.unwrap().rows
    }

    async fn ticket_status(&self) -> String {
        let rows = self.rows("Ticket", json!({ "id": { "eq": self.ticket } }), None).await;
        text(&rows[0], "status")
    }

    async fn route(&self, name: &str, kind: &str, recipients: Value) {
        self.tx(async |r| {
            r.untyped()
                .table("NoticeRoute")?
                .create_one(&values(json!({ "name": name, "eventKind": kind, "recipients": recipients })))
                .await
        })
        .await
        .unwrap();
    }

    /// Raises `kind` on the ticket in a transaction of its own.
    async fn raise(&self, kind: &str, id: Option<String>) -> Result<String, NotifierError> {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let mut input = RaiseInput::of("Ticket", self.ticket.clone(), kind);
        input.id = id;
        let raised = self.notifier.raise(&tx, input).await;
        if raised.is_ok() {
            tx.commit().await.unwrap();
        }
        raised
    }

    async fn overdue(&self) -> String {
        self.raise("ticket.overdue", None).await.unwrap()
    }

    /// Runs every message of the dispatch queue that is due, until none
    /// is.
    async fn drain(&self) -> usize {
        let queue = self.notifier.worker_queue();
        let mut handled = 0;
        for _ in 0..50 {
            let claims = queue.claim(20, Duration::from_secs(30)).await.unwrap();
            if claims.is_empty() {
                return handled;
            }
            for claim in claims {
                self.notifier.handle(claim.message).await.unwrap();
                queue.complete(&claim.id, &claim.token).await.unwrap();
                handled += 1;
            }
        }
        panic!("the dispatch queue did not drain");
    }

    /// Makes the queue's waiting messages of `step` due now: time passing.
    async fn advance(&self, step: &str) {
        let client = self.pool.get().await.unwrap();
        client
            .execute("UPDATE notice_work_queue SET due_at = now() WHERE step = $1 AND state = 'ready'", &[&step])
            .await
            .unwrap();
    }

    async fn notices_of(&self, user: &str) -> Vec<Values> {
        self.notifier.inbox(&RecipientRef::user(user), &[], None).await.unwrap()
    }

    async fn reply(&self, channel: &str, from: &str, body: &str) -> runtime::orm::notify::ReplyOutcome {
        self.notifier.reply(ReplyInput::of(channel, from, body)).await.unwrap()
    }

    async fn drop_schema(self) {
        drop(self.notifier);
        self.pool.close();
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }
}

fn signed(body: Value) -> WebhookRequest {
    WebhookRequest {
        headers: BTreeMap::from([("x-relay-token".to_owned(), RELAY_TOKEN.to_owned())]),
        body: body.to_string(),
    }
}

fn now_millis() -> u64 {
    u64::try_from(SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis()).unwrap()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn notifications() {
    if std::env::var("NOTIFY_TEST_DATABASE_URL").is_err() {
        panic!("set NOTIFY_TEST_DATABASE_URL");
    }

    {
        case("an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time");
        let w = world(Options::default()).await;
        let event = w.overdue().await;
        w.drain().await;
        let sms = w.relays.sms();
        assert_eq!(Relays::to(&sms), [PHONE_ANN, PHONE_BEN]);
        assert!(sms[0].text.contains("Boiler pressure low"), "{sms:?}");
        assert!(sms[0].text.contains("ACK 1"), "{sms:?}");
        let ann = w.notices_of(&w.ann).await;
        assert_eq!(ann.len(), 1);
        assert_eq!(text(&ann[0], "eventId"), event);
        assert_eq!(text(&ann[0], "urgency"), "urgent");
        assert_eq!(ann[0]["payload"], json!({ "title": "Boiler pressure low" }));
        // A kind the table does not declare is refused, and its
        // transaction with it.
        let lost = w.raise("ticket.lost", None).await.unwrap_err();
        assert!(lost.to_string().contains("declares no event ticket.lost"), "{lost}");
        // Cal cannot read the ticket: no notice, no delivery, no trace.
        assert!(w.notices_of(&w.cal).await.is_empty());
        assert!(w.rows("Delivery", json!({ "recipient": { "eq": w.cal } }), None).await.is_empty());
        assert!(w.relays.mail().is_empty());
        // Ten minutes on, nobody has acknowledged: email follows.
        w.advance("escalate").await;
        w.drain().await;
        assert_eq!(Relays::to(&w.relays.mail()), [EMAIL_ANN, EMAIL_BEN]);
        let deliveries = w.rows("Delivery", json!({ "recipient": { "eq": w.ann } }), Some("queuedAt")).await;
        let got: Vec<(String, String, String)> = deliveries
            .iter()
            .map(|d| (text(d, "channelKind"), text(d, "state"), text(d, "attempt")))
            .collect();
        assert_eq!(
            got,
            [("sms".into(), "sent".into(), "1".into()), ("email".into(), "sent".into(), "2".into())]
        );
        w.drop_schema().await;
    }

    {
        case("a reply of ack acknowledges the notice and stops its escalation, and undo takes it back");
        let w = world(Options::default()).await;
        w.overdue().await;
        w.drain().await;
        let acked = w.reply("sms", "+1555) 010-0001", "ack").await;
        assert_eq!(acked.outcome, "done", "{acked:?}");
        assert!(acked.echo.as_deref().unwrap().contains("UNDO 1"), "{acked:?}");
        assert_eq!(w.relays.sms().last().unwrap().to, PHONE_ANN);
        let notice = &w.notices_of(&w.ann).await[0];
        assert_eq!(text(notice, "state"), "acknowledged");
        assert_eq!(text(notice, "acknowledgedVia"), "sms");
        w.advance("escalate").await;
        w.drain().await;
        // Ann acknowledged: only Ben's email went out.
        assert_eq!(Relays::to(&w.relays.mail()), [EMAIL_BEN]);
        let undone = w.reply("sms", PHONE_ANN, "undo 1").await;
        assert_eq!(undone.outcome, "done", "{undone:?}");
        assert_eq!(text(&w.notices_of(&w.ann).await[0], "state"), "unread");
        w.drop_schema().await;
    }

    {
        case("a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text");
        let w = world(Options::default()).await;
        // Ann has had 141 notices before this one.
        w.tx(async |r| {
            r.untyped()
                .table("Notice")?
                .create_one(&values(json!({
                    "recipient": w.ann, "reference": 141, "eventId": "0f8b6c4e-1b2a-4c3d-9e8f-123456789abc",
                    "eventKind": "ticket.assigned", "urgency": "record", "title": "Earlier", "state": "read",
                })))
                .await
        })
        .await
        .unwrap();
        w.overdue().await;
        w.drain().await;
        let to_ann = w.relays.sms().into_iter().find(|m| m.to == PHONE_ANN).unwrap();
        assert!(to_ann.text.contains("#142"), "{to_ann:?}");
        let done = w.reply("email", "Ann@Example.test", "done 142").await;
        assert_eq!(done.outcome, "done", "{done:?}");
        let echo = done.echo.clone().unwrap();
        assert!(echo.contains("Marked done 142") && echo.contains("UNDO 142"), "{echo}");
        assert_eq!(w.ticket_status().await, "done");
        assert_eq!(w.relays.mail().last().unwrap().to, EMAIL_ANN);
        let audit = &w.rows("Reply", json!({ "verb": { "eq": "done" } }), None).await[0];
        for (member, want) in [
            ("channelKind", "email"),
            ("address", EMAIL_ANN),
            ("interpreter", "keyword"),
            ("verb", "done"),
            ("reference", "142"),
            ("outcome", "done"),
            ("body", "done 142"),
        ] {
            assert_eq!(text(audit, member), want, "{member}: {audit:?}");
        }
        assert_eq!(text(audit, "principal"), w.ann);
        let notice = w.notices_of(&w.ann).await.into_iter().find(|n| text(n, "reference") == "142").unwrap();
        assert_eq!(text(&notice, "state"), "done");
        // Undo puts the ticket back as it was.
        assert_eq!(w.reply("email", EMAIL_ANN, "undo 142").await.outcome, "done");
        assert_eq!(w.ticket_status().await, "open");
        w.drop_schema().await;
    }

    {
        case("a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges");
        let w = world(Options::default()).await;
        w.overdue().await;
        w.drain().await;
        let refused = w.reply("sms", PHONE_ANN, "done 1").await;
        assert_eq!(refused.outcome, "refused");
        assert!(refused.detail.as_deref().unwrap().contains("its trust is low"), "{refused:?}");
        assert_eq!(w.ticket_status().await, "open");
        let audit = &w.rows("Reply", json!({ "outcome": { "eq": "refused" } }), None).await[0];
        assert_eq!((text(audit, "channelKind"), text(audit, "verb")), ("sms".into(), "done".into()));
        // Ben's email, entered by hand, is trusted only to acknowledge.
        let ben = w.ben.clone();
        w.tx(async |r| {
            r.untyped()
                .table("ChannelIdentity")?
                .update_many(
                    Some(&values(json!({ "principal": { "eq": ben }, "kind": { "eq": "email" } }))),
                    &values(json!({ "trust": "entered" })),
                )
                .await
        })
        .await
        .unwrap();
        let by_hand = w.reply("email", EMAIL_BEN, "done 1").await;
        assert_eq!(by_hand.outcome, "refused");
        assert!(by_hand.detail.as_deref().unwrap().contains("entered by hand"), "{by_hand:?}");
        assert_eq!(w.reply("email", EMAIL_BEN, "ack 1").await.outcome, "done");
        w.drop_schema().await;
    }

    {
        case("a reply names its notice by reference unless exactly one waits on its sender");
        let w = world(Options::default()).await;
        w.overdue().await;
        w.raise("ticket.overdue", Some("1f8b6c4e-1b2a-4c3d-9e8f-123456789abc".to_owned())).await.unwrap();
        w.drain().await;
        let ambiguous = w.reply("sms", PHONE_ANN, "ack").await;
        assert_eq!(ambiguous.outcome, "ambiguous");
        assert!(ambiguous.detail.as_deref().unwrap().contains("#2"), "{ambiguous:?}");
        assert_eq!(w.reply("sms", PHONE_ANN, "ack 2").await.outcome, "done");
        // One waits now: ack alone takes it.
        assert_eq!(w.reply("sms", PHONE_ANN, "OK").await.outcome, "done");
        assert!(w.notices_of(&w.ann).await.iter().all(|n| text(n, "state") == "acknowledged"));
        assert_eq!(w.reply("sms", PHONE_ANN, "ack 9").await.outcome, "refused");
        w.drop_schema().await;
    }

    {
        case("an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing");
        let w = world(Options::default()).await;
        let dana = w
            .tx(async |r| r.untyped().table("Account")?.create_one(&values(json!({ "email": "dana@example.test" }))).await)
            .await
            .unwrap();
        let dana = text(&dana, "id");
        let identity = w
            .notifier
            .add_identity(AddIdentity {
                kind: "phone".to_owned(),
                address: "+1 555 010 0009".to_owned(),
                principal: RecipientRef::user(dana.clone()),
                trust: None,
            })
            .await
            .unwrap();
        assert_eq!(text(&identity, "trust"), "unverified");
        assert!(w.notifier.resolve_sender("phone", "+15550100009").await.unwrap().is_empty());
        w.notifier.start_verification(&text(&identity, "id"), "verify").await.unwrap();
        let sent = w.relays.sms().last().unwrap().clone();
        assert_eq!(sent.to, "+15550100009");
        let code: String = sent.text.chars().filter(char::is_ascii_digit).take(6).collect();
        assert_eq!(code.len(), 6, "{sent:?}");
        assert_eq!(w.reply("sms", "+15550100009", "000000").await.outcome, "unknown");
        assert_eq!(w.reply("sms", "+15550100009", &code).await.outcome, "verified");
        let resolved = w.notifier.resolve_sender("phone", "+15550100009").await.unwrap();
        assert_eq!(text(&resolved[0], "principal"), dana);
        let stranger = w.reply("sms", "+15550109999", "done 1").await;
        assert_eq!(stranger.outcome, "unknown");
        assert!(stranger.principal.is_none());
        assert_eq!(w.ticket_status().await, "open");
        w.drop_schema().await;
    }

    {
        case("a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others");
        let w = world(Options::default()).await;
        w.relays.state.lock().unwrap().fail_to.insert(PHONE_ANN.to_owned());
        w.overdue().await;
        w.drain().await;
        assert_eq!(Relays::to(&w.relays.mail()), [EMAIL_ANN]);
        let deliveries = w.rows("Delivery", json!({ "recipient": { "eq": w.ann } }), Some("queuedAt")).await;
        let got: Vec<(String, String)> = deliveries.iter().map(|d| (text(d, "channelKind"), text(d, "state"))).collect();
        assert_eq!(got, [("sms".into(), "failed".into()), ("email".into(), "sent".into())]);
        // The SMS relay goes down: its health check fails, channel.down is
        // raised to the people its route names, and they hear of it by
        // email.
        w.route("Channel alerts", "channel.down", json!([{ "principal": w.ben }])).await;
        w.relays.state.lock().unwrap().sms_down = true;
        let health = w.notifier.check_health().await.unwrap();
        assert!(!health.iter().find(|h| h.slot == "SMS").unwrap().ok);
        w.drain().await;
        let alerts: Vec<Values> = w.notices_of(&w.ben).await.into_iter().filter(|n| text(n, "eventKind") == "channel.down").collect();
        assert_eq!(alerts.len(), 1);
        assert_eq!(text(&alerts[0], "subjectKey"), "SMS");
        assert_eq!(w.relays.mail().last().unwrap().to, EMAIL_BEN);
        // A second check while it is still down alerts no one again.
        w.notifier.check_health().await.unwrap();
        w.drain().await;
        let alerts = w.notices_of(&w.ben).await.into_iter().filter(|n| text(n, "eventKind") == "channel.down").count();
        assert_eq!(alerts, 1);
        w.drop_schema().await;
    }

    {
        case("quiet hours hold a normal notice, digests batch, and record sends nothing");
        // 2027-03-10T23:30:00Z.
        let fixed = UNIX_EPOCH + Duration::from_secs(1_804_721_400);
        let digest_at = i64::try_from((fixed + Duration::from_secs(30_600)).duration_since(UNIX_EPOCH).unwrap().as_millis()).unwrap();
        let expand: Expand = Arc::new(move |_| vec![digest_at]);
        let w = world(Options { now: Some(fixed), expand: Some(expand) }).await;
        let (ann, ben) = (w.ann.clone(), w.ben.clone());
        w.tx(async |r| {
            let prefs = r.untyped().table("NoticePreference")?;
            prefs
                .create_one(&values(json!({
                    "principal": ben, "eventKind": "*", "urgency": "normal", "channels": ["sms"],
                    "quietStart": "22:00", "quietEnd": "07:00", "timeZone": "UTC",
                })))
                .await?;
            prefs
                .create_one(&values(json!({
                    "principal": ann, "eventKind": "*", "urgency": "digest", "channels": ["email"],
                    "timeZone": "UTC", "digest": "FREQ=DAILY;BYHOUR=8;BYMINUTE=0",
                })))
                .await
        })
        .await
        .unwrap();
        w.route("Assignments", "ticket.assigned", json!([{ "principal": w.ben }])).await;
        w.route("Summaries", "ticket.summary", json!([{ "path": "owner" }])).await;
        w.route("History", "reference.cleared", json!([{ "path": "owner" }])).await;
        {
            let mut client = w.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            for (kind, id) in [
                ("ticket.assigned", None),
                ("ticket.summary", None),
                ("ticket.summary", Some("2f8b6c4e-1b2a-4c3d-9e8f-123456789abc")),
                ("reference.cleared", None),
            ] {
                let mut input = RaiseInput::of("Ticket", w.ticket.clone(), kind);
                input.id = id.map(str::to_owned);
                w.notifier.raise(&tx, input).await.unwrap();
            }
            tx.commit().await.unwrap();
        }
        w.drain().await;
        assert!(w.relays.sms().is_empty());
        assert!(w.relays.mail().is_empty());
        let held = &w.rows("Delivery", json!({ "recipient": { "eq": w.ben } }), None).await[0];
        assert_eq!(text(held, "state"), "queued");
        assert!(text(held, "dueAt").starts_with("2027-03-11T07:00:00"), "{held:?}");
        assert_eq!(w.notices_of(&w.ben).await[0]["payload"], json!({ "title": "Boiler pressure low", "status": "open" }));
        let record = w.notices_of(&w.ann).await.into_iter().find(|n| text(n, "eventKind") == "reference.cleared").unwrap();
        assert_eq!(text(&record, "state"), "read");
        // One digest is waiting for Ann's two summaries.
        let client = w.pool.get().await.unwrap();
        let mut steps: Vec<String> = client
            .query("SELECT step FROM notice_work_queue WHERE state = 'ready'", &[])
            .await
            .unwrap()
            .iter()
            .map(|row| row.get(0))
            .collect();
        drop(client);
        steps.sort();
        assert_eq!(steps, ["deliver", "digest"]);
        w.advance("digest").await;
        w.advance("deliver").await;
        w.drain().await;
        assert_eq!(Relays::to(&w.relays.sms()), [PHONE_BEN]);
        let mail = w.relays.mail();
        assert_eq!(mail.len(), 1);
        assert_eq!(mail[0].to, EMAIL_ANN);
        assert_eq!(mail[0].text.split('\n').count(), 2, "{mail:?}");
        w.drop_schema().await;
    }

    {
        case("a delivery's statuses move it on and a cleared reference is recorded on its holder");
        let w = world(Options::default()).await;
        w.overdue().await;
        w.drain().await;
        let sms = w.relays.sms().into_iter().find(|m| m.to == PHONE_ANN).unwrap();
        let moved = w
            .notifier
            .statuses("SMS", &signed(json!({ "events": [{ "id": sms.id, "status": "delivered", "at": now_millis() }] })))
            .await
            .unwrap();
        assert_eq!(moved, 1);
        let delivery = &w.rows("Delivery", json!({ "providerMessageId": { "eq": sms.id } }), None).await[0];
        assert_eq!(text(delivery, "state"), "delivered");
        // An unsigned callback is the adapter's to refuse.
        assert!(w.notifier.statuses("SMS", &WebhookRequest::default()).await.is_err());
        // Replies come in through the relay's own callback.
        let outcomes = w
            .notifier
            .inbound("SMS", &signed(json!({ "messages": [{ "id": "in-1", "from": PHONE_ANN, "text": "ack 1", "at": now_millis() }] })))
            .await
            .unwrap();
        assert_eq!(outcomes[0].outcome, "done", "{outcomes:?}");
        let delivery = &w.rows("Delivery", json!({ "providerMessageId": { "eq": sms.id } }), None).await[0];
        assert_eq!(text(delivery, "state"), "acknowledged");
        // A ticket another refers to is deleted: its holder records it.
        w.route("History", "reference.cleared", json!([{ "path": "owner" }])).await;
        let (ticket, ann) = (w.ticket.clone(), w.ann.clone());
        w.tx(async |r| {
            let other = r
                .untyped()
                .table("Ticket")?
                .create_one(&values(json!({ "title": "Parts order", "status": "open", "owner": ann })))
                .await?;
            let other = text(&other, "id");
            r.references()?.add(&RowRef::new("Ticket", &ticket), &RowRef::new("Ticket", &other), "").await?;
            r.untyped().table("Ticket")?.delete_one(&json!(other), &WriteOptions::default()).await?;
            Ok::<_, BoxError>(())
        })
        .await
        .unwrap();
        let cleared = PostgresQueue::<Value>::new(
            w.pool.clone(),
            QueueDefinition::parse(types::queues::REFERENCE_CLEARED_QUEUE).unwrap(),
        );
        let claims = cleared.claim(10, Duration::from_secs(30)).await.unwrap();
        assert_eq!(claims.len(), 1);
        {
            let mut client = w.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            w.notifier.cleared(&tx, &claims[0].message).await.unwrap();
            tx.commit().await.unwrap();
        }
        cleared.complete(&claims[0].id, &claims[0].token).await.unwrap();
        w.drain().await;
        let record = w.notices_of(&w.ann).await.into_iter().find(|n| text(n, "eventKind") == "reference.cleared").unwrap();
        assert_eq!(record["payload"]["targetTable"], json!("Ticket"));
        assert_eq!(record["payload"]["role"], json!(""));
        assert_eq!(text(&record, "title"), "Boiler pressure low");
        // A behavior's event, as its behavior worker's Workers method
        // hands it.
        w.route("Missed", "recurrence.missed", json!([{ "path": "owner" }])).await;
        {
            let mut client = w.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            w.notifier
                .from_behavior_event(&tx, "Ticket", "recurrence", "missed", &w.ticket, Some(&json!({ "key": "2027-01-01T09:00" })))
                .await
                .unwrap();
            tx.commit().await.unwrap();
        }
        w.drain().await;
        let missed = w.notices_of(&w.ann).await.into_iter().find(|n| text(n, "eventKind") == "recurrence.missed").unwrap();
        assert_eq!(missed["payload"], json!({ "key": "2027-01-01T09:00" }));
        w.drop_schema().await;
    }

    {
        case("password reset sends a code through the email adapter and the code sets the password");
        let w = world(Options::default()).await;
        let params = Argon2Params { memory_kib: 64, iterations: 1, parallelism: 1 };
        let store: Arc<dyn Store> = Arc::new(
            SqlStore::new(PostgresPool::new(w.pool.clone()), types::identity::IDENTITY_DESCRIPTOR, Arc::new(Catalog)).unwrap(),
        );
        let config = Config::parse(br#"{"password":{"argon2":{"memoryKiB":64,"iterations":1,"parallelism":1}}}"#).unwrap();
        let service = Service::new(store.clone(), config).unwrap();
        store
            .create_user(NewUser {
                login: "dana@example.test".to_owned(),
                name: String::new(),
                password_hash: hash_password("first-password-1", params).unwrap(),
                at: SystemTime::now(),
            })
            .await
            .unwrap();
        let reset = PasswordReset::new(w.notifier.clone(), Arc::new(service));
        reset.request("nobody@example.test").await.unwrap();
        assert!(w.relays.mail().is_empty());
        reset.request("Dana@example.test").await.unwrap();
        let sent = w.relays.mail().last().unwrap().clone();
        assert_eq!(sent.to, "dana@example.test");
        assert!(sent.subject.contains("password reset"), "{sent:?}");
        let code: String = sent.text.chars().filter(char::is_ascii_digit).take(6).collect();
        assert!(!reset.complete("dana@example.test", "999999", "second-password-2").await.unwrap());
        assert!(reset.complete("dana@example.test", &code, "second-password-2").await.unwrap());
        let login = store.find_login("dana@example.test").await.unwrap();
        assert!(verify_password(&login.password_hash, "second-password-2", params).unwrap().ok);
        // The code is spent, and the address it reached is verified.
        assert!(!reset.complete("dana@example.test", &code, "third-password-3").await.unwrap());
        assert_eq!(w.notifier.resolve_sender("email", "dana@example.test").await.unwrap().len(), 1);
        assert!(!format!("{:?}", w.relays.mail()).contains(RELAY_TOKEN));
        w.drop_schema().await;
    }

    {
        case("an approval is asked of the principals who hold its permission, and approve and deny by reply decide it within the channel's trust");
        let w = world(Options::default()).await;
        let ben = w.ben.clone();
        let (first, second) = w
            .tx(async |r| {
                let approver = r
                    .untyped()
                    .table("Role")?
                    .create_one(&values(json!({ "name": "approver", "permissions": ["agents.approve"] })))
                    .await?;
                r.untyped()
                    .table("UserRoleGrant")?
                    .create_one(&values(json!({ "userId": ben, "roleId": approver["id"], "grantedAt": "2026-01-01T00:00:00Z" })))
                    .await?;
                let approval = |summary: &str| {
                    values(json!({ "operation": "pay_invoice", "summary": summary, "permission": "agents.approve", "state": "pending" }))
                };
                let first = r.untyped().table("Approval")?.create_one(&approval("Pay invoice 7 for 120")).await?;
                let second = r.untyped().table("Approval")?.create_one(&approval("Pay invoice 8 for 300")).await?;
                Ok::<_, BoxError>((text(&first, "id"), text(&second, "id")))
            })
            .await
            .unwrap();
        w.route("Approvals go to approvers", "approval.requested", json!([{ "role": "approver" }])).await;
        for approval in [&first, &second] {
            let mut client = w.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            w.notifier.raise(&tx, RaiseInput::of("Approval", approval.clone(), "approval.requested")).await.unwrap();
            tx.commit().await.unwrap();
        }
        w.drain().await;
        // Ben holds the approval's permission and hears of both; Ann does
        // not.
        assert_eq!(Relays::to(&w.relays.sms()), [PHONE_BEN, PHONE_BEN]);
        assert!(w.relays.sms()[0].text.contains("Pay invoice 7 for 120"), "{:?}", w.relays.sms());
        assert!(w.notices_of(&w.ann).await.is_empty());
        let state = async |id: &str| {
            let rows = w.rows("Approval", json!({ "id": { "eq": id } }), None).await;
            rows[0].clone()
        };
        // SMS is low: it may not approve.
        let low = w.reply("sms", PHONE_BEN, "approve 1").await;
        assert_eq!(low.outcome, "refused");
        assert!(low.detail.as_deref().unwrap().contains("its trust is low"), "{low:?}");
        assert_eq!(text(&state(&first).await, "state"), "pending");
        let approved = w.reply("email", EMAIL_BEN, "approve 1").await;
        assert_eq!(approved.outcome, "done", "{approved:?}");
        assert!(approved.echo.as_deref().unwrap().starts_with("Approved 1: Pay invoice 7 for 120."), "{approved:?}");
        let decided = state(&first).await;
        assert_eq!(
            (text(&decided, "state"), text(&decided, "decidedBy"), text(&decided, "decidedVia")),
            ("approved".into(), w.ben.clone(), "email".into())
        );
        let denied = w.reply("email", EMAIL_BEN, "deny 2").await;
        assert_eq!(denied.outcome, "done", "{denied:?}");
        assert_eq!(text(&state(&second).await, "state"), "denied");
        // A decided approval is decided once.
        let again = w.reply("email", EMAIL_BEN, "approve 2").await;
        assert_eq!(again.outcome, "failed", "{again:?}");
        assert!(again.detail.as_deref().unwrap().contains("the approval is denied"), "{again:?}");
        assert_eq!(text(&state(&second).await, "state"), "denied");
        w.drop_schema().await;
    }
}
