//! A provider crate (D159, D185) exports the definition [`define_provider`]
//! checks: its name, the interface it implements, the settings it reads,
//! the optional operations it implements and its role, and `create`,
//! which makes the provider from its settings. Its Cargo.toml declares the
//! same under `[package.metadata.superschematic.provider]`, which the
//! build reads and checks against the interface's contract; the generated
//! server holds each linked definition to the manifest the build read
//! ([`check_provider_manifest`]), so the build and the runtime read one
//! provider.

use async_trait::async_trait;
use http::request::Parts;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;
use std::fmt;
use std::sync::Arc;

use super::{
    contracts::RuntimeInterface, extension_subject, ChannelAdapter, CredentialProvider,
    FileProcessor, ModelProvider, SandboxProvider, SecretValue, SECRET_MASK,
};
use crate::auth::{bearer_token, Authenticator, Principal, PrincipalKind};
use crate::{ApiError, BoxError};

/// One setting a provider reads, from the config field named after its
/// slot and the key.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProviderSettingSpec {
    /// A `Secret<string>` field: the provider gets a [`SecretValue`],
    /// never a string.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub secret: bool,
    /// An environment may leave it unset.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub optional: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
}

impl ProviderSettingSpec {
    /// A plain setting an environment sets.
    pub fn plain() -> Self {
        Self::default()
    }

    /// A secret setting an environment sets.
    pub fn secret() -> Self {
        Self {
            secret: true,
            ..Self::default()
        }
    }

    /// The setting, which an environment may leave unset.
    #[must_use]
    pub fn optional(mut self) -> Self {
        self.optional = true;
        self
    }
}

/// What a provider runs with.
#[derive(Clone, Debug)]
pub struct ProviderContext {
    /// The config field that names it.
    pub slot: String,
    /// Its name.
    pub provider: String,
    /// Its own principal (kind `extension`), which it opens the app's
    /// repositories for (`with_access(AccessCaller::of(Some(&principal)))`)
    /// and which the access log records.
    pub principal: Principal,
    /// A span whose events carry the provider and the slot.
    pub span: tracing::Span,
}

/// A provider, of the interface it implements.
#[derive(Clone)]
pub enum ProviderInstance {
    Credentials(Arc<dyn CredentialProvider>),
    Channel(Arc<dyn ChannelAdapter>),
    Sandbox(Arc<dyn SandboxProvider>),
    Model(Arc<dyn ModelProvider>),
    Files(Arc<dyn FileProcessor>),
}

impl ProviderInstance {
    /// The interface it implements.
    pub fn interface(&self) -> RuntimeInterface {
        match self {
            ProviderInstance::Credentials(_) => RuntimeInterface::Credentials,
            ProviderInstance::Channel(_) => RuntimeInterface::Channel,
            ProviderInstance::Sandbox(_) => RuntimeInterface::Sandbox,
            ProviderInstance::Model(_) => RuntimeInterface::Model,
            ProviderInstance::Files(_) => RuntimeInterface::Files,
        }
    }
}

impl fmt::Debug for ProviderInstance {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "ProviderInstance({})", self.interface())
    }
}

/// What makes a provider from its settings and its context.
pub type ProviderCreate = Arc<
    dyn Fn(&ProviderSettings, &ProviderContext) -> Result<ProviderInstance, BoxError> + Send + Sync,
>;

/// A provider crate's definition.
#[derive(Clone)]
pub struct ProviderDefinition {
    /// Lower-case words joined by hyphens; an environment's slot holds it.
    pub name: String,
    pub implements: RuntimeInterface,
    /// The settings it reads, by key: slot `SMS` and key `URL` read
    /// `SMS_URL`.
    pub config: BTreeMap<String, ProviderSettingSpec>,
    /// The permissions its principal holds, each one capability, two
    /// dotted segments or more.
    pub role: Vec<String>,
    /// The optional operations of its interface it implements: a channel
    /// adapter's `status` and `inbound`. Every required one it implements,
    /// since its trait has no default for them.
    pub operations: Vec<String>,
    pub create: ProviderCreate,
}

impl ProviderDefinition {
    /// A definition of the provider `name`, of `implements`, made by
    /// `create`, with no settings, no role and no optional operation.
    pub fn new(
        name: impl Into<String>,
        implements: RuntimeInterface,
        create: impl Fn(&ProviderSettings, &ProviderContext) -> Result<ProviderInstance, BoxError>
            + Send
            + Sync
            + 'static,
    ) -> Self {
        Self {
            name: name.into(),
            implements,
            config: BTreeMap::new(),
            role: Vec::new(),
            operations: Vec::new(),
            create: Arc::new(create),
        }
    }

    /// The definition reading the setting `key`.
    #[must_use]
    pub fn with_setting(mut self, key: impl Into<String>, spec: ProviderSettingSpec) -> Self {
        self.config.insert(key.into(), spec);
        self
    }

    /// The definition whose principal holds `role`.
    #[must_use]
    pub fn with_role(mut self, role: impl IntoIterator<Item = impl Into<String>>) -> Self {
        self.role = role.into_iter().map(Into::into).collect();
        self
    }

    /// The definition implementing the optional `operations`.
    #[must_use]
    pub fn with_operations(
        mut self,
        operations: impl IntoIterator<Item = impl Into<String>>,
    ) -> Self {
        self.operations = operations.into_iter().map(Into::into).collect();
        self
    }

    /// Every operation it implements: the ones its interface requires,
    /// then its optional ones, in the contract's order.
    pub fn all_operations(&self) -> Vec<String> {
        let contract = self.implements.contract();
        contract
            .required
            .iter()
            .chain(
                contract
                    .optional
                    .iter()
                    .filter(|op| self.operations.iter().any(|o| o == *op)),
            )
            .map(|op| (*op).to_owned())
            .collect()
    }
}

impl fmt::Debug for ProviderDefinition {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ProviderDefinition")
            .field("name", &self.name)
            .field("implements", &self.implements)
            .field("config", &self.config)
            .field("role", &self.role)
            .field("operations", &self.operations)
            .finish_non_exhaustive()
    }
}

/// A definition, a manifest or a provider that breaks its interface's
/// contract.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderContractError(pub String);

impl fmt::Display for ProviderContractError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ProviderContractError {}

fn name_ok(name: &str) -> bool {
    !name.is_empty()
        && name.split('-').all(|word| {
            !word.is_empty()
                && word
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
        })
        && name.as_bytes()[0].is_ascii_lowercase()
}

fn key_ok(key: &str) -> bool {
    key.as_bytes().first().is_some_and(u8::is_ascii_uppercase)
        && key
            .bytes()
            .all(|b| b.is_ascii_uppercase() || b.is_ascii_digit() || b == b'_')
}

fn segment_ok(segment: &str) -> bool {
    segment
        .as_bytes()
        .first()
        .is_some_and(u8::is_ascii_alphabetic)
        && segment
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_')
}

/// Checks a definition as the build checks a manifest
/// (`ir.RuntimeProvider.Check`) and returns it: a name, config keys of
/// upper-case words, optional operations its interface has, each once, and
/// a role of capabilities, never an area.
pub fn define_provider(
    definition: ProviderDefinition,
) -> Result<ProviderDefinition, ProviderContractError> {
    let fail = |message: String| {
        Err(ProviderContractError(format!(
            "provider {:?}: {message}",
            definition.name
        )))
    };
    if !name_ok(&definition.name) {
        return fail("a name is lower-case letters and digits in words joined by hyphens".into());
    }
    for (key, spec) in &definition.config {
        if !key_ok(key) {
            return fail(format!(
                "config key {key:?} is upper-case letters, digits and underscores"
            ));
        }
        if key == super::CALLER_KEY && !spec.secret {
            return fail(format!(
                "config key {key} is the key the provider calls the app with, which an environment sets as a secret; declare it secret (#744)"
            ));
        }
    }
    let contract = definition.implements.contract();
    for (i, op) in definition.operations.iter().enumerate() {
        if definition.operations[..i].contains(op) {
            return fail(format!("lists operation {op} twice"));
        }
        if !contract.optional.contains(&op.as_str()) {
            return fail(format!(
                "a {} has no optional operation {op}; its optional operations are {}",
                contract.title,
                if contract.optional.is_empty() {
                    "none".to_owned()
                } else {
                    contract.optional.join(", ")
                }
            ));
        }
    }
    for permission in &definition.role {
        let segments: Vec<&str> = permission.split('.').collect();
        if !segments.iter().all(|s| segment_ok(s)) {
            return fail(format!(
                "role permission {permission:?} is a dotted path of words"
            ));
        }
        if segments.len() < 2 {
            return fail(format!(
                "role permission {permission:?} names an area; a provider's role names one capability in it"
            ));
        }
    }
    Ok(definition)
}

/// The manifest of a provider as a Cargo.toml's
/// `[package.metadata.superschematic.provider]` or a package.json's
/// `"superschematic": {"provider": ...}` declares it.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Manifest {
    name: String,
    implements: String,
    #[serde(default)]
    operations: Option<Vec<String>>,
    #[serde(default)]
    config: BTreeMap<String, ProviderSettingSpec>,
    #[serde(default)]
    role: Vec<String>,
}

/// Holds a provider's manifest, as JSON, to its definition: the name, the
/// interface, the settings and their secrecy, the role, and the
/// operations, which the manifest lists or leaves to the required ones.
/// The generated server calls it for each provider the build read, so the
/// crate it links is the one the build checked.
pub fn check_provider_manifest(
    manifest: &Value,
    definition: &ProviderDefinition,
) -> Result<(), ProviderContractError> {
    let fail = |message: String| {
        Err(ProviderContractError(format!(
            "crate of provider {}: {message}",
            definition.name
        )))
    };
    let manifest: Manifest = match serde_json::from_value(manifest.clone()) {
        Ok(manifest) => manifest,
        Err(err) => return fail(format!("its manifest is no provider manifest: {err}")),
    };
    if manifest.name != definition.name {
        return fail(format!("its manifest names {:?}", manifest.name));
    }
    if manifest.implements != definition.implements.as_str() {
        return fail(format!(
            "its manifest implements {:?}, the definition {}",
            manifest.implements, definition.implements
        ));
    }
    let settings = |config: &BTreeMap<String, ProviderSettingSpec>| {
        config
            .iter()
            .map(|(key, spec)| format!("{key}:{}:{}", spec.secret, spec.optional))
            .collect::<Vec<_>>()
            .join(",")
    };
    if settings(&manifest.config) != settings(&definition.config) {
        return fail(format!(
            "its manifest's config [{}] is not the definition's [{}]",
            settings(&manifest.config),
            settings(&definition.config)
        ));
    }
    let sorted = |list: &[String]| {
        let mut list = list.to_vec();
        list.sort();
        list
    };
    if sorted(&manifest.role) != sorted(&definition.role) {
        return fail(format!(
            "its manifest's role {:?} is not the definition's {:?}",
            sorted(&manifest.role),
            sorted(&definition.role)
        ));
    }
    let contract = definition.implements.contract();
    let listed = manifest.operations.unwrap_or_else(|| {
        contract
            .required
            .iter()
            .map(|op| (*op).to_owned())
            .collect()
    });
    if sorted(&listed) != sorted(&definition.all_operations()) {
        return fail(format!(
            "its manifest lists operations {:?}, and the provider has {:?}",
            sorted(&listed),
            sorted(&definition.all_operations())
        ));
    }
    Ok(())
}

/// The principal a provider acts as (D159): kind `extension`, subject
/// `extension:<name>`, its role's permissions.
pub fn extension_principal(name: &str, role: &[String]) -> Principal {
    Principal::new(extension_subject(name), role.iter().cloned()).of_kind(PrincipalKind::Extension)
}

/// A value of the API's config as the providers read it (D159): a plain
/// field's text, or a `Secret<string>` field's secret.
#[derive(Clone, PartialEq, Eq)]
pub enum ConfigValue {
    Text(String),
    Secret(SecretValue),
}

impl fmt::Debug for ConfigValue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ConfigValue::Text(text) => fmt::Debug::fmt(text, f),
            ConfigValue::Secret(_) => f.write_str(SECRET_MASK),
        }
    }
}

/// A setting missing, or read as what it is not.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderSettingError(pub String);

impl fmt::Display for ProviderSettingError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ProviderSettingError {}

/// A provider's settings: the config fields named after its slot, by
/// key. A plain setting reads as text and a secret one as a
/// [`SecretValue`], so a provider never holds a secret as plain text. The
/// debug and JSON forms print the keys and masked secrets.
#[derive(Clone)]
pub struct ProviderSettings {
    slot: String,
    values: BTreeMap<String, ConfigValue>,
}

impl ProviderSettings {
    /// The settings of the provider in `slot`.
    pub fn new(slot: impl Into<String>, values: BTreeMap<String, ConfigValue>) -> Self {
        Self {
            slot: slot.into(),
            values,
        }
    }

    /// Whether the environment set `key`.
    pub fn has(&self, key: &str) -> bool {
        self.values.contains_key(key)
    }

    /// The plain setting `key`; an error for one unset or secret.
    pub fn text(&self, key: &str) -> Result<&str, ProviderSettingError> {
        self.optional_text(key)?.ok_or_else(|| {
            ProviderSettingError(format!("setting {key} of slot {} is not set", self.slot))
        })
    }

    /// The plain setting `key`, or `None` when unset; an error for a
    /// secret one.
    pub fn optional_text(&self, key: &str) -> Result<Option<&str>, ProviderSettingError> {
        match self.values.get(key) {
            None => Ok(None),
            Some(ConfigValue::Text(text)) => Ok(Some(text)),
            Some(ConfigValue::Secret(_)) => Err(ProviderSettingError(format!(
                "setting {key} of slot {} is a secret; read it with secret()",
                self.slot
            ))),
        }
    }

    /// The secret setting `key`; an error for one unset or plain.
    pub fn secret(&self, key: &str) -> Result<SecretValue, ProviderSettingError> {
        match self.values.get(key) {
            None => Err(ProviderSettingError(format!(
                "setting {key} of slot {} is not set",
                self.slot
            ))),
            Some(ConfigValue::Secret(secret)) => Ok(secret.clone()),
            Some(ConfigValue::Text(_)) => Err(ProviderSettingError(format!(
                "setting {key} of slot {} is no secret; read it with text()",
                self.slot
            ))),
        }
    }
}

impl fmt::Debug for ProviderSettings {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "ProviderSettings({}: {:?})", self.slot, self.values)
    }
}

impl Serialize for ProviderSettings {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap;
        let mut out = s.serialize_map(Some(self.values.len()))?;
        for (key, value) in &self.values {
            match value {
                ConfigValue::Text(text) => out.serialize_entry(key, text)?,
                ConfigValue::Secret(_) => out.serialize_entry(key, SECRET_MASK)?,
            }
        }
        out.end()
    }
}

/// A provider that calls the app with a key of its own: the key and the
/// provider it is.
#[derive(Clone, Debug)]
pub struct ExtensionKey {
    pub key: SecretValue,
    pub name: String,
    pub role: Vec<String>,
}

/// An authenticator for the providers that call the app over HTTP, a
/// messaging bridge that connects outward and relays replies, for one: a
/// Bearer token equal to one of the keys is that provider's principal,
/// with its role's permissions alone. Any other request is left to the
/// authenticator it wraps, or has no caller.
pub struct ExtensionAuthenticator {
    keys: Vec<ExtensionKey>,
    next: Option<Arc<dyn Authenticator>>,
}

impl ExtensionAuthenticator {
    /// The authenticator of `keys`, leaving every other request with no
    /// caller.
    pub fn new(keys: Vec<ExtensionKey>) -> Self {
        Self { keys, next: None }
    }

    /// The authenticator handing every other request to `next`.
    #[must_use]
    pub fn with_next(mut self, next: Arc<dyn Authenticator>) -> Self {
        self.next = Some(next);
        self
    }
}

fn equal(a: &[u8], b: &[u8]) -> bool {
    let mut diff = a.len() ^ b.len();
    for i in 0..a.len().max(b.len()) {
        diff |= usize::from(a.get(i).copied().unwrap_or(0) ^ b.get(i).copied().unwrap_or(0));
    }
    diff == 0
}

#[async_trait]
impl Authenticator for ExtensionAuthenticator {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        if let Some(token) = bearer_token(&request.headers) {
            for entry in &self.keys {
                if equal(token.as_bytes(), entry.key.reveal().as_bytes()) {
                    return Ok(Some(extension_principal(&entry.name, &entry.role)));
                }
            }
        }
        match &self.next {
            Some(next) => next.authenticate(request).await,
            None => Ok(None),
        }
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        match &self.next {
            Some(next) => next.permits(held, required),
            None => crate::auth::has_any_permission(held, required),
        }
    }

    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &http::Method,
    ) -> Result<(), ApiError> {
        match &self.next {
            Some(next) => next.called(principal, operation, method).await,
            None => Ok(()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn none(_: &ProviderSettings, _: &ProviderContext) -> Result<ProviderInstance, BoxError> {
        Err("never made".into())
    }

    #[test]
    fn a_definition_is_held_to_the_rules_the_build_holds_a_manifest_to() {
        let base = || ProviderDefinition::new("relay", RuntimeInterface::Channel, none);
        let refused = |definition: ProviderDefinition| define_provider(definition).unwrap_err().0;
        assert!(refused(ProviderDefinition::new(
            "Relay",
            RuntimeInterface::Channel,
            none
        ))
        .contains("lower-case"));
        assert!(refused(base().with_role(["messages"])).contains("names an area"));
        assert!(
            refused(base().with_setting("url", ProviderSettingSpec::plain()))
                .contains("upper-case")
        );
        assert!(
            refused(base().with_setting("CALLER_KEY", ProviderSettingSpec::plain()))
                .contains("the key the provider calls the app with")
        );
        assert!(refused(base().with_operations(["send"])).contains("no optional operation send"));
        assert!(refused(
            ProviderDefinition::new("m", RuntimeInterface::Model, none).with_operations(["embed"])
        )
        .contains("none"));
        assert!(define_provider(
            base()
                .with_role(["messages.relay"])
                .with_operations(["inbound"])
        )
        .is_ok());
    }

    #[test]
    fn a_manifest_is_held_to_the_definition() {
        let definition = ProviderDefinition::new("relay-a", RuntimeInterface::Channel, none)
            .with_setting("URL", ProviderSettingSpec::plain())
            .with_setting("TOKEN", ProviderSettingSpec::secret())
            .with_setting("SENDER", ProviderSettingSpec::plain().optional())
            .with_role(["messages.relay"]);
        let manifest = serde_json::json!({
            "name": "relay-a", "implements": "channel",
            "config": {"URL": {}, "TOKEN": {"secret": true}, "SENDER": {"optional": true, "description": "who it sends as"}},
            "role": ["messages.relay"]
        });
        check_provider_manifest(&manifest, &definition).unwrap();
        let with = |key: &str, value: Value| {
            let mut changed = manifest.clone();
            changed[key] = value;
            check_provider_manifest(&changed, &definition)
                .unwrap_err()
                .0
        };
        assert!(with(
            "operations",
            serde_json::json!(["send", "health", "inbound"])
        )
        .contains("lists operations"));
        assert!(with("role", serde_json::json!(["messages.send"])).contains("manifest's role"));
        assert!(with("config", serde_json::json!({"URL": {}, "TOKEN": {}}))
            .contains("manifest's config"));
        assert!(with("implements", serde_json::json!("model")).contains("implements"));
    }

    #[test]
    fn settings_print_their_keys_and_masked_secrets() {
        let settings = ProviderSettings::new(
            "SMS",
            BTreeMap::from([
                (
                    "URL".to_owned(),
                    ConfigValue::Text("http://relay.test".into()),
                ),
                (
                    "TOKEN".to_owned(),
                    ConfigValue::Secret(SecretValue::new("token-value-1")),
                ),
            ]),
        );
        assert_eq!(settings.text("URL").unwrap(), "http://relay.test");
        assert_eq!(settings.secret("TOKEN").unwrap().reveal(), "token-value-1");
        assert!(settings.text("TOKEN").unwrap_err().0.contains("secret()"));
        assert!(settings.secret("URL").unwrap_err().0.contains("text()"));
        assert!(settings.text("SENDER").unwrap_err().0.contains("not set"));
        let printed = format!("{settings:?} {}", serde_json::to_string(&settings).unwrap());
        assert!(!printed.contains("token-value-1"), "{printed}");
        assert_eq!(
            serde_json::to_value(&settings).unwrap(),
            serde_json::json!({"TOKEN": "[secret]", "URL": "http://relay.test"})
        );
    }

    #[tokio::test]
    async fn a_provider_is_authenticated_by_its_key_with_its_role_alone() {
        let auth = ExtensionAuthenticator::new(vec![ExtensionKey {
            key: SecretValue::new("bridge-key"),
            name: "chat-bridge".into(),
            role: vec!["messages.relay".into()],
        }]);
        let parts = |token: &str| {
            http::Request::builder()
                .header("authorization", format!("Bearer {token}"))
                .body(())
                .unwrap()
                .into_parts()
                .0
        };
        let principal = auth
            .authenticate(&parts("bridge-key"))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(principal.subject, "extension:chat-bridge");
        assert_eq!(principal.kind, PrincipalKind::Extension);
        assert_eq!(principal.permissions, ["messages.relay"]);
        assert!(auth.authenticate(&parts("other")).await.unwrap().is_none());
        assert!(
            crate::auth::admit(&auth, Some(principal.clone()), &["messages.relay".into()]).is_ok()
        );
        assert!(crate::auth::admit(&auth, Some(principal), &["notes.write".into()]).is_err());
    }
}
