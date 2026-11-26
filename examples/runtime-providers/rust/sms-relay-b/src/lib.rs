//! A second SMS channel adapter (D159, D185), for a relay of another
//! shape: form posts to `<URL>/accounts/<ACCOUNT>/send` with basic
//! authentication, and no callbacks. The Rust port of
//! `providers/sms-relay-b`. An environment swaps sms-relay-a for it by its
//! config alone: the slot's value and the settings this one reads.
//!
//! [`definition`], which a server links, sends through the process's
//! [`outbox`], a stub of the relay, since this is an example.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, OnceLock};

use async_trait::async_trait;
use base64::Engine as _;
use superschematic_http_runtime::providers::{
    ChannelAdapter, ChannelHealth, DeliveryState, OutboundMessage, ProviderDefinition,
    ProviderInstance, ProviderSettingSpec, RuntimeInterface, SecretValue, SendResult,
};
use superschematic_http_runtime::BoxError;

/// A request the relay was sent: its URL, headers and form body.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Request {
    pub url: String,
    pub headers: BTreeMap<String, String>,
    pub body: String,
}

/// The relay as a stub in the process: it answers each send with the sid
/// `b-<n>` and keeps every request it was sent.
#[derive(Default)]
pub struct Outbox {
    sent: Mutex<Vec<Request>>,
}

impl Outbox {
    /// The requests the relay was sent, oldest first.
    pub fn sent(&self) -> Vec<Request> {
        self.sent.lock().unwrap().clone()
    }

    fn send(&self, request: Request) -> String {
        let mut sent = self.sent.lock().unwrap();
        sent.push(request);
        format!("b-{}", sent.len())
    }
}

/// The process's relay stub, which [`definition`] sends through.
pub fn outbox() -> Arc<Outbox> {
    static OUTBOX: OnceLock<Arc<Outbox>> = OnceLock::new();
    OUTBOX.get_or_init(|| Arc::new(Outbox::default())).clone()
}

/// A form value, percent-encoded as `application/x-www-form-urlencoded`
/// writes it.
fn form(value: &str) -> String {
    value
        .bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'*' => {
                char::from(b).to_string()
            }
            b' ' => "+".to_owned(),
            other => format!("%{other:02X}"),
        })
        .collect()
}

struct Adapter {
    base: String,
    account: String,
    token: SecretValue,
    outbox: Arc<Outbox>,
}

impl Adapter {
    fn authorization(&self) -> String {
        let pair = format!("{}:{}", self.account, self.token.reveal());
        format!(
            "Basic {}",
            base64::engine::general_purpose::STANDARD.encode(pair)
        )
    }
}

#[async_trait]
impl ChannelAdapter for Adapter {
    fn kind(&self) -> &str {
        "sms"
    }

    async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
        let sid = self.outbox.send(Request {
            url: format!("{}/accounts/{}/send", self.base, form(&self.account)),
            headers: BTreeMap::from([
                ("authorization".to_owned(), self.authorization()),
                (
                    "content-type".to_owned(),
                    "application/x-www-form-urlencoded".to_owned(),
                ),
            ]),
            body: format!("To={}&Body={}", form(&message.to), form(&message.body)),
        });
        Ok(SendResult {
            provider_message_id: sid,
            state: DeliveryState::Sent,
        })
    }

    async fn health(&self) -> Result<ChannelHealth, BoxError> {
        Ok(ChannelHealth {
            ok: true,
            detail: None,
        })
    }
}

/// The definition, over `outbox`.
pub fn definition_with(outbox: Arc<Outbox>) -> ProviderDefinition {
    ProviderDefinition::new("sms-relay-b", RuntimeInterface::Channel, move |settings, _| {
        Ok(ProviderInstance::Channel(Arc::new(Adapter {
            base: settings.text("URL")?.trim_end_matches('/').to_owned(),
            account: settings.text("ACCOUNT")?.to_owned(),
            token: settings.secret("TOKEN")?,
            outbox: outbox.clone(),
        })))
    })
    .with_setting("URL", ProviderSettingSpec::plain())
    .with_setting("ACCOUNT", ProviderSettingSpec::plain())
    .with_setting("TOKEN", ProviderSettingSpec::secret())
}

/// The definition a server links, over the process's [`outbox`].
pub fn definition() -> ProviderDefinition {
    definition_with(outbox())
}
