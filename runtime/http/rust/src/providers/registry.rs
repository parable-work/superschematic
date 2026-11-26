//! How core obtains the provider an environment configures (D159, D185).
//! The API's `@envVars` class names its slots with `@providers`, and the
//! stack binds each slot, a config field, to a provider's name in each
//! environment, with the provider's settings in the fields named after the
//! slot. [`providers_from`] reads the loaded config with the slots and the
//! definitions the server links, and answers [`Providers`]:
//! `channel("SMS")` gives the channel adapter slot `SMS` names, made on
//! first use from its settings, with its own principal in its context.
//!
//! The build refuses an environment whose slot names an unknown provider
//! or leaves a needed setting unbound; `providers_from` refuses the same
//! at start, with every problem and never a value, for a config no build
//! checked.

use serde::Serialize;
use serde_json::Value;
use std::collections::BTreeMap;
use std::fmt;
use std::sync::{Arc, Mutex};

use super::define::{check_provider_manifest, extension_principal, ConfigValue};
use super::{
    provider_config_field, ChannelAdapter, CredentialProvider, FileProcessor, ModelProvider,
    ProviderContext, ProviderContractError, ProviderDefinition, ProviderInstance, ProviderSettings,
    RuntimeInterface, SandboxProvider,
};
use crate::auth::Principal;
use crate::BoxError;

/// What [`providers_from`] reads.
#[derive(Clone, Default)]
pub struct ProvidersOptions {
    /// The API's provider slots, as `@providers` declares them: `SMS` takes
    /// `channel`.
    pub slots: BTreeMap<String, String>,
    /// The API's loaded config: each slot's field names a provider, and
    /// the fields after it hold its settings. A field unset is absent.
    pub config: BTreeMap<String, ConfigValue>,
    /// The definitions of the provider crates the server links, the ones
    /// the API's config lists.
    pub definitions: Vec<ProviderDefinition>,
    /// The manifests the build read of the providers the API's config
    /// lists, by name, as JSON: each definition is held to its own
    /// ([`check_provider_manifest`]). Empty for a server no build wrote.
    pub manifests: BTreeMap<String, Value>,
}

/// A slot an environment fills.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct ConfiguredProvider {
    pub slot: String,
    pub interface: RuntimeInterface,
    pub provider: String,
}

/// The config names an unknown provider, one of another interface, or
/// leaves a setting unset: every problem, and never a value.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderConfigError {
    pub problems: Vec<String>,
}

impl fmt::Display for ProviderConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "runtime providers: {}", self.problems.join("; "))
    }
}

impl std::error::Error for ProviderConfigError {}

/// Asked for a slot the environment leaves unset, or one of another
/// interface.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderNotConfiguredError(pub String);

impl fmt::Display for ProviderNotConfiguredError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ProviderNotConfiguredError {}

struct Entry {
    iface: RuntimeInterface,
    definition: ProviderDefinition,
    settings: ProviderSettings,
    principal: Principal,
    made: Mutex<Option<ProviderInstance>>,
}

/// The providers an environment configures. A server holds one, shared,
/// in its Deps (`Arc<Providers>`).
pub struct Providers {
    slots: BTreeMap<String, String>,
    entries: BTreeMap<String, Entry>,
}

impl fmt::Debug for Providers {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Providers")
            .field("configured", &self.configured(None))
            .finish()
    }
}

/// Reads the providers `options.config` configures; it fails with a
/// [`ProviderConfigError`] naming every problem.
pub fn providers_from(options: ProvidersOptions) -> Result<Providers, ProviderConfigError> {
    let mut problems = Vec::new();
    let mut by_name: BTreeMap<String, ProviderDefinition> = BTreeMap::new();
    for definition in options.definitions {
        if by_name.contains_key(&definition.name) {
            problems.push(format!(
                "two linked providers are named {}",
                definition.name
            ));
        }
        if let Some(manifest) = options.manifests.get(&definition.name) {
            if let Err(err) = check_provider_manifest(manifest, &definition) {
                problems.push(err.0);
            }
        }
        by_name.insert(definition.name.clone(), definition);
    }
    for name in options.manifests.keys() {
        if !by_name.contains_key(name) {
            problems.push(format!(
                "the API's config lists provider {name}, which the server does not link"
            ));
        }
    }
    let mut entries = BTreeMap::new();
    for (slot, iface) in &options.slots {
        let Some(iface) = RuntimeInterface::parse(iface) else {
            problems.push(format!(
                "slot {slot} takes {iface:?}, which is no runtime interface"
            ));
            continue;
        };
        let name = match options.config.get(slot) {
            None => continue,
            Some(ConfigValue::Text(name)) if name.is_empty() => continue,
            Some(ConfigValue::Text(name)) => name,
            Some(ConfigValue::Secret(_)) => {
                problems.push(format!("slot {slot} holds no provider's name"));
                continue;
            }
        };
        let Some(definition) = by_name.get(name) else {
            let links = by_name.keys().cloned().collect::<Vec<_>>().join(", ");
            problems.push(format!(
                "slot {slot} names provider {name}, which the server does not link (it links {})",
                if links.is_empty() { "none" } else { &links }
            ));
            continue;
        };
        if definition.implements != iface {
            problems.push(format!(
                "slot {slot} takes a {}, and provider {name} implements {}",
                iface.contract().title,
                definition.implements
            ));
            continue;
        }
        let mut values = BTreeMap::new();
        for (key, spec) in &definition.config {
            let field = provider_config_field(slot, key);
            let value = match options.config.get(&field) {
                None => None,
                Some(ConfigValue::Text(text)) if text.is_empty() => None,
                Some(value) => Some(value),
            };
            let Some(value) = value else {
                if !spec.optional {
                    problems.push(format!(
                        "provider {name} in slot {slot} needs {field}, which is not set"
                    ));
                }
                continue;
            };
            let secret = matches!(value, ConfigValue::Secret(_));
            if spec.secret != secret {
                let what = |secret: bool| if secret { "a secret" } else { "no secret" };
                problems.push(format!(
                    "provider {name} in slot {slot} reads {field} as {}, and the config holds it as {}",
                    what(spec.secret),
                    what(secret)
                ));
                continue;
            }
            values.insert(key.clone(), value.clone());
        }
        entries.insert(
            slot.clone(),
            Entry {
                iface,
                principal: extension_principal(&definition.name, &definition.role),
                settings: ProviderSettings::new(slot.clone(), values),
                definition: definition.clone(),
                made: Mutex::new(None),
            },
        );
    }
    if !problems.is_empty() {
        return Err(ProviderConfigError { problems });
    }
    Ok(Providers {
        slots: options.slots,
        entries,
    })
}

impl Providers {
    /// Providers with no slot: an API that declares none.
    pub fn none() -> Self {
        Self {
            slots: BTreeMap::new(),
            entries: BTreeMap::new(),
        }
    }

    /// The slots the environment fills, of one interface or of every one,
    /// sorted by slot.
    pub fn configured(&self, iface: Option<RuntimeInterface>) -> Vec<ConfiguredProvider> {
        self.entries
            .iter()
            .filter(|(_, entry)| iface.is_none_or(|iface| entry.iface == iface))
            .map(|(slot, entry)| ConfiguredProvider {
                slot: slot.clone(),
                interface: entry.iface,
                provider: entry.definition.name.clone(),
            })
            .collect()
    }

    fn entry(
        &self,
        slot: &str,
        iface: Option<RuntimeInterface>,
    ) -> Result<&Entry, ProviderNotConfiguredError> {
        let Some(entry) = self.entries.get(slot) else {
            return Err(ProviderNotConfiguredError(
                if self.slots.contains_key(slot) {
                    format!("slot {slot} names no provider in this environment")
                } else {
                    format!("{slot} is no provider slot of the API")
                },
            ));
        };
        if let Some(iface) = iface {
            if entry.iface != iface {
                return Err(ProviderNotConfiguredError(format!(
                    "slot {slot} takes a {}, not a {}",
                    entry.iface.contract().title,
                    iface.contract().title
                )));
            }
        }
        Ok(entry)
    }

    /// The provider of `slot`, which takes `iface`, made on first use; a
    /// [`ProviderNotConfiguredError`] for a slot unset, and the provider's
    /// own error for one whose `create` failed, which the next call tries
    /// again.
    pub fn get(&self, slot: &str, iface: RuntimeInterface) -> Result<ProviderInstance, BoxError> {
        let entry = self.entry(slot, Some(iface))?;
        let mut made = entry
            .made
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some(instance) = made.as_ref() {
            return Ok(instance.clone());
        }
        let context = ProviderContext {
            slot: slot.to_owned(),
            provider: entry.definition.name.clone(),
            principal: entry.principal.clone(),
            span: tracing::info_span!("provider", provider = %entry.definition.name, slot = %slot),
        };
        let instance = (entry.definition.create)(&entry.settings, &context)?;
        if instance.interface() != entry.iface {
            return Err(Box::new(ProviderContractError(format!(
                "{} {}: create made a {}",
                entry.iface.contract().title,
                entry.definition.name,
                instance.interface().contract().title
            ))));
        }
        *made = Some(instance.clone());
        Ok(instance)
    }

    /// The credential provider of `slot`.
    pub fn credentials(&self, slot: &str) -> Result<Arc<dyn CredentialProvider>, BoxError> {
        match self.get(slot, RuntimeInterface::Credentials)? {
            ProviderInstance::Credentials(provider) => Ok(provider),
            _ => unreachable!("get holds the instance to its interface"),
        }
    }

    /// The channel adapter of `slot`.
    pub fn channel(&self, slot: &str) -> Result<Arc<dyn ChannelAdapter>, BoxError> {
        match self.get(slot, RuntimeInterface::Channel)? {
            ProviderInstance::Channel(provider) => Ok(provider),
            _ => unreachable!("get holds the instance to its interface"),
        }
    }

    /// The sandbox provider of `slot`.
    pub fn sandbox(&self, slot: &str) -> Result<Arc<dyn SandboxProvider>, BoxError> {
        match self.get(slot, RuntimeInterface::Sandbox)? {
            ProviderInstance::Sandbox(provider) => Ok(provider),
            _ => unreachable!("get holds the instance to its interface"),
        }
    }

    /// The model provider of `slot`.
    pub fn model(&self, slot: &str) -> Result<Arc<dyn ModelProvider>, BoxError> {
        match self.get(slot, RuntimeInterface::Model)? {
            ProviderInstance::Model(provider) => Ok(provider),
            _ => unreachable!("get holds the instance to its interface"),
        }
    }

    /// The file processor of `slot`.
    pub fn files(&self, slot: &str) -> Result<Arc<dyn FileProcessor>, BoxError> {
        match self.get(slot, RuntimeInterface::Files)? {
            ProviderInstance::Files(provider) => Ok(provider),
            _ => unreachable!("get holds the instance to its interface"),
        }
    }

    /// The principal of the provider `slot` names.
    pub fn principal_of(&self, slot: &str) -> Result<Principal, ProviderNotConfiguredError> {
        Ok(self.entry(slot, None)?.principal.clone())
    }

    /// The name of the provider `slot` names.
    pub fn provider_of(&self, slot: &str) -> Result<&str, ProviderNotConfiguredError> {
        Ok(&self.entry(slot, None)?.definition.name)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::PrincipalKind;
    use crate::providers::{
        ChannelHealth, DeliveryState, OutboundMessage, ProviderSettingSpec, SecretValue, SendResult,
    };
    use async_trait::async_trait;

    struct Recording {
        name: String,
        url: String,
        token: SecretValue,
        principal: Principal,
        sent: Mutex<Vec<(OutboundMessage, String, String)>>,
    }

    #[async_trait]
    impl ChannelAdapter for Recording {
        fn kind(&self) -> &str {
            "sms"
        }

        async fn send(&self, message: &OutboundMessage) -> Result<SendResult, BoxError> {
            let mut sent = self.sent.lock().unwrap();
            sent.push((
                message.clone(),
                self.url.clone(),
                self.principal.subject.clone(),
            ));
            Ok(SendResult {
                provider_message_id: format!("{}-{}", self.name, sent.len()),
                state: DeliveryState::Queued,
            })
        }

        async fn health(&self) -> Result<ChannelHealth, BoxError> {
            Ok(ChannelHealth {
                ok: !self.token.reveal().is_empty(),
                detail: None,
            })
        }
    }

    fn recording(name: &'static str) -> ProviderDefinition {
        ProviderDefinition::new(name, RuntimeInterface::Channel, move |settings, context| {
            Ok(ProviderInstance::Channel(Arc::new(Recording {
                name: name.to_owned(),
                url: settings.text("URL")?.to_owned(),
                token: settings.secret("TOKEN")?,
                principal: context.principal.clone(),
                sent: Mutex::new(Vec::new()),
            })))
        })
        .with_setting("URL", ProviderSettingSpec::plain())
        .with_setting("TOKEN", ProviderSettingSpec::secret())
        .with_setting("SENDER", ProviderSettingSpec::plain().optional())
        .with_role(["messages.relay"])
    }

    fn options(config: &[(&str, ConfigValue)], slots: &[(&str, &str)]) -> ProvidersOptions {
        ProvidersOptions {
            slots: slots
                .iter()
                .map(|(a, b)| ((*a).to_owned(), (*b).to_owned()))
                .collect(),
            config: config
                .iter()
                .map(|(k, v)| ((*k).to_owned(), v.clone()))
                .collect(),
            definitions: vec![recording("relay-a"), recording("relay-b")],
            manifests: BTreeMap::new(),
        }
    }

    fn text(value: &str) -> ConfigValue {
        ConfigValue::Text(value.to_owned())
    }

    #[tokio::test]
    async fn a_slot_gives_the_provider_it_names_made_from_its_settings_as_its_own_principal() {
        let providers = providers_from(options(
            &[
                ("SMS", text("relay-b")),
                ("SMS_URL", text("http://relay.test")),
                (
                    "SMS_TOKEN",
                    ConfigValue::Secret(SecretValue::new("token-value-1")),
                ),
            ],
            &[("SMS", "channel"), ("VAULT", "credentials")],
        ))
        .unwrap();
        assert_eq!(
            providers.configured(None),
            [ConfiguredProvider {
                slot: "SMS".into(),
                interface: RuntimeInterface::Channel,
                provider: "relay-b".into()
            }]
        );
        assert!(providers
            .configured(Some(RuntimeInterface::Credentials))
            .is_empty());
        let sms = providers.channel("SMS").unwrap();
        let message = OutboundMessage {
            to: "+15550100".into(),
            body: "hello".into(),
            ..OutboundMessage::default()
        };
        assert_eq!(
            sms.send(&message).await.unwrap().provider_message_id,
            "relay-b-1"
        );
        assert!(sms.health().await.unwrap().ok);
        assert!(Arc::ptr_eq(&sms, &providers.channel("SMS").unwrap()));
        let principal = providers.principal_of("SMS").unwrap();
        assert_eq!(
            (
                principal.subject.as_str(),
                principal.kind,
                principal.permissions.as_slice()
            ),
            (
                "extension:relay-b",
                PrincipalKind::Extension,
                ["messages.relay".to_owned()].as_slice()
            )
        );
        assert!(!format!("{providers:?}").contains("token-value-1"));
        let err = providers.credentials("VAULT").err().unwrap();
        assert!(
            err.downcast_ref::<ProviderNotConfiguredError>().is_some(),
            "{err}"
        );
        assert!(providers
            .get("SMS", RuntimeInterface::Model)
            .err()
            .unwrap()
            .to_string()
            .contains("not a model provider"));
        assert!(providers
            .channel("PUSH")
            .err()
            .unwrap()
            .to_string()
            .contains("no provider slot"));
    }

    #[test]
    fn a_config_that_names_an_unknown_provider_or_leaves_a_setting_unset_is_refused_naming_no_value(
    ) {
        let refused = |config: &[(&str, ConfigValue)], slots: &[(&str, &str)]| {
            providers_from(options(config, slots)).unwrap_err().problems
        };
        assert_eq!(
            refused(&[("SMS", text("relay-x"))], &[("SMS", "channel")]),
            ["slot SMS names provider relay-x, which the server does not link (it links relay-a, relay-b)"]
        );
        assert_eq!(
            refused(&[("BOT", text("relay-a"))], &[("BOT", "model")]),
            ["slot BOT takes a model provider, and provider relay-a implements channel"]
        );
        assert_eq!(
            refused(
                &[
                    ("SMS", text("relay-a")),
                    (
                        "SMS_TOKEN",
                        ConfigValue::Secret(SecretValue::new("token-value-2"))
                    )
                ],
                &[("SMS", "channel")]
            ),
            ["provider relay-a in slot SMS needs SMS_URL, which is not set"]
        );
        let problems = refused(
            &[
                ("SMS", text("relay-a")),
                ("SMS_URL", text("u")),
                ("SMS_TOKEN", text("token-value-3")),
            ],
            &[("SMS", "channel")],
        );
        assert_eq!(
            problems,
            ["provider relay-a in slot SMS reads SMS_TOKEN as a secret, and the config holds it as no secret"]
        );
        assert!(!problems.join("").contains("token-value-3"));
        assert_eq!(
            refused(&[], &[("SMS", "payments")]),
            ["slot SMS takes \"payments\", which is no runtime interface"]
        );
    }

    #[test]
    fn a_linked_crate_is_held_to_the_manifest_the_build_read() {
        let mut with = options(&[], &[("SMS", "channel")]);
        with.manifests.insert(
            "relay-a".into(),
            serde_json::json!({"name": "relay-a", "implements": "channel", "config": {"URL": {}, "TOKEN": {"secret": true}, "SENDER": {"optional": true}}, "role": ["messages.send"]}),
        );
        with.manifests.insert(
            "relay-c".into(),
            serde_json::json!({"name": "relay-c", "implements": "channel"}),
        );
        let problems = providers_from(with).unwrap_err().problems;
        assert_eq!(problems.len(), 2, "{problems:?}");
        assert!(problems[0].contains("manifest's role"), "{problems:?}");
        assert!(
            problems[1].contains("lists provider relay-c, which the server does not link"),
            "{problems:?}"
        );
    }

    #[test]
    fn a_create_that_makes_another_interface_is_refused() {
        let broken = ProviderDefinition::new("half", RuntimeInterface::Channel, |_, _| {
            Ok(ProviderInstance::Model(Arc::new(Stub)))
        });
        let providers = providers_from(ProvidersOptions {
            slots: BTreeMap::from([("SMS".into(), "channel".into())]),
            config: BTreeMap::from([("SMS".into(), text("half"))]),
            definitions: vec![broken],
            manifests: BTreeMap::new(),
        })
        .unwrap();
        let err = providers.channel("SMS").err().unwrap();
        assert!(
            err.to_string().contains("create made a model provider"),
            "{err}"
        );
    }

    struct Stub;

    #[async_trait]
    impl ModelProvider for Stub {
        async fn complete(
            &self,
            _turn: &crate::providers::ModelTurn,
        ) -> Result<crate::providers::ModelCompletion, BoxError> {
            Err("stub".into())
        }
    }
}
