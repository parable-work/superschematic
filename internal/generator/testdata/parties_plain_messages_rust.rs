//! fixture-parties-plain-messages-db's inbound path in the Rust
//! repositories and runtime (D157, D193, #618), run by
//! TestRustPartiesPlainMessages: the messages keep references and hold no
//! files, and the reports hold files. A party's text is filed onto a
//! report and onto the granted job once, with no error: filing asks no
//! table for files it does not hold, so the run request is handled the
//! first time and is not run again. The case runs on the database
//! RUST_E2E_STORE names (tests/backend): a SQLite file with the build's
//! SQLite DDL applied, or a schema of the test's own at
//! RUST_E2E_DATABASE_URL with create.sql applied; it prints its answer,
//! which the Go test holds equal on the two databases.

mod backend;

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
use backend::{answer, in_tx, Backend};
use runtime::access::IssueGrant;
use runtime::bucket::{
    Body, Bucket, BucketError, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions,
};
use runtime::orm::inbound::Inbound;
use runtime::orm::notify::Notifier;
use runtime::orm::parties::PartyRef;
use runtime::orm::rowfeatures::RowRef;
use runtime::orm::store::Store;
use runtime::orm::{AccessCaller, Values};
use runtime::providers::{
    providers_from, ChannelAdapter, ChannelHealth, ConfigValue, InboundAttachment, InboundMessage,
    OutboundMessage, ProviderDefinition, ProviderInstance, ProvidersOptions, RuntimeInterface,
    SendResult, WebhookRequest,
};
use runtime::queue::QueueDefinition;
use runtime::{BoxError, PrincipalKind};
use serde_json::{json, Value};

const RELAY_TOKEN: &str = "relay-token";
const PHOTO: [u8; 8] = [0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4];
const DEE: &str = "+15550100011";

fn values(value: Value) -> Values {
    value.as_object().cloned().unwrap()
}

fn text(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

/// A stand-in SMS relay: it reads an inbound text and the photo it came
/// with, and sends nothing.
struct SmsRelay;

#[async_trait]
impl ChannelAdapter for SmsRelay {
    fn kind(&self) -> &str {
        "sms"
    }

    async fn send(&self, _message: &OutboundMessage) -> Result<SendResult, BoxError> {
        Err("the relay sends nothing here".into())
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        Ok(ChannelHealth { ok: true, detail: None })
    }

    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        if request.headers.get("x-relay-token").map(String::as_str) != Some(RELAY_TOKEN) {
            return Err("the callback holds no relay token".into());
        }
        let body: Value = serde_json::from_str(&request.body)?;
        let mut out = Vec::new();
        for message in body["messages"].as_array().cloned().unwrap_or_default() {
            let attachments = message["media"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .iter()
                .map(|media| InboundAttachment {
                    name: media["name"].as_str().unwrap_or_default().to_owned(),
                    media_type: media["type"].as_str().unwrap_or_default().to_owned(),
                    bytes: PHOTO.to_vec(),
                })
                .collect();
            out.push(InboundMessage {
                from: message["from"].as_str().unwrap_or_default().to_owned(),
                body: message["text"].as_str().unwrap_or_default().to_owned(),
                received_at: SystemTime::now(),
                provider_message_id: message["id"].as_str().unwrap_or_default().to_owned(),
                in_reply_to: None,
                message_id: None,
                references: Vec::new(),
                to: Vec::new(),
                subject: None,
                authentication: None,
                attachments,
                opt_out: None,
            });
        }
        Ok(out)
    }
}

/// A bucket no file reaches: the messages hold none.
struct NoBucket;

#[async_trait]
impl Bucket for NoBucket {
    fn name(&self) -> &str {
        "memory/files"
    }

    async fn put(&self, _name: &str, _body: Body, _options: PutOptions) -> Result<PutResult, BucketError> {
        panic!("no file reaches the bucket")
    }

    async fn head(&self, _name: &str) -> Result<Option<Object>, BucketError> {
        Ok(None)
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        Err(BucketError::NotFound { bucket: "memory/files".into(), object: name.into() })
    }

    async fn delete(&self, _name: &str) -> Result<(), BucketError> {
        Ok(())
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        Ok(Page::default())
    }

    async fn signed_url(&self, name: &str, options: SignedUrlOptions) -> Result<String, BucketError> {
        Ok(format!("https://files.test/{name}?method={}", options.method.as_str()))
    }
}

async fn backlinks(store: &Store, table: &str, key: &str) -> Vec<String> {
    let client = store.client().await.unwrap();
    let repos = client.db().repositories(store.definition().unwrap()).unwrap().as_system();
    repos
        .references()
        .unwrap()
        .referenced_by(&RowRef::new(table, key), None)
        .await
        .unwrap()
        .items
        .into_iter()
        .filter(|b| b.table == "Message")
        .map(|b| b.key)
        .collect()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn parties_plain_messages() {
    println!("case: a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files");
    let backend = Backend::open("parties_plain_rust", include_str!("../create.sqlite.sql"), include_str!("../create.sql")).await;
    let store = backend.store();

    let providers = Arc::new(
        providers_from(ProvidersOptions {
            slots: BTreeMap::from([("SMS".to_owned(), "channel".to_owned())]),
            config: BTreeMap::from([("SMS".to_owned(), ConfigValue::Text("sms-relay".to_owned()))]),
            definitions: vec![ProviderDefinition::new("sms-relay", RuntimeInterface::Channel, |_, _| {
                Ok(ProviderInstance::Channel(Arc::new(SmsRelay)))
            })
            .with_operations(["inbound"])],
            manifests: BTreeMap::new(),
        })
        .unwrap(),
    );
    let dispatch = QueueDefinition::parse(types::queues::NOTICE_WORK_QUEUE).unwrap();
    let notifier = Notifier::new(store.clone(), dispatch).unwrap().with_providers(providers.clone());
    let runs = QueueDefinition::parse(types::queues::RUN_REQUEST_QUEUE).unwrap();
    let inbound = Inbound::new(store.clone(), notifier, runs)
        .unwrap()
        .with_providers(providers)
        .with_bucket(Arc::new(NoBucket));

    let (dee, job, grant) = in_tx(&store, async |r| {
        r.table("Role")
            .unwrap()
            .create_one(&values(json!({ "name": "contractor", "permissions": ["reports.write"], "reaches": ["Report:job"] })))
            .await
            .unwrap();
        let dee = text(&r.table("Contact").unwrap().create_one(&values(json!({ "name": "Dee" }))).await.unwrap(), "id");
        let job = text(&r.table("Job").unwrap().create_one(&values(json!({ "title": "Fix the gate" }))).await.unwrap(), "id");
        r.table("ChannelIdentity")
            .unwrap()
            .create_one(&values(json!({
                "kind": "phone", "address": DEE, "principalKind": "party", "principalTable": "Contact",
                "principal": dee, "trust": "verified", "verifiedAt": "2026-01-01T00:00:00Z",
            })))
            .await
            .unwrap();
        let issued = r
            .issue_grant(&IssueGrant {
                holder: dee.clone(),
                holder_kind: PrincipalKind::Party,
                role: "contractor".to_owned(),
                target_type: "Job".to_owned(),
                target_id: job.clone(),
                starts_at: None,
                ends_at: None,
                ends_when: None,
            })
            .await
            .unwrap();
        (dee, job, text(&issued.grant, "id"))
    })
    .await;

    let request = WebhookRequest {
        headers: BTreeMap::from([("x-relay-token".to_owned(), RELAY_TOKEN.to_owned())]),
        body: json!({ "messages": [{ "id": "in-1", "from": DEE, "text": "Gate fixed", "media": [{ "url": "https://sms.test/media/1", "type": "image/jpeg", "name": "gate.jpg" }] }] }).to_string(),
    };
    let outcome = inbound.inbound("SMS", &request).await.unwrap().remove(0);
    assert_eq!(outcome.outcome, "run", "{outcome:?}");
    assert_eq!(outcome.grant.as_deref(), Some(grant.as_str()));
    let id = outcome.message.clone().unwrap();
    // The photo is dropped, and the message says why: its table holds no
    // files.
    let message = inbound.message(&id).await.unwrap().unwrap();
    assert!(text(&message, "detail").contains("no @attachments"), "{message:?}");

    // The run request's handler, as the party through its grant: a report
    // on the granted job, the message filed onto it and onto the job. A
    // handler that fails is a run request run again.
    let queue = inbound.run_queue();
    let claims = queue.claim(10, Duration::from_secs(30)).await.unwrap();
    assert_eq!(claims.len(), 1);
    let claim = &claims[0];
    let party = inbound.principal_of(&claim.message.party, Some(&claim.message.grant)).await.unwrap();
    let granted = party.grants.as_ref().and_then(|g| g.first()).cloned().unwrap();
    let report = {
        let mut client = store.client().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let report = {
            let repos = tx.db().repositories(store.definition().unwrap()).unwrap().with_access(AccessCaller::of(Some(&party)));
            repos
                .table("Report")
                .unwrap()
                .create_one(&values(json!({ "jobId": granted.target_id, "body": "Gate fixed" })))
                .await
                .unwrap()
        };
        tx.commit().await.unwrap();
        text(&report, "id")
    };
    inbound.file(&claim.message.message, &RowRef::new("Report", report.clone()), &party, None).await.unwrap();
    inbound.file(&claim.message.message, &RowRef::new(granted.target_type.clone(), granted.target_id.clone()), &party, None).await.unwrap();
    queue.complete(&claim.id, &claim.token).await.unwrap();
    // Nothing is left to run again.
    assert!(queue.claim(10, Duration::from_secs(30)).await.unwrap().is_empty());

    // The message is on the report, the job and the party, once each, and
    // the report holds no file of it.
    for (table, key) in [("Report", report.clone()), ("Job", job.clone()), ("Contact", dee.clone())] {
        assert_eq!(backlinks(&store, table, &key).await, vec![id.clone()], "{table}");
    }
    let files = {
        let client = store.client().await.unwrap();
        let repos = client.db().repositories(store.definition().unwrap()).unwrap().as_system();
        repos.attachments().unwrap().list(&RowRef::new("Report", report.clone())).await.unwrap()
    };
    assert!(files.is_empty(), "{files:?}");
    // Filing it again changes nothing.
    let filer = inbound.principal_of(&PartyRef::new("Contact", dee.clone()), None).await.unwrap();
    inbound.file(&id, &RowRef::new("Report", report.clone()), &filer, None).await.unwrap();
    assert_eq!(backlinks(&store, "Report", &report).await, vec![id.clone()]);
    answer(
        "a text filed onto rows when the messages hold no files",
        json!({
            "outcome": outcome.outcome,
            "reportBacklinks": backlinks(&store, "Report", &report).await.len(),
            "jobBacklinks": backlinks(&store, "Job", &job).await.len(),
            "reportFiles": files.len(),
        }),
    );

    drop((store, inbound));
    backend.drop_database().await;
}
