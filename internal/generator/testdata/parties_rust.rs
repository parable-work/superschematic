//! fixture-parties-db end to end in the Rust repositories and runtime
//! (D157, D193, #618), run by TestRustPartiesEndToEnd: contacts that are
//! parties, the jobs a grant lets one reach and the reports it writes on
//! them, the conversations and their messages, which hold files and point
//! at rows, the run requests queue, and the notifier a user's reply runs
//! through. Each case runs on the database RUST_E2E_STORE names
//! (tests/backend): a SQLite file with the build's SQLite DDL applied, or
//! a schema of the test's own at RUST_E2E_DATABASE_URL with create.sql
//! applied. The cases are the TypeScript repositories' (tsgen's
//! parties.test.ts), through the runtime's orm::inbound over the generated
//! repositories, with an SMS relay and a mail relay as stand-in channel
//! adapters (D159) and a bucket in memory; each prints its answers, which
//! the Go test holds equal on the two databases.

mod backend;

use std::collections::{BTreeMap, HashMap};
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use backend::{answer, in_tx, rows_of, Backend};
use runtime::access::IssueGrant;
use runtime::bucket::{
    Body, Bucket, BucketError, Bytes, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions, Stream,
};
use runtime::orm::inbound::{
    Inbound, InboundError, InboundOutcome, RunRequest, SendApproval, SendDecision, SendInput,
};
use runtime::orm::notify::Notifier;
use runtime::orm::parties::PartyRef;
use runtime::orm::store::{Store, TxRepositories};
use runtime::orm::rowfeatures::RowRef;
use runtime::orm::{AccessCaller, FindOptions, Values};
use runtime::providers::{
    providers_from, ChannelAdapter, ChannelHealth, ConfigValue, DeliveryState, InboundAttachment,
    InboundAuthentication, InboundMessage, OptOut, OutboundMessage, ProviderDefinition,
    ProviderInstance, ProvidersOptions, RuntimeInterface, SendResult, WebhookRequest,
};
use runtime::queue::QueueDefinition;
use runtime::{ActingFor, BoxError, Principal, PrincipalKind};
use serde_json::{json, Value};

const RELAY_TOKEN: &str = "relay-token";
const INBOX: &str = "inbox@example.test";
const PHOTO: [u8; 12] = [0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 5, 6, 7, 8];
const DEE: &str = "+15550100011";
const EVE: &str = "+15550100012";
const ANN: &str = "+15550100001";

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

fn sha(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

fn nanos() -> u128 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos()
}

/// What a stand-in relay sent.
#[derive(Clone, Debug)]
struct Sent {
    to: String,
    subject: String,
    text: String,
    message_id: Option<String>,
    reply_to: Option<String>,
}

#[derive(Clone, Default)]
struct Relays {
    sms: Arc<Mutex<Vec<Sent>>>,
    mail: Arc<Mutex<Vec<Sent>>>,
    n: Arc<Mutex<u64>>,
}

impl Relays {
    fn sms(&self) -> Vec<Sent> {
        self.sms.lock().unwrap().clone()
    }

    fn mail(&self) -> Vec<Sent> {
        self.mail.lock().unwrap().clone()
    }

    fn next(&self, prefix: &str) -> String {
        let mut n = self.n.lock().unwrap();
        *n += 1;
        format!("{prefix}-{n}")
    }
}

fn checked(request: &WebhookRequest) -> Result<Value, BoxError> {
    if request.headers.get("x-relay-token").map(String::as_str) != Some(RELAY_TOKEN) {
        return Err("the callback holds no relay token".into());
    }
    Ok(serde_json::from_str(&request.body)?)
}

fn strings(value: &Value) -> Vec<String> {
    value.as_array().into_iter().flatten().filter_map(Value::as_str).map(str::to_owned).collect()
}

/// A stand-in SMS relay: it records what it sends, fetches an inbound
/// text's media from its service, and reads STOP and START as an opt-out
/// and an opt-in.
struct SmsRelay(Relays);

#[async_trait]
impl ChannelAdapter for SmsRelay {
    fn kind(&self) -> &str {
        "sms"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let id = self.0.next("sms");
        self.0.sms.lock().unwrap().push(Sent {
            to: message.to.clone(),
            subject: String::new(),
            text: message.body.clone(),
            message_id: None,
            reply_to: None,
        });
        Ok(SendResult { provider_message_id: id, state: DeliveryState::Queued })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        Ok(ChannelHealth { ok: true, detail: None })
    }

    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        let body = checked(request)?;
        let mut out = Vec::new();
        for message in body["messages"].as_array().cloned().unwrap_or_default() {
            let mut attachments = Vec::new();
            for (i, media) in message["media"].as_array().cloned().unwrap_or_default().iter().enumerate() {
                if media["url"].as_str() != Some("https://sms.test/media/1") {
                    return Err("sms-relay: the relay's media answered 404".into());
                }
                attachments.push(InboundAttachment {
                    name: media["name"].as_str().map_or_else(|| format!("media-{}", i + 1), str::to_owned),
                    media_type: media["type"].as_str().unwrap_or("application/octet-stream").to_owned(),
                    bytes: PHOTO.to_vec(),
                });
            }
            let body = message["text"].as_str().unwrap_or_default().to_owned();
            let keyword = body.trim().to_uppercase();
            let opt_out = match keyword.as_str() {
                "STOP" | "STOPALL" | "UNSUBSCRIBE" | "CANCEL" | "END" | "QUIT" => Some(OptOut::Out),
                "START" | "UNSTOP" => Some(OptOut::In),
                _ => None,
            };
            out.push(InboundMessage {
                from: message["from"].as_str().unwrap_or_default().to_owned(),
                body,
                received_at: SystemTime::now(),
                provider_message_id: message["id"].as_str().unwrap_or_default().to_owned(),
                in_reply_to: message["replyTo"].as_str().map(str::to_owned),
                message_id: None,
                references: Vec::new(),
                to: Vec::new(),
                subject: None,
                authentication: None,
                attachments,
                opt_out,
            });
        }
        Ok(out)
    }
}

/// A stand-in mail relay: it records what it sends with its threading,
/// and reads the mail the relay received for the deployment, with its
/// authentication, threading and attachments.
struct MailRelay(Relays);

#[async_trait]
impl ChannelAdapter for MailRelay {
    fn kind(&self) -> &str {
        "email"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let id = self.0.next("mail");
        self.0.mail.lock().unwrap().push(Sent {
            to: message.to.clone(),
            subject: message.subject.clone().unwrap_or_default(),
            text: message.body.clone(),
            message_id: message.message_id.clone(),
            reply_to: message.reply_to.clone(),
        });
        Ok(SendResult { provider_message_id: id, state: DeliveryState::Sent })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        Ok(ChannelHealth { ok: true, detail: None })
    }

    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        let body = checked(request)?;
        let mut out = Vec::new();
        for mail in body["mail"].as_array().cloned().unwrap_or_default() {
            let auth = &mail["auth"];
            let authentication = auth.is_object().then(|| InboundAuthentication {
                spf: auth["spf"].as_str().map(str::to_owned),
                dkim: auth["dkim"].as_str().map(str::to_owned),
                dmarc: auth["dmarc"].as_str().map(str::to_owned),
            });
            let attachments = mail["attachments"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .iter()
                .map(|file| InboundAttachment {
                    name: file["name"].as_str().unwrap_or_default().to_owned(),
                    media_type: file["type"].as_str().unwrap_or_default().to_owned(),
                    bytes: PHOTO.to_vec(),
                })
                .collect();
            out.push(InboundMessage {
                from: mail["from"].as_str().unwrap_or_default().to_owned(),
                body: mail["text"].as_str().unwrap_or_default().to_owned(),
                received_at: SystemTime::now(),
                provider_message_id: mail["id"].as_str().unwrap_or_default().to_owned(),
                in_reply_to: mail["inReplyTo"].as_str().map(str::to_owned),
                message_id: mail["messageId"].as_str().map(str::to_owned),
                references: strings(&mail["references"]),
                to: strings(&mail["to"]),
                subject: mail["subject"].as_str().map(str::to_owned),
                authentication,
                attachments,
                opt_out: None,
            });
        }
        Ok(out)
    }
}

type Objects = HashMap<String, (Vec<u8>, Option<String>)>;

/// A bucket in memory.
#[derive(Default)]
struct MemoryBucket {
    objects: Mutex<Objects>,
}

struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BucketError>;
    fn poll_next(mut self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        Poll::Ready(self.0.take().map(Ok))
    }
}

#[async_trait]
impl Bucket for MemoryBucket {
    fn name(&self) -> &str {
        "memory/files"
    }

    async fn put(&self, name: &str, body: Body, options: PutOptions) -> Result<PutResult, BucketError> {
        let bytes = body.as_bytes().cloned().unwrap_or_default();
        let size = bytes.len() as u64;
        self.objects.lock().unwrap().insert(name.to_owned(), (bytes.to_vec(), options.content_type.clone()));
        let object = Object { name: name.to_owned(), size, content_type: options.content_type, updated: None };
        Ok(PutResult { object, created: true })
    }

    async fn head(&self, name: &str) -> Result<Option<Object>, BucketError> {
        Ok(self.objects.lock().unwrap().get(name).map(|(bytes, content_type)| Object {
            name: name.to_owned(),
            size: bytes.len() as u64,
            content_type: content_type.clone(),
            updated: None,
        }))
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        let Some((bytes, content_type)) = self.objects.lock().unwrap().get(name).cloned() else {
            return Err(BucketError::NotFound { bucket: "memory/files".into(), object: name.into() });
        };
        let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type, updated: None };
        Ok(Reader::new(object, Once(Some(Bytes::from(bytes)))))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        self.objects.lock().unwrap().remove(name);
        Ok(())
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        Ok(Page::default())
    }

    async fn signed_url(&self, name: &str, options: SignedUrlOptions) -> Result<String, BucketError> {
        Ok(format!("https://files.test/{name}?method={}", options.method.as_str()))
    }
}

/// The approval policy a case gives the path: by the body's first word.
struct ByWord;

#[async_trait]
impl SendApproval for ByWord {
    async fn approve(&self, input: &SendInput) -> Result<SendDecision, BoxError> {
        Ok(if input.body.starts_with("Quote") {
            SendDecision::Ask
        } else if input.body.starts_with("Refund") {
            SendDecision::Deny
        } else {
            SendDecision::Allow
        })
    }
}

/// A schema of its own holding fixture-parties-db's tables, the notifier
/// and the inbound path over it, and the contact, the job and the grant of
/// every case.
struct World {
    backend: Backend,
    store: Store,
    notifier: Notifier,
    inbound: Inbound,
    relays: Relays,
    bucket: Arc<MemoryBucket>,
    dee: String,
    job: String,
    grant: String,
    ann: String,
    staff: Principal,
}

async fn world(approve: bool) -> World {
    let backend = Backend::open("parties_rust", include_str!("../create.sqlite.sql"), include_str!("../create.sql")).await;
    let store = backend.store();

    let relays = Relays::default();
    let (sms, mail) = (relays.clone(), relays.clone());
    let providers = Arc::new(
        providers_from(ProvidersOptions {
            slots: BTreeMap::from([("SMS".to_owned(), "channel".to_owned()), ("MAIL".to_owned(), "channel".to_owned())]),
            config: BTreeMap::from([
                ("SMS".to_owned(), ConfigValue::Text("sms-relay".to_owned())),
                ("MAIL".to_owned(), ConfigValue::Text("mail-relay".to_owned())),
            ]),
            definitions: vec![
                ProviderDefinition::new("sms-relay", RuntimeInterface::Channel, move |_, _| {
                    Ok(ProviderInstance::Channel(Arc::new(SmsRelay(sms.clone()))))
                })
                .with_operations(["inbound"]),
                ProviderDefinition::new("mail-relay", RuntimeInterface::Channel, move |_, _| {
                    Ok(ProviderInstance::Channel(Arc::new(MailRelay(mail.clone()))))
                })
                .with_operations(["inbound"]),
            ],
            manifests: BTreeMap::new(),
        })
        .unwrap(),
    );
    let dispatch = QueueDefinition::parse(types::queues::NOTICE_WORK_QUEUE).unwrap();
    let notifier = Notifier::new(store.clone(), dispatch)
        .unwrap()
        .with_providers(providers.clone());
    let runs = QueueDefinition::parse(types::queues::RUN_REQUEST_QUEUE).unwrap();
    let bucket = Arc::new(MemoryBucket::default());
    let mut inbound = Inbound::new(store.clone(), notifier.clone(), runs)
        .unwrap()
        .with_providers(providers)
        .with_bucket(bucket.clone())
        .with_inbound_address(INBOX)
        .with_rate_limit(3, 3600);
    if approve {
        inbound = inbound.with_approval(Arc::new(ByWord));
    }
    let mut w = World {
        backend,
        store,
        notifier,
        inbound,
        relays,
        bucket,
        dee: String::new(),
        job: String::new(),
        grant: String::new(),
        ann: String::new(),
        staff: Principal::default(),
    };
    let (ann, dee, job) = w
        .tx(async |r| {
            r.table("Role")?
                .create_one(&values(json!({ "name": "contractor", "permissions": ["reports.write"], "reaches": ["Report:job"] })))
                .await?;
            let ann = text(&r.table("Account")?.create_one(&values(json!({ "email": "ann@example.test" }))).await?, "id");
            let dee = text(&r.table("Contact")?.create_one(&values(json!({ "name": "Dee" }))).await?, "id");
            let job = text(&r.table("Job")?.create_one(&values(json!({ "title": "Fix the gate" }))).await?, "id");
            for (kind, address) in [("phone", DEE), ("email", "dee@example.test")] {
                r.table("ChannelIdentity")?
                    .create_one(&values(json!({
                        "kind": kind, "address": address, "principalKind": "party", "principalTable": "Contact",
                        "principal": dee, "trust": "verified", "verifiedAt": "2026-01-01T00:00:00Z",
                    })))
                    .await?;
            }
            r.table("ChannelIdentity")?
                .create_one(&values(json!({ "kind": "phone", "address": ANN, "principal": ann, "trust": "verified", "verifiedAt": "2026-01-01T00:00:00Z" })))
                .await?;
            Ok::<_, BoxError>((ann, dee, job))
        })
        .await
        .unwrap();
    w.grant = w.grant_to(&dee, "Job", &job).await;
    w.staff = Principal::new(ann.clone(), ["parties.message", "parties.triage"]);
    w.ann = ann;
    w.dee = dee;
    w.job = job;
    w
}

impl World {
    /// Runs `work` over the system's repositories in a transaction it
    /// commits.
    async fn tx<T>(&self, work: impl AsyncFnOnce(&TxRepositories<'_, '_>) -> T) -> T {
        in_tx(&self.store, work).await
    }

    async fn grant_to(&self, holder: &str, table: &str, key: &str) -> String {
        self.tx(async |r| {
            let issued = r
                .issue_grant(&IssueGrant {
                    holder: holder.to_owned(),
                    holder_kind: PrincipalKind::Party,
                    role: "contractor".to_owned(),
                    target_type: table.to_owned(),
                    target_id: key.to_owned(),
                    starts_at: None,
                    ends_at: None,
                    ends_when: None,
                })
                .await
                .unwrap();
            text(&issued.grant, "id")
        })
        .await
    }

    async fn revoke(&self, grant: &str) {
        let client = self.store.client().await.unwrap();
        let definition = self.store.definition().unwrap();
        client.db().revoke_grant(definition, grant, SystemTime::now()).await.unwrap();
    }

    async fn rows(&self, table: &str, filter: Value) -> Vec<Values> {
        let _ = FindOptions::default();
        rows_of(&self.store, table, filter, None).await
    }

    async fn create(&self, table: &str, row: Value) -> String {
        self.tx(async |r| text(&r.table(table).unwrap().create_one(&values(row)).await.unwrap(), "id"))
            .await
    }

    async fn reports(&self) -> Vec<Values> {
        self.rows("Report", json!({ "jobId": { "eq": self.job } })).await
    }

    async fn attachments(&self, table: &str, key: &str) -> Vec<runtime::orm::rowfeatures::Attachment> {
        let client = self.store.client().await.unwrap();
        let repos = client.db().repositories(self.store.definition().unwrap()).unwrap().as_system();
        repos.attachments().unwrap().list(&RowRef::new(table, key)).await.unwrap()
    }

    async fn backlinks(&self, table: &str, key: &str) -> Vec<(String, String)> {
        let client = self.store.client().await.unwrap();
        let repos = client.db().repositories(self.store.definition().unwrap()).unwrap().as_system();
        repos
            .references()
            .unwrap()
            .referenced_by(&RowRef::new(table, key), None)
            .await
            .unwrap()
            .items
            .into_iter()
            .map(|b| (b.table, b.key))
            .collect()
    }

    async fn message(&self, id: &str) -> Values {
        self.inbound.message(id).await.unwrap().unwrap()
    }

    /// The stub handler of a run request: as the party, through the grant
    /// the request names, a report on the granted job, the message filed
    /// onto both.
    async fn file_on_granted_record(&self, request: RunRequest) {
        let party = self.inbound.principal_of(&request.party, Some(&request.grant)).await.unwrap();
        // The grant ended since the message came: the run reaches nothing.
        let Some(grant) = party.grants.as_ref().and_then(|g| g.first()).cloned() else {
            return;
        };
        let message = self.message(&request.message).await;
        let report = {
            let mut client = self.store.client().await.unwrap();
            let tx = client.transaction().await.unwrap();
            let report = {
                let repos = tx.db().repositories(self.store.definition().unwrap()).unwrap().with_access(AccessCaller::of(Some(&party)));
                repos
                    .table("Report")
                    .unwrap()
                    .create_one(&values(json!({ "jobId": grant.target_id, "body": text(&message, "body") })))
                    .await
                    .unwrap()
            };
            tx.commit().await.unwrap();
            text(&report, "id")
        };
        self.inbound.file(&request.message, &RowRef::new("Report", report), &party, None).await.unwrap();
        self.inbound.file(&request.message, &RowRef::new(grant.target_type, grant.target_id), &party, None).await.unwrap();
    }

    /// Runs every run request that is waiting through the stub handler;
    /// answers how many.
    async fn drain_runs(&self) -> usize {
        let queue = self.inbound.run_queue();
        let mut handled = 0;
        for _ in 0..20 {
            let claims = queue.claim(10, Duration::from_secs(30)).await.unwrap();
            if claims.is_empty() {
                return handled;
            }
            for claim in claims {
                self.file_on_granted_record(claim.message).await;
                queue.complete(&claim.id, &claim.token).await.unwrap();
                handled += 1;
            }
        }
        panic!("the run requests did not drain");
    }

    /// The run requests waiting, read and put back.
    async fn pending_runs(&self) -> Vec<RunRequest> {
        let queue = self.inbound.run_queue();
        let claims = queue.claim(50, Duration::from_secs(30)).await.unwrap();
        for claim in &claims {
            queue.release(&claim.id, &claim.token).await.unwrap();
        }
        claims.into_iter().map(|c| c.message).collect()
    }

    async fn sms_in(&self, from: &str, body: &str, extra: Value) -> InboundOutcome {
        let mut message = json!({ "id": format!("in-{}", nanos()), "from": from, "text": body });
        for (k, v) in extra.as_object().cloned().unwrap_or_default() {
            message[k] = v;
        }
        let request = WebhookRequest {
            headers: BTreeMap::from([("x-relay-token".to_owned(), RELAY_TOKEN.to_owned())]),
            body: json!({ "messages": [message] }).to_string(),
        };
        self.inbound.inbound("SMS", &request).await.unwrap().remove(0)
    }

    async fn mail_in(&self, from: &str, body: &str, extra: Value) -> InboundOutcome {
        let mut mail = json!({
            "id": format!("mail-in-{}", nanos()), "from": from, "to": [INBOX], "subject": "The gate", "text": body,
            "auth": { "spf": "pass", "dkim": "pass", "dmarc": "pass" },
        });
        for (k, v) in extra.as_object().cloned().unwrap_or_default() {
            mail[k] = v;
        }
        let request = WebhookRequest {
            headers: BTreeMap::from([("x-relay-token".to_owned(), RELAY_TOKEN.to_owned())]),
            body: json!({ "mail": [mail] }).to_string(),
        };
        self.inbound.inbound("MAIL", &request).await.unwrap().remove(0)
    }

    fn dee_party(&self) -> PartyRef {
        PartyRef::new("Contact", self.dee.clone())
    }

    async fn send(&self, as_: &Principal, channel: &str, body: &str) -> Result<runtime::orm::inbound::SendOutcome, InboundError> {
        self.inbound.send(SendInput::new(as_.clone(), self.dee_party(), channel, body)).await
    }

    async fn drop(self) {
        let World { backend, store, notifier, inbound, .. } = self;
        drop((store, notifier, inbound));
        backend.drop_database().await;
    }
}

async fn a_text_with_a_photo_becomes_a_row_on_the_granted_record() {
    case("a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached");
    let w = world(false).await;
    let outcome = w
        .sms_in("+1 (555) 010-0011", "Gate fixed, photo attached", json!({ "media": [{ "url": "https://sms.test/media/1", "type": "image/jpeg", "name": "gate.jpg" }] }))
        .await;
    assert_eq!(outcome.outcome, "run", "{outcome:?}");
    assert_eq!(outcome.party, Some(w.dee_party()));
    assert_eq!(outcome.grant.as_deref(), Some(w.grant.as_str()));
    let id = outcome.message.clone().unwrap();
    // The run request, as the agent dispatcher (D158) takes it.
    let pending = w.pending_runs().await;
    assert_eq!(
        pending,
        vec![RunRequest { trigger: "inbound".to_owned(), party: w.dee_party(), message: id.clone(), grant: w.grant.clone() }]
    );
    let message = w.message(&id).await;
    for (member, want) in [("direction", "inbound"), ("via", "sms"), ("fromAddress", DEE), ("disposition", "run"), ("partyTable", "Contact"), ("trust", "verified")] {
        assert_eq!(text(&message, member), want, "{member} of {message:?}");
    }
    assert!(message.get("triage").is_none_or(Value::is_null));
    // The photo is the message's, in the bucket once.
    let photos = w.attachments("Message", &id).await;
    assert_eq!(photos.len(), 1);
    assert_eq!((photos[0].name.as_str(), photos[0].media_type.as_str(), photos[0].size), ("gate.jpg", "image/jpeg", PHOTO.len() as u64));
    assert_eq!(photos[0].content_hash, sha(&PHOTO));
    assert_eq!(w.bucket.objects.lock().unwrap().len(), 1);
    assert_eq!(w.drain_runs().await, 1);
    // A report on the granted job, written as the party, with the photo.
    let reports = w.reports().await;
    assert_eq!(reports.len(), 1);
    answer(
        "a granted party's text",
        json!({
            "outcome": outcome.outcome,
            "reports": reports.iter().map(|r| text(r, "body")).collect::<Vec<_>>(),
            "photos": photos.iter().map(|p| (p.name.clone(), p.size, p.content_hash.clone())).collect::<Vec<_>>(),
        }),
    );
    assert_eq!(text(&reports[0], "body"), "Gate fixed, photo attached");
    let report = text(&reports[0], "id");
    let filed = w.attachments("Report", &report).await;
    assert_eq!((filed[0].name.as_str(), filed[0].content_hash.as_str(), filed[0].object_key.as_str()), ("gate.jpg", sha(&PHOTO).as_str(), photos[0].object_key.as_str()));
    assert_eq!(w.bucket.objects.lock().unwrap().len(), 1);
    // A run request run again, after its handler failed, files nothing
    // twice.
    let filer = w.inbound.principal_of(&w.dee_party(), None).await.unwrap();
    w.inbound.file(&id, &RowRef::new("Report", report.clone()), &filer, None).await.unwrap();
    assert_eq!(w.attachments("Report", &report).await.len(), 1);
    // The message is on each row it references: the job, the report and
    // the party.
    for (table, key) in [("Job", w.job.clone()), ("Report", report.clone()), ("Contact", w.dee.clone())] {
        let backlinks = w.backlinks(table, &key).await;
        assert!(backlinks.contains(&("Message".to_owned(), id.clone())), "{table}: {backlinks:?}");
    }
    // The party reaches the job's reports through its grant, and no other
    // job's.
    let other = w.create("Job", json!({ "title": "Paint the fence" })).await;
    let party = w.inbound.principal_of(&w.dee_party(), None).await.unwrap();
    let mut client = w.store.client().await.unwrap();
    let tx = client.transaction().await.unwrap();
    let refused = {
        let repos = tx.db().repositories(w.store.definition().unwrap()).unwrap().with_access(AccessCaller::of(Some(&party)));
        repos.table("Report").unwrap().create_one(&values(json!({ "jobId": other, "body": "not mine" }))).await
    };
    assert!(refused.is_err(), "{refused:?}");
    drop(tx);
    drop(client);
    w.drop().await;
}

async fn a_text_from_an_unknown_number_lands_in_triage() {
    case("a text from an unknown number lands in triage");
    let w = world(false).await;
    let outcome = w.sms_in("+15550109999", "Gate fixed", json!({ "media": [{ "url": "https://sms.test/media/1", "type": "image/jpeg" }] })).await;
    assert_eq!(outcome.outcome, "unknown");
    assert!(outcome.party.is_none());
    assert!(w.pending_runs().await.is_empty());
    let id = outcome.message.clone().unwrap();
    let message = w.message(&id).await;
    assert_eq!((text(&message, "disposition"), text(&message, "triage"), text(&message, "fromAddress")), ("unknown".into(), "open".into(), "+15550109999".into()));
    assert!(message.get("party").is_none_or(Value::is_null));
    // Its photo is filed to its conversation, held with it.
    assert_eq!(w.attachments("Message", &id).await.len(), 1);
    let held = w.inbound.triage(&w.staff, None).await.unwrap();
    assert_eq!(held.iter().map(|m| text(m, "id")).collect::<Vec<_>>(), vec![id.clone()]);
    // Reading triage is a permission.
    assert!(matches!(w.inbound.triage(&Principal::new(w.ann.clone(), Vec::<String>::new()), None).await, Err(InboundError::Refused(_))));
    answer(
        "an unknown sender",
        json!({
            "outcome": outcome.outcome,
            "disposition": text(&message, "disposition"),
            "triage": text(&message, "triage"),
            "held": held.len(),
            "runs": w.pending_runs().await.len(),
        }),
    );
    w.inbound.settle(&w.staff, &id).await.unwrap();
    assert!(w.inbound.triage(&w.staff, None).await.unwrap().is_empty());
    assert!(w.reports().await.is_empty());
    w.drop().await;
}

async fn a_lapsed_identity_reaches_nothing_until_verified_again() {
    case("the same text from a party whose grant has ended reaches nothing, until its identity is verified again");
    let w = world(false).await;
    // A run request made before the grant ended runs as nothing after.
    w.sms_in(DEE, "Gate fixed", json!({})).await;
    w.revoke(&w.grant).await;
    assert_eq!(w.drain_runs().await, 1);
    assert!(w.reports().await.is_empty());
    // The number may be someone else's now: held as no one's.
    let outcome = w.sms_in(DEE, "Gate fixed", json!({})).await;
    assert_eq!(outcome.outcome, "lapsed");
    assert!(outcome.party.is_none());
    assert!(w.pending_runs().await.is_empty());
    let id = outcome.message.clone().unwrap();
    let message = w.message(&id).await;
    assert_eq!((text(&message, "disposition"), text(&message, "triage")), ("lapsed".into(), "open".into()));
    assert!(message.get("party").is_none_or(Value::is_null));
    let timeline = w.inbound.timeline(&w.staff, &w.dee_party(), None).await.unwrap();
    assert!(!timeline.iter().any(|m| text(m, "id") == id));
    assert!(!w.backlinks("Contact", &w.dee).await.iter().any(|(_, key)| *key == id));
    // Nothing is sent to it either.
    let refused = w.send(&w.staff, "sms", "Are you there?").await.unwrap();
    assert!(refused.detail.as_deref().unwrap_or_default().contains("lapsed"), "{refused:?}");
    answer(
        "a party whose grant ended",
        json!({
            "outcome": outcome.outcome,
            "disposition": text(&message, "disposition"),
            "reports": w.reports().await.len(),
            "onTimeline": timeline.iter().any(|m| text(m, "id") == id),
            "send": refused.detail,
        }),
    );
    // Verified again by the code sent to it, the party is itself again,
    // with no grant.
    let identity = w.rows("ChannelIdentity", json!({ "kind": { "eq": "phone" }, "address": { "eq": DEE } })).await.remove(0);
    w.notifier.start_verification(&text(&identity, "id"), "verify").await.unwrap();
    let sent = w.relays.sms();
    let code: String = sent.last().unwrap().text.chars().filter(char::is_ascii_digit).collect();
    let code = &code[code.len() - 6..];
    let verified = w.sms_in(DEE, code, json!({})).await;
    assert_eq!(verified.outcome, "verified", "{verified:?}");
    let again = w.sms_in(DEE, "Gate fixed", json!({})).await;
    assert_eq!(again.outcome, "timeline", "{again:?}");
    assert!(w.pending_runs().await.is_empty());
    w.drop().await;
}

async fn email_failing_dmarc_is_unknown_and_an_authenticated_email_threads() {
    case("email failing DMARC is treated as unknown, and an authenticated email threads to its party");
    let w = world(false).await;
    let spoofed = w.mail_in("Dee@Example.test", "Gate fixed", json!({ "auth": { "spf": "pass", "dkim": "pass", "dmarc": "fail" } })).await;
    assert_eq!(spoofed.outcome, "unknown");
    assert!(spoofed.detail.as_deref().unwrap_or_default().contains("dmarc=fail"), "{spoofed:?}");
    assert!(w.pending_runs().await.is_empty());
    // No authentication reported is none passed.
    let bare = w.mail_in("dee@example.test", "Gate fixed", json!({ "auth": null })).await;
    assert_eq!(bare.outcome, "unknown");
    // Mail to another address is not the deployment's.
    let elsewhere = w.mail_in("dee@example.test", "Gate fixed", json!({ "to": ["other@example.test"] })).await;
    assert_eq!(elsewhere.outcome, "ignored");
    // The staff write; the party's reply threads to the same conversation.
    let mut input = SendInput::new(w.staff.clone(), w.dee_party(), "email", "Is the gate fixed?");
    input.subject = Some("The gate".to_owned());
    input.about = vec![RowRef::new("Job", w.job.clone())];
    let sent = w.inbound.send(input).await.unwrap();
    assert_eq!(sent.outcome, "sent", "{sent:?}");
    let out = w.relays.mail().last().cloned().unwrap();
    assert_eq!((out.to.as_str(), out.subject.as_str()), ("dee@example.test", "The gate"));
    assert_eq!(out.reply_to.as_deref(), Some(INBOX));
    let message_id = out.message_id.clone().unwrap();
    assert!(message_id.starts_with('<') && message_id.ends_with("@example.test>"), "{message_id}");
    let answer = w
        .mail_in(
            "dee@example.test",
            "Yes, photo attached",
            json!({ "messageId": "<r1@mail.example>", "inReplyTo": message_id, "references": [message_id], "attachments": [{ "name": "gate.jpg", "type": "image/jpeg" }] }),
        )
        .await;
    assert_eq!(answer.outcome, "run", "{answer:?}");
    let outbound = w.message(sent.message.as_deref().unwrap()).await;
    assert_eq!(answer.conversation.as_deref(), Some(text(&outbound, "conversation").as_str()));
    assert_eq!(text(&w.message(answer.message.as_deref().unwrap()).await, "inReplyTo"), message_id);
    assert_eq!(w.drain_runs().await, 1);
    let report = text(&w.reports().await[0], "id");
    assert_eq!(w.attachments("Report", &report).await[0].content_hash, sha(&PHOTO));
    // The outbound message is on the job it was about.
    assert!(w.backlinks("Job", &w.job).await.iter().any(|(_, key)| Some(key.as_str()) == sent.message.as_deref()));
    w.drop().await;
}

async fn a_party_with_no_grant_is_on_its_timeline() {
    case("a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply");
    let w = world(false).await;
    let eve = w.create("Contact", json!({ "name": "Eve" })).await;
    w.create(
        "ChannelIdentity",
        json!({ "kind": "phone", "address": EVE, "principalKind": "party", "principalTable": "Contact", "principal": eve, "trust": "entered", "verifiedAt": "2026-01-01T00:00:00Z" }),
    )
    .await;
    let outcome = w.sms_in(EVE, "Can I quote for the fence?", json!({})).await;
    assert_eq!(outcome.outcome, "timeline");
    assert_eq!(outcome.party, Some(PartyRef::new("Contact", eve.clone())));
    let id = outcome.message.clone().unwrap();
    let message = w.message(&id).await;
    assert_eq!((text(&message, "disposition"), text(&message, "triage"), text(&message, "trust")), ("timeline".into(), "open".into(), "entered".into()));
    let timeline = w.inbound.timeline(&w.staff, &PartyRef::new("Contact", eve.clone()), None).await.unwrap();
    assert_eq!(timeline.iter().map(|m| text(m, "id")).collect::<Vec<_>>(), vec![id.clone()]);
    let triage = w.inbound.triage(&w.staff, None).await.unwrap();
    assert_eq!(triage.iter().map(|m| text(m, "id")).collect::<Vec<_>>(), vec![id.clone()]);
    assert_eq!(w.backlinks("Contact", &eve).await.into_iter().map(|(_, k)| k).collect::<Vec<_>>(), vec![id.clone()]);
    assert!(w.pending_runs().await.is_empty());
    // A user's text is a reply, which the notifier runs as the user.
    let reply = w.sms_in(ANN, "ack", json!({})).await;
    assert_eq!(reply.outcome, "reply");
    let principal = reply.reply.as_ref().and_then(|r| r.principal.clone()).unwrap();
    assert_eq!((principal.kind.as_str(), principal.id.as_str()), ("user", w.ann.as_str()));
    assert!(reply.message.is_none());
    w.drop().await;
}

async fn two_parties_on_one_address_are_told_apart_by_the_thread() {
    case("two parties sharing an address are told apart by the thread, and held in triage otherwise");
    let w = world(false).await;
    let fay = w.create("Contact", json!({ "name": "Fay" })).await;
    let gus = w.create("Contact", json!({ "name": "Gus" })).await;
    let fence = w.create("Job", json!({ "title": "Paint the fence" })).await;
    for party in [&fay, &gus] {
        w.create(
            "ChannelIdentity",
            json!({ "kind": "email", "address": "office@example.test", "principalKind": "party", "principalTable": "Contact", "principal": party, "trust": "verified", "verifiedAt": "2026-01-01T00:00:00Z" }),
        )
        .await;
    }
    w.grant_to(&fay, "Job", &w.job).await;
    let gus_grant = w.grant_to(&gus, "Job", &fence).await;
    let unthreaded = w.mail_in("office@example.test", "Done", json!({})).await;
    assert_eq!(unthreaded.outcome, "unknown");
    assert!(unthreaded.detail.as_deref().unwrap_or_default().contains("shared by 2 parties"), "{unthreaded:?}");
    let mut input = SendInput::new(w.staff.clone(), PartyRef::new("Contact", gus.clone()), "email", "How is the fence?");
    input.subject = Some("The fence".to_owned());
    let sent = w.inbound.send(input).await.unwrap();
    let message_id = w.relays.mail().last().unwrap().message_id.clone().unwrap();
    let threaded = w.mail_in("office@example.test", "Fence painted", json!({ "inReplyTo": message_id })).await;
    assert_eq!(threaded.outcome, "run", "{threaded:?}");
    assert_eq!(threaded.party, Some(PartyRef::new("Contact", gus.clone())));
    assert_eq!(threaded.grant.as_deref(), Some(gus_grant.as_str()));
    let conversation = text(&w.message(sent.message.as_deref().unwrap()).await, "conversation");
    assert_eq!(threaded.conversation.as_deref(), Some(conversation.as_str()));
    assert_eq!(w.drain_runs().await, 1);
    let painted: Vec<String> = w.rows("Report", json!({ "jobId": { "eq": fence } })).await.iter().map(|r| text(r, "body")).collect();
    assert_eq!(painted, vec!["Fence painted".to_owned()]);
    assert!(w.reports().await.is_empty());
    w.drop().await;
}

async fn sending_takes_the_permission_rate_limit_approval_and_stop() {
    case("sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP");
    let w = world(true).await;
    assert!(matches!(w.send(&Principal::new(w.ann.clone(), Vec::<String>::new()), "sms", "Hello").await, Err(InboundError::Refused(_))));
    // An agent run sends for the person it acts for.
    let agent = Principal::new(uuid::Uuid::new_v4().to_string(), ["parties.message"])
        .of_kind(PrincipalKind::Agent)
        .acting_for(ActingFor::user(w.ann.clone()));
    let first = w.send(&agent, "sms", "The gate is booked for Monday.").await.unwrap();
    assert_eq!(first.outcome, "sent", "{first:?}");
    assert_eq!(w.relays.sms().iter().map(|m| m.to.clone()).collect::<Vec<_>>(), vec![DEE.to_owned()]);
    let row = w.message(first.message.as_deref().unwrap()).await;
    for (member, want) in [("direction", "outbound"), ("state", "sent"), ("author", agent.subject.as_str()), ("authorKind", "agent"), ("forSubject", w.ann.as_str()), ("via", "sms")] {
        assert_eq!(text(&row, member), want, "{member} of {row:?}");
    }
    // Asked: held until approved.
    let asked = w.send(&w.staff, "sms", "Quote: 400 for the gate.").await.unwrap();
    assert_eq!(asked.outcome, "awaitingApproval");
    assert_eq!(w.relays.sms().len(), 1);
    assert_eq!(w.inbound.release(asked.message.as_deref().unwrap()).await.unwrap().outcome, "sent");
    assert_eq!(w.relays.sms().len(), 2);
    let denied = w.send(&w.staff, "sms", "Refund issued.").await.unwrap();
    assert_eq!((denied.outcome.as_str(), denied.detail.as_deref()), ("refused", Some("the approval policy refuses it")));
    assert_eq!(w.send(&w.staff, "sms", "See you Monday.").await.unwrap().outcome, "sent");
    let limited = w.send(&w.staff, "sms", "One more thing.").await.unwrap();
    assert_eq!(limited.outcome, "refused");
    assert!(limited.detail.as_deref().unwrap_or_default().contains("the most it may be"), "{limited:?}");
    assert_eq!(w.relays.sms().len(), 3);
    // STOP is the adapter's to read and the core's to keep.
    let stop = w.sms_in(DEE, "STOP", json!({})).await;
    assert_eq!(stop.outcome, "optOut");
    let identity = w.rows("ChannelIdentity", json!({ "kind": { "eq": "phone" }, "address": { "eq": DEE } })).await.remove(0);
    assert!(identity.get("optedOutAt").is_some_and(|at| !at.is_null()));
    assert!(w.pending_runs().await.is_empty());
    assert_eq!(w.send(&w.staff, "sms", "Still there?").await.unwrap().detail.as_deref(), Some("the address opted out"));
    let start = w.sms_in(DEE, "start", json!({})).await;
    assert_eq!(start.outcome, "optOut");
    let welcome = w.send(&w.staff, "sms", "Welcome back.").await.unwrap();
    assert!(welcome.detail.as_deref().unwrap_or_default().contains("the most it may be"), "{welcome:?}");
    assert_eq!(w.relays.sms().len(), 3);
    assert!(!format!("{:?}{:?}", w.relays.sms(), w.relays.mail()).contains(RELAY_TOKEN));
    w.drop().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn parties_end_to_end() {
    a_text_with_a_photo_becomes_a_row_on_the_granted_record().await;
    a_text_from_an_unknown_number_lands_in_triage().await;
    a_lapsed_identity_reaches_nothing_until_verified_again().await;
    email_failing_dmarc_is_unknown_and_an_authenticated_email_threads().await;
    a_party_with_no_grant_is_on_its_timeline().await;
    two_parties_on_one_address_are_told_apart_by_the_thread().await;
    sending_takes_the_permission_rate_limit_approval_and_stop().await;
}
