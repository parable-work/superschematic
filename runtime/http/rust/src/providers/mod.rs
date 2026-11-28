//! The runtime extension points (D159, D185): five interfaces a running
//! app calls, each implemented by a provider crate. A credential provider
//! ([`CredentialProvider`]), a channel adapter ([`ChannelAdapter`]), a
//! sandbox provider ([`SandboxProvider`]), a model provider
//! ([`ModelProvider`]) and a file processor ([`FileProcessor`]).
//!
//! A provider crate exports `pub fn definition() -> ProviderDefinition`
//! ([`define_provider`]) and declares the same in its Cargo.toml, under
//! `[package.metadata.superschematic.provider]`, which the build reads and
//! checks against the interface's contract, as it reads a TypeScript
//! package's `package.json`. A generated server reads the providers its
//! environment configures with [`providers_from`], over the slots the API's
//! `@providers` names and the definitions of the crates its config lists,
//! each held to the manifest the build read ([`check_provider_manifest`]).
//!
//! Rust has no ambient caller (D181), so a provider does not run "within"
//! its principal: its [`ProviderContext`] carries the principal (kind
//! `extension`, subject `extension:<name>`, its role's permissions), and a
//! provider that writes through the app's repositories opens them
//! `with_access(AccessCaller::of(Some(&context.principal)))`, as an
//! operation's implementation opens them for its caller.
//!
//! The contracts are `runtime/http/testdata/runtime_contracts.json`, which
//! the Go build (`ir.RuntimeContracts`) and the TypeScript runtime hold
//! too; a test of this module reads it.

#[cfg(feature = "http-client")]
mod agent_runner;
mod channel;
mod contracts;
mod credentials;
mod define;
mod files;
mod model;
mod registry;
mod sandbox;
mod secret;

#[cfg(feature = "http-client")]
pub use agent_runner::{
    run_agent_instance, AgentInstanceOptions, AgentInstanceResult, SandboxToolContext,
    SandboxToolHandler,
};
pub use channel::{
    ChannelAdapter, ChannelHealth, DeliveryState, DeliveryStatus, InboundAttachment,
    InboundAuthentication, InboundMessage, OptOut, OutboundMessage, SendResult, WebhookRequest,
};
pub use contracts::{
    contract_of, extension_subject, provider_config_field, RuntimeContract, RuntimeInterface,
    RUNTIME_CONTRACTS,
};
pub use credentials::{
    apply_injection, use_credential, CredentialListing, CredentialProvider, CredentialQuery,
    CredentialUse, Injection, InjectionError, InjectionRunner, InjectionTarget, UseCredential,
};
pub use define::{
    check_provider_manifest, define_provider, extension_principal, ConfigValue,
    ExtensionAuthenticator, ExtensionKey, ProviderContext, ProviderContractError, ProviderCreate,
    ProviderDefinition, ProviderInstance, ProviderSettingError, ProviderSettingSpec,
    ProviderSettings,
};
pub use files::{
    reads_media_type, ExtractedText, FileInput, FileProcessor, Preview, PreviewOptions,
};
pub use model::{
    ModelCompletion, ModelEmbedRequest, ModelEmbedding, ModelMessage, ModelProvider, ModelRole,
    ModelStop, ModelTool, ModelToolCall, ModelTurn, ModelUsage,
};
pub use registry::{
    providers_from, ConfiguredProvider, ProviderConfigError, ProviderNotConfiguredError, Providers,
    ProvidersOptions,
};
pub use sandbox::{
    Amount, SandboxCost, SandboxInstance, SandboxLimits, SandboxProvider, SandboxSpec,
    SandboxState, SandboxStatus,
};
pub use secret::{SecretValue, SECRET_MASK};

/// An optional operation a provider does not implement: what the default
/// of each optional method of an interface answers, a channel adapter's
/// `status` and `inbound`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Unsupported {
    /// The interface's title, `channel adapter`.
    pub title: &'static str,
    /// The operation, `status`.
    pub operation: &'static str,
}

impl std::fmt::Display for Unsupported {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "this {} implements no {}", self.title, self.operation)
    }
}

impl std::error::Error for Unsupported {}

/// What an optional operation a provider leaves out fails with.
pub(crate) fn unsupported(title: &'static str, operation: &'static str) -> crate::BoxError {
    Box::new(Unsupported { title, operation })
}

/// RFC 3339 in UTC, to the millisecond, as the TypeScript runtime's
/// `Date` serializes: what a [`SystemTime`](std::time::SystemTime) of a
/// provider's types serializes as.
pub(crate) mod time {
    use serde::{Deserialize, Deserializer, Serializer};
    use std::time::{Duration, SystemTime, UNIX_EPOCH};

    pub fn text(time: SystemTime) -> String {
        let since = time.duration_since(UNIX_EPOCH).unwrap_or_default();
        let secs = since.as_secs();
        let days = i64::try_from(secs / 86_400).unwrap_or(i64::MAX);
        let rest = secs % 86_400;
        let (year, month, day) = crate::civil::from_days(days);
        format!(
            "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
            rest / 3600,
            rest % 3600 / 60,
            rest % 60,
            since.subsec_millis()
        )
    }

    pub fn parse(text: &str) -> Option<SystemTime> {
        let b = text.as_bytes();
        if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || b[10] != b'T' {
            return None;
        }
        let num = |range: std::ops::Range<usize>| text.get(range)?.parse::<i64>().ok();
        let (year, month, day) = (num(0..4)?, num(5..7)?, num(8..10)?);
        let (hour, minute, second) = (num(11..13)?, num(14..16)?, num(17..19)?);
        let mut millis = 0;
        if b.get(19) == Some(&b'.') {
            let end = text[20..].find(|c: char| !c.is_ascii_digit())? + 20;
            let digits = &text[20..end];
            millis = format!("{digits:0<3}")[..3].parse::<u64>().ok()?;
        }
        if !text.ends_with('Z') {
            return None;
        }
        let secs =
            crate::civil::to_days(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second;
        let secs = u64::try_from(secs).ok()?;
        Some(UNIX_EPOCH + Duration::from_secs(secs) + Duration::from_millis(millis))
    }

    pub fn serialize<S: Serializer>(time: &SystemTime, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&text(*time))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<SystemTime, D::Error> {
        let raw = String::deserialize(d)?;
        parse(&raw)
            .ok_or_else(|| serde::de::Error::custom(format!("{raw:?} is no RFC 3339 time in UTC")))
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn a_time_reads_back_as_it_was_written() {
            let at = UNIX_EPOCH + Duration::from_millis(1_795_608_000_123);
            let written = text(at);
            assert_eq!(written, "2026-11-25T12:00:00.123Z");
            assert_eq!(parse(&written), Some(at));
            assert_eq!(
                parse("2026-11-25T12:00:00Z"),
                Some(UNIX_EPOCH + Duration::from_secs(1_795_608_000))
            );
        }
    }
}
