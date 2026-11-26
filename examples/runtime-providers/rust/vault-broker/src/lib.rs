//! A credential provider over a password manager's service (D159, D185):
//! the Rust port of `providers/vault-broker`. Items are named by secret
//! reference, `vault://<vault>/<item>/<field>`, and the broker holds a
//! service account token limited to the vaults `VAULTS` names, which it
//! also refuses others by. `list` gives each item's title and sites;
//! `resolve` reads one field and seals it in an [`Injection`], which only
//! the runner opens. The value is never returned, logged or held as plain
//! text outside the call that seals it.
//!
//! The service is reached through a [`Service`]: [`definition`], which a
//! server links, reads the process's [`vault`], a stub of the service an
//! example stocks, since this is an example.

use std::sync::{Arc, Mutex, OnceLock};

use async_trait::async_trait;
use superschematic_http_runtime::providers::{
    CredentialListing, CredentialProvider, CredentialQuery, Injection, InjectionTarget,
    ProviderDefinition, ProviderInstance, ProviderSettingSpec, RuntimeInterface, SecretValue,
};
use superschematic_http_runtime::BoxError;

/// An item of a vault: its id, title, the sites it is for, and its
/// fields' values by label.
#[derive(Clone)]
pub struct Item {
    pub vault: String,
    pub id: String,
    pub title: String,
    pub urls: Vec<String>,
    pub fields: Vec<(String, SecretValue)>,
}

/// The password manager's service as a stub in the process: the items it
/// holds, and the token each read must carry.
#[derive(Default)]
pub struct Service {
    items: Mutex<Vec<Item>>,
    token: Mutex<Option<SecretValue>>,
}

impl Service {
    /// Stocks the service with `item`, which a read with `token` reaches.
    pub fn stock(&self, token: &str, item: Item) {
        *self.token.lock().unwrap() = Some(SecretValue::new(token));
        self.items.lock().unwrap().push(item);
    }

    fn items(&self, token: &SecretValue, vault: &str) -> Result<Vec<Item>, BoxError> {
        if self.token.lock().unwrap().as_ref() != Some(token) {
            return Err("vault-broker: the service answered 401".into());
        }
        Ok(self
            .items
            .lock()
            .unwrap()
            .iter()
            .filter(|item| item.vault == vault)
            .cloned()
            .collect())
    }
}

/// The process's service stub, which [`definition`] reads.
pub fn vault() -> Arc<Service> {
    static VAULT: OnceLock<Arc<Service>> = OnceLock::new();
    VAULT.get_or_init(|| Arc::new(Service::default())).clone()
}

/// A secret reference, read: `vault://<vault>/<item>/<field>`.
pub fn parse_reference(reference: &str) -> Result<(String, String, String), BoxError> {
    let parts: Vec<&str> = reference
        .strip_prefix("vault://")
        .map(|rest| rest.split('/').collect())
        .unwrap_or_default();
    match parts.as_slice() {
        [vault, item, field] if !vault.is_empty() && !item.is_empty() && !field.is_empty() => {
            Ok(((*vault).to_owned(), (*item).to_owned(), (*field).to_owned()))
        }
        _ => Err(format!(
            "vault-broker: {reference:?} is no secret reference; one is vault://<vault>/<item>/<field>"
        )
        .into()),
    }
}

/// The host of a site or a URL.
fn host(text: &str) -> &str {
    let rest = text.split_once("://").map_or(text, |(_, rest)| rest);
    rest.split('/').next().unwrap_or(rest)
}

/// Whether an item for `urls` may be used on `site`: the same host.
fn usable_on(urls: &[String], site: &str) -> bool {
    urls.iter().any(|url| host(url) == host(site))
}

struct Broker {
    token: SecretValue,
    vaults: Vec<String>,
    service: Arc<Service>,
}

impl Broker {
    fn in_scope(&self, vault: &str) -> Result<(), BoxError> {
        if self.vaults.iter().any(|v| v == vault) {
            return Ok(());
        }
        Err(format!(
            "vault-broker: vault {vault} is not one this broker may read ({})",
            self.vaults.join(", ")
        )
        .into())
    }
}

#[async_trait]
impl CredentialProvider for Broker {
    async fn list(&self, query: &CredentialQuery) -> Result<Vec<CredentialListing>, BoxError> {
        let mut listings = Vec::new();
        for vault in &self.vaults {
            for item in self.service.items(&self.token, vault)? {
                if query.site.as_deref().is_some_and(|site| !usable_on(&item.urls, site)) {
                    continue;
                }
                // Each item's password, the field a sign-in fills.
                listings.push(CredentialListing {
                    reference: format!("vault://{vault}/{}/password", item.id),
                    title: item.title,
                    sites: item.urls,
                });
            }
        }
        Ok(listings)
    }

    async fn resolve(&self, reference: &str, target: &InjectionTarget) -> Result<Injection, BoxError> {
        let (vault, id, label) = parse_reference(reference)?;
        self.in_scope(&vault)?;
        let item = self
            .service
            .items(&self.token, &vault)?
            .into_iter()
            .find(|item| item.id == id)
            .ok_or_else(|| format!("vault-broker: vault {vault} holds no item {id}"))?;
        if !usable_on(&item.urls, target.site()) {
            return Err(format!(
                "vault-broker: item {id} is not for {}; it is for {}",
                target.site(),
                item.urls.join(", ")
            )
            .into());
        }
        let (_, value) = item
            .fields
            .into_iter()
            .find(|(field, _)| *field == label)
            .ok_or_else(|| format!("vault-broker: item {id} has no field {label}"))?;
        Ok(Injection::into_target(reference, target.clone(), value))
    }
}

/// The definition, over `service`.
pub fn definition_with(service: Arc<Service>) -> ProviderDefinition {
    ProviderDefinition::new("vault-broker", RuntimeInterface::Credentials, move |settings, _| {
        let vaults = settings
            .text("VAULTS")?
            .split(',')
            .map(str::trim)
            .filter(|v| !v.is_empty())
            .map(str::to_owned)
            .collect();
        // The URL names the service a real broker calls; the stub has it
        // in the process.
        settings.text("URL")?;
        Ok(ProviderInstance::Credentials(Arc::new(Broker {
            token: settings.secret("TOKEN")?,
            vaults,
            service: service.clone(),
        })))
    })
    .with_setting("URL", ProviderSettingSpec::plain())
    .with_setting("TOKEN", ProviderSettingSpec::secret())
    .with_setting("VAULTS", ProviderSettingSpec::plain())
}

/// The definition a server links, over the process's [`vault`].
pub fn definition() -> ProviderDefinition {
    definition_with(vault())
}
