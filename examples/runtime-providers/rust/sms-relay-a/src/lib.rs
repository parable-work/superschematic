//! A channel adapter for an SMS relay that takes JSON (D159, D185): the
//! Rust port of `providers/sms-relay-a`. It sends a POST to
//! `<URL>/v1/messages` with a Bearer token, and reads the relay's
//! callbacks, delivery statuses and inbound messages, from the request a
//! `@webhook` route received, after checking the relay's HMAC-SHA256
//! signature of the body with the same token; STOP and START it reads as
//! an opt-out and an opt-in (D157).
//!
//! The relay is reached through a [`Relay`]: [`definition`], which a
//! server links, sends through the process's [`outbox`], a stub of the
//! relay that answers each send and keeps the requests it was sent, since
//! this is an example; [`definition_with`] takes any other.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use serde_json::{json, Value};
use superschematic_http_runtime::providers::{
    ChannelAdapter, ChannelHealth, DeliveryState, DeliveryStatus, InboundMessage, OptOut,
    OutboundMessage, ProviderDefinition, ProviderInstance, ProviderSettingSpec, RuntimeInterface,
    SecretValue, SendResult, WebhookRequest, CALLER_KEY,
};
use superschematic_http_runtime::BoxError;

/// A request to the relay: its method, URL, headers and body.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Request {
    pub method: String,
    pub url: String,
    pub headers: BTreeMap<String, String>,
    pub body: String,
}

/// What reaches the relay.
#[async_trait]
pub trait Relay: Send + Sync {
    /// The relay's answer to `request`: its status and its JSON body.
    async fn call(&self, request: Request) -> Result<(u16, Value), BoxError>;
}

/// The relay as a stub in the process: it answers a send `accepted`, with
/// the id `a-<n>`, and keeps every request it was sent.
#[derive(Default)]
pub struct Outbox {
    sent: Mutex<Vec<Request>>,
}

impl Outbox {
    /// The requests the relay was sent, oldest first.
    pub fn sent(&self) -> Vec<Request> {
        self.sent.lock().unwrap().clone()
    }
}

#[async_trait]
impl Relay for Outbox {
    async fn call(&self, request: Request) -> Result<(u16, Value), BoxError> {
        let mut sent = self.sent.lock().unwrap();
        let health = request.method == "GET";
        sent.push(request);
        if health {
            return Ok((200, json!({ "ok": true })));
        }
        Ok((
            200,
            json!({ "id": format!("a-{}", sent.len()), "status": "accepted" }),
        ))
    }
}

/// The process's relay stub, which [`definition`] sends through.
pub fn outbox() -> Arc<Outbox> {
    static OUTBOX: OnceLock<Arc<Outbox>> = OnceLock::new();
    OUTBOX.get_or_init(|| Arc::new(Outbox::default())).clone()
}

/// The keywords SMS carriers treat as opting out of, and back into, a
/// sender's messages (D157).
const OPT_OUT: [&str; 6] = ["STOP", "STOPALL", "UNSUBSCRIBE", "CANCEL", "END", "QUIT"];
const OPT_IN: [&str; 2] = ["START", "UNSTOP"];

/// The relay's states, as a delivery state.
fn state(status: &str) -> DeliveryState {
    match status {
        "sent" => DeliveryState::Sent,
        "delivered" => DeliveryState::Delivered,
        "undelivered" | "failed" => DeliveryState::Failed,
        _ => DeliveryState::Queued,
    }
}

/// The relay's signature of a callback's body: its HMAC-SHA256 under the
/// token, in hex.
pub fn signature(token: &str, body: &str) -> String {
    let key = ring::hmac::Key::new(ring::hmac::HMAC_SHA256, token.as_bytes());
    ring::hmac::sign(&key, body.as_bytes())
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// A time the relay writes, in epoch seconds.
fn at(value: &Value) -> SystemTime {
    UNIX_EPOCH + Duration::from_secs(value.as_u64().unwrap_or(0))
}

struct Adapter {
    base: String,
    token: SecretValue,
    sender: Option<String>,
    relay: Arc<dyn Relay>,
}

impl Adapter {
    fn verified(&self, request: &WebhookRequest) -> Result<Value, BoxError> {
        let signed = request
            .headers
            .get("x-relay-signature")
            .map_or("", String::as_str);
        if signed != signature(self.token.reveal(), &request.body) {
            return Err("sms-relay-a: the callback is not signed with the relay token".into());
        }
        Ok(serde_json::from_str(&request.body)?)
    }

    fn bearer(&self) -> (String, String) {
        (
            "authorization".to_owned(),
            format!("Bearer {}", self.token.reveal()),
        )
    }
}

fn text(value: &Value) -> String {
    value.as_str().unwrap_or_default().to_owned()
}

#[async_trait]
impl ChannelAdapter for Adapter {
    fn kind(&self) -> &str {
        "sms"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let mut body = json!({ "to": message.to, "text": message.body });
        if let Some(sender) = &self.sender {
            body["from"] = json!(sender);
        }
        if let Some(reference) = &message.reference {
            body["reference"] = json!(reference);
        }
        let mut headers = BTreeMap::from([
            self.bearer(),
            ("content-type".to_owned(), "application/json".to_owned()),
        ]);
        if let Some(key) = &message.idempotency_key {
            headers.insert("idempotency-key".into(), key.clone());
        }
        let request = Request {
            method: "POST".into(),
            url: format!("{}/v1/messages", self.base),
            headers,
            body: body.to_string(),
        };
        let (status, sent) = self.relay.call(request).await?;
        if status != 200 {
            return Err(format!("sms-relay-a: the relay answered {status}").into());
        }
        Ok(SendResult {
            provider_message_id: text(&sent["id"]),
            state: state(&text(&sent["status"])),
        })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        let request = Request {
            method: "GET".into(),
            url: format!("{}/v1/health", self.base),
            headers: BTreeMap::from([self.bearer()]),
            body: String::new(),
        };
        let (status, _) = self.relay.call(request).await?;
        Ok(if status == 200 {
            ChannelHealth {
                ok: true,
                detail: None,
            }
        } else {
            ChannelHealth {
                ok: false,
                detail: Some(format!("the relay answered {status}")),
            }
        })
    }

    async fn status(&self, request: &WebhookRequest) -> Result<Vec<DeliveryStatus>, BoxError> {
        let body = self.verified(request)?;
        Ok(body["events"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|event| DeliveryStatus {
                provider_message_id: text(&event["id"]),
                state: state(&text(&event["status"])),
                at: at(&event["at"]),
                detail: event["error"].as_str().map(str::to_owned),
            })
            .collect())
    }

    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        let body = self.verified(request)?;
        Ok(body["messages"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|message| {
                let said = text(&message["text"]);
                let keyword = said.trim().to_uppercase();
                // The opt-out and opt-in keywords of SMS are the adapter's
                // to read; the core records them.
                let opt_out = if OPT_OUT.contains(&keyword.as_str()) {
                    Some(OptOut::Out)
                } else if OPT_IN.contains(&keyword.as_str()) {
                    Some(OptOut::In)
                } else {
                    None
                };
                InboundMessage {
                    from: text(&message["from"]),
                    body: said,
                    received_at: at(&message["at"]),
                    provider_message_id: text(&message["id"]),
                    in_reply_to: message["replyTo"].as_str().map(str::to_owned),
                    message_id: None,
                    references: Vec::new(),
                    to: Vec::new(),
                    subject: None,
                    authentication: None,
                    attachments: Vec::new(),
                    opt_out,
                }
            })
            .collect())
    }
}

/// The definition, over `relay`.
pub fn definition_with(relay: Arc<dyn Relay>) -> ProviderDefinition {
    ProviderDefinition::new(
        "sms-relay-a",
        RuntimeInterface::Channel,
        move |settings, _| {
            Ok(ProviderInstance::Channel(Arc::new(Adapter {
                base: settings.text("URL")?.trim_end_matches('/').to_owned(),
                token: settings.secret("TOKEN")?,
                sender: settings.optional_text("SENDER")?.map(str::to_owned),
                relay: relay.clone(),
            })))
        },
    )
    .with_setting("URL", ProviderSettingSpec::plain())
    .with_setting("TOKEN", ProviderSettingSpec::secret())
    .with_setting("SENDER", ProviderSettingSpec::plain().optional())
    .with_setting(CALLER_KEY, ProviderSettingSpec::secret().optional())
    .with_role(["messages.relay"])
    .with_operations(["status", "inbound"])
}

/// The definition a server links, over the process's [`outbox`].
pub fn definition() -> ProviderDefinition {
    definition_with(outbox())
}
