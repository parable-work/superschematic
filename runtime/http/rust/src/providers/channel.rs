//! The channel adapter (D159): what notifications (D153) send through. An
//! adapter sends a message and reports its health. One whose service
//! posts delivery statuses and inbound messages back reads them from the
//! request a `@webhook` route received (`status`, `inbound`): the route is
//! the app's, so the service's callback runs through the same operation,
//! permissions and audit as any other caller.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::time::SystemTime;

use crate::BoxError;

/// Where a delivery stands, as the adapter's service reports it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DeliveryState {
    Queued,
    Sent,
    Delivered,
    Read,
    Failed,
}

/// A message to send.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OutboundMessage {
    /// The recipient's address on the channel: a phone number, an email
    /// address, a chat handle.
    pub to: String,
    pub body: String,
    /// A subject, for a channel that shows one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subject: Option<String>,
    /// The reference number a reply names to say what it answers (D153).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reference: Option<String>,
    /// A key the adapter passes to its service so a retried send sends
    /// once.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idempotency_key: Option<String>,
    /// The message's id in the channel's threads (D157): an email's
    /// Message-ID.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub message_id: Option<String>,
    /// The id of the message it answers: an email's In-Reply-To.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub in_reply_to: Option<String>,
    /// The ids of the messages of its thread before it: an email's
    /// References.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    /// Where a reply goes: the deployment's inbound address, for email.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reply_to: Option<String>,
}

/// What `send` answers.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SendResult {
    /// The service's id of the message, which its statuses and replies
    /// name.
    pub provider_message_id: String,
    pub state: DeliveryState,
}

/// One delivery status a service reported.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DeliveryStatus {
    pub provider_message_id: String,
    pub state: DeliveryState,
    #[serde(with = "super::time")]
    pub at: SystemTime,
    /// Why a delivery failed, in the service's words.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// An opt-out or opt-in keyword of the channel the adapter read (D157):
/// `out` for STOP and its kin, `in` for START.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum OptOut {
    Out,
    In,
}

/// An email's authentication results, as an inbound adapter's service
/// reports them (D157): each pass, fail, softfail, neutral, none,
/// temperror or permerror.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct InboundAuthentication {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub spf: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dkim: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dmarc: Option<String>,
}

/// A file an inbound message came with, its bytes fetched by the adapter.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InboundAttachment {
    pub name: String,
    pub media_type: String,
    pub bytes: Vec<u8>,
}

/// One message a service received for the app.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InboundMessage {
    /// The sender's address on the channel.
    pub from: String,
    pub body: String,
    pub received_at: SystemTime,
    pub provider_message_id: String,
    /// The id of the message it replies to, when the service threads
    /// replies.
    pub in_reply_to: Option<String>,
    /// The message's own id in the channel's threads (D157).
    pub message_id: Option<String>,
    /// The ids of the messages of its thread before it.
    pub references: Vec<String>,
    /// The addresses it was sent to.
    pub to: Vec<String>,
    /// Its subject, for a channel that has one.
    pub subject: Option<String>,
    /// What the service found of an email's authentication (D157); the
    /// adapter reports, the core decides.
    pub authentication: Option<InboundAuthentication>,
    /// The files it came with.
    pub attachments: Vec<InboundAttachment>,
    /// An opt-out or opt-in keyword it carried (D157).
    pub opt_out: Option<OptOut>,
}

/// A request a `@webhook` route received from the adapter's service, as
/// the route read it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WebhookRequest {
    /// The request's headers, names in lower case.
    pub headers: BTreeMap<String, String>,
    /// The raw body, which a signature covers.
    pub body: String,
}

/// What `health` answers. An adapter that is not ok is skipped, and the
/// others carry the alert that it is down.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ChannelHealth {
    pub ok: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// A channel adapter. `status` and `inbound` are optional: an adapter
/// that implements one says so in its definition's `operations` and its
/// manifest's, and one that does not answers
/// [`Unsupported`](super::Unsupported).
#[async_trait]
pub trait ChannelAdapter: Send + Sync {
    /// The kind of channel it sends on: `sms`, `email`, `chat`. A channel
    /// kind's trust level is data (D153).
    fn kind(&self) -> &str;

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError>;

    async fn health(&self) -> Result<ChannelHealth, BoxError>;

    /// The delivery statuses a callback request carries.
    async fn status(&self, request: &WebhookRequest) -> Result<Vec<DeliveryStatus>, BoxError> {
        let _ = request;
        Err(super::unsupported("channel adapter", "status"))
    }

    /// The messages an inbound request carries.
    async fn inbound(&self, request: &WebhookRequest) -> Result<Vec<InboundMessage>, BoxError> {
        let _ = request;
        Err(super::unsupported("channel adapter", "inbound"))
    }
}
