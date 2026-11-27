//! The credential provider (D159). Core never holds a credential's value:
//! `list` gives the titles and sites of the items a run may use, and
//! `resolve` gives an [`Injection`], which carries the value sealed. Only
//! [`apply_injection`] opens it, handing it to the runner that uses it, a
//! browser that fills a field or a client that sets a request header,
//! inside the process that runs the step. The debug, display and JSON
//! forms of an injection print its reference and target alone, so a log or
//! a run record that holds one holds no value. [`use_credential`] does the
//! three steps and returns what a run record keeps: the reference, the
//! provider, the target and the time it was used.
//!
//! An injection's fields are private to this module, and a provider makes
//! one only from a [`SecretValue`] ([`Injection::fill`],
//! [`Injection::header`]): no value of another type passes for one, as the
//! TypeScript class's private field makes it nominal.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::fmt;
use std::time::SystemTime;

use super::SecretValue;
use crate::BoxError;

/// An item a credential provider holds, as `list` gives it: never its
/// value.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CredentialListing {
    /// The secret reference the item is named by, in the provider's own
    /// form, such as `vault/item/field`.
    #[serde(rename = "ref")]
    pub reference: String,
    /// What a person calls the item.
    pub title: String,
    /// The sites the item may be used on, by origin or host.
    pub sites: Vec<String>,
}

/// What `list` narrows to: the items usable on one site.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct CredentialQuery {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub site: Option<String>,
}

/// Where an injection goes: a field a browser fills, or a header a request
/// carries, on one site.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "lowercase")]
pub enum InjectionTarget {
    Fill { site: String, selector: String },
    Header { site: String, name: String },
}

impl InjectionTarget {
    /// The site the target is on.
    pub fn site(&self) -> &str {
        match self {
            InjectionTarget::Fill { site, .. } | InjectionTarget::Header { site, .. } => site,
        }
    }

    fn kind(&self) -> &'static str {
        match self {
            InjectionTarget::Fill { .. } => "fill",
            InjectionTarget::Header { .. } => "header",
        }
    }
}

/// What applies an injection: the runner of a step, inside the process
/// that uses the value.
#[async_trait]
pub trait InjectionRunner: Send + Sync {
    /// Fills a browser field with the value.
    async fn fill(&self, selector: &str, value: &str) -> Result<(), BoxError>;
    /// Sets a header of the request the runner sends.
    async fn set_header(&self, name: &str, value: &str) -> Result<(), BoxError>;
}

/// What a run record keeps of a credential's use (D159): its reference,
/// never its value.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CredentialUse {
    #[serde(rename = "ref")]
    pub reference: String,
    /// The provider's name.
    pub provider: String,
    pub target: InjectionTarget,
    #[serde(with = "super::time")]
    pub used_at: SystemTime,
}

/// A credential provider: password managers and secret brokers implement
/// it. Neither operation gives core a value. A provider refuses a
/// reference its scope does not hold, and one whose item is not for the
/// target's site.
#[async_trait]
pub trait CredentialProvider: Send + Sync {
    /// The items a run may use, narrowed to a site: titles and sites only.
    async fn list(&self, query: &CredentialQuery) -> Result<Vec<CredentialListing>, BoxError>;
    /// The item `reference` names, sealed in an injection into `target`.
    async fn resolve(
        &self,
        reference: &str,
        target: &InjectionTarget,
    ) -> Result<Injection, BoxError>;
}

/// A credential's value on its way to the runner, sealed: no member
/// returns the value, and only [`apply_injection`] opens it.
pub struct Injection {
    reference: String,
    target: InjectionTarget,
    value: SecretValue,
}

impl Injection {
    /// An injection that fills the field `selector` names on `site` with
    /// `value`.
    pub fn fill(
        reference: impl Into<String>,
        site: impl Into<String>,
        selector: impl Into<String>,
        value: SecretValue,
    ) -> Self {
        Self {
            reference: reference.into(),
            target: InjectionTarget::Fill {
                site: site.into(),
                selector: selector.into(),
            },
            value,
        }
    }

    /// An injection that sets the header `name` of a request to `site` to
    /// `value`.
    pub fn header(
        reference: impl Into<String>,
        site: impl Into<String>,
        name: impl Into<String>,
        value: SecretValue,
    ) -> Self {
        Self {
            reference: reference.into(),
            target: InjectionTarget::Header {
                site: site.into(),
                name: name.into(),
            },
            value,
        }
    }

    /// An injection into `target`, of either kind.
    pub fn into_target(
        reference: impl Into<String>,
        target: InjectionTarget,
        value: SecretValue,
    ) -> Self {
        Self {
            reference: reference.into(),
            target,
            value,
        }
    }

    /// The reference it was resolved from.
    pub fn reference(&self) -> &str {
        &self.reference
    }

    /// Where it goes.
    pub fn target(&self) -> &InjectionTarget {
        &self.target
    }
}

impl fmt::Debug for Injection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Injection({} {})", self.target.kind(), self.reference)
    }
}

impl fmt::Display for Injection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(self, f)
    }
}

impl Serialize for Injection {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeStruct;
        let mut out = s.serialize_struct("Injection", 3)?;
        out.serialize_field("ref", &self.reference)?;
        out.serialize_field("target", &self.target)?;
        out.serialize_field("value", &self.value)?;
        out.end()
    }
}

/// Refuses an injection: not the reference and target asked for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InjectionError(pub String);

impl fmt::Display for InjectionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for InjectionError {}

/// Hands `injection`'s value to `runner`, the one place it is opened. It
/// takes the injection, so the value is gone once applied.
pub async fn apply_injection(
    injection: Injection,
    runner: &dyn InjectionRunner,
) -> Result<(), BoxError> {
    match &injection.target {
        InjectionTarget::Fill { selector, .. } => {
            runner.fill(selector, injection.value.reveal()).await
        }
        InjectionTarget::Header { name, .. } => {
            runner.set_header(name, injection.value.reveal()).await
        }
    }
}

/// What [`use_credential`] takes.
pub struct UseCredential<'a> {
    /// The provider, as `Providers::credentials` gives it.
    pub provider: &'a dyn CredentialProvider,
    /// The provider's name, which the use records.
    pub provider_name: &'a str,
    pub reference: &'a str,
    pub target: &'a InjectionTarget,
    pub runner: &'a dyn InjectionRunner,
    /// Records the use, a run record's credential log; the record holds
    /// the reference and the time.
    pub record: Option<&'a (dyn Fn(&CredentialUse) + Send + Sync)>,
    /// The time the use records, the system's unless set.
    pub now: Option<SystemTime>,
}

/// Resolves the reference into the target through the provider, applies
/// it in the runner and records the use. The caller gets the use, which
/// holds no value. An injection for another reference or another target
/// is refused before the runner sees it.
pub async fn use_credential(use_: UseCredential<'_>) -> Result<CredentialUse, BoxError> {
    let injection = use_.provider.resolve(use_.reference, use_.target).await?;
    if injection.reference != use_.reference || &injection.target != use_.target {
        return Err(Box::new(InjectionError(format!(
            "credential provider {} answered {} with an injection for {} into another target",
            use_.provider_name, use_.reference, injection.reference
        ))));
    }
    let target = injection.target.clone();
    apply_injection(injection, use_.runner).await?;
    let record = CredentialUse {
        reference: use_.reference.to_owned(),
        provider: use_.provider_name.to_owned(),
        target,
        used_at: use_.now.unwrap_or_else(SystemTime::now),
    };
    if let Some(keep) = use_.record {
        keep(&record);
    }
    Ok(record)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;
    use std::time::{Duration, UNIX_EPOCH};

    const SECRET: &str = "correct-horse-battery-staple";

    fn target() -> InjectionTarget {
        InjectionTarget::Header {
            site: "https://reports.test".into(),
            name: "Authorization".into(),
        }
    }

    struct Vault {
        reference: Option<&'static str>,
        site: Option<&'static str>,
    }

    #[async_trait]
    impl CredentialProvider for Vault {
        async fn list(&self, _query: &CredentialQuery) -> Result<Vec<CredentialListing>, BoxError> {
            Ok(vec![CredentialListing {
                reference: "ops/reports-api/token".into(),
                title: "Reports API".into(),
                sites: vec!["https://reports.test".into()],
            }])
        }

        async fn resolve(
            &self,
            reference: &str,
            into: &InjectionTarget,
        ) -> Result<Injection, BoxError> {
            let InjectionTarget::Header { site, name } = into else {
                return Err("header only".into());
            };
            Ok(Injection::header(
                self.reference.unwrap_or(reference),
                self.site.unwrap_or(site),
                name.clone(),
                SecretValue::new(format!("Bearer {SECRET}")),
            ))
        }
    }

    #[derive(Default)]
    struct Headers(Mutex<Vec<(String, String)>>);

    #[async_trait]
    impl InjectionRunner for Headers {
        async fn fill(&self, _selector: &str, _value: &str) -> Result<(), BoxError> {
            Err("no browser".into())
        }

        async fn set_header(&self, name: &str, value: &str) -> Result<(), BoxError> {
            self.0.lock().unwrap().push((name.into(), value.into()));
            Ok(())
        }
    }

    #[tokio::test]
    async fn the_value_reaches_the_runner_alone_and_the_record_holds_the_reference() {
        let vault = Vault {
            reference: None,
            site: None,
        };
        let runner = Headers::default();
        let records = Mutex::new(Vec::new());
        let keep = |use_: &CredentialUse| records.lock().unwrap().push(use_.clone());
        let at = UNIX_EPOCH + Duration::from_secs(1_795_608_000);
        let use_ = use_credential(UseCredential {
            provider: &vault,
            provider_name: "vault-broker",
            reference: "ops/reports-api/token",
            target: &target(),
            runner: &runner,
            record: Some(&keep),
            now: Some(at),
        })
        .await
        .unwrap();
        assert_eq!(
            runner.0.lock().unwrap().as_slice(),
            [("Authorization".to_owned(), format!("Bearer {SECRET}"))]
        );
        assert_eq!(
            serde_json::to_value(&use_).unwrap(),
            serde_json::json!({"ref": "ops/reports-api/token", "provider": "vault-broker", "target": {"kind": "header", "site": "https://reports.test", "name": "Authorization"}, "usedAt": "2026-11-25T12:00:00.000Z"})
        );
        assert_eq!(
            records.lock().unwrap().as_slice(),
            std::slice::from_ref(&use_)
        );

        let injection = vault
            .resolve("ops/reports-api/token", &target())
            .await
            .unwrap();
        let printed = [
            format!("{injection:?}"),
            injection.to_string(),
            serde_json::to_string(&injection).unwrap(),
            format!("{use_:?}"),
            serde_json::to_string(&vault.list(&CredentialQuery::default()).await.unwrap()).unwrap(),
        ]
        .join("\n");
        assert!(!printed.contains(SECRET), "{printed}");
        assert_eq!(
            serde_json::to_value(&injection).unwrap()["value"],
            serde_json::json!("[secret]")
        );
    }

    #[tokio::test]
    async fn an_injection_for_another_reference_or_target_is_refused_before_the_runner() {
        let runner = Headers::default();
        for (vault, said) in [
            (
                Vault {
                    reference: Some("ops/other"),
                    site: None,
                },
                "an injection for ops/other",
            ),
            (
                Vault {
                    reference: None,
                    site: Some("https://elsewhere.test"),
                },
                "into another target",
            ),
        ] {
            let err = use_credential(UseCredential {
                provider: &vault,
                provider_name: "swapped",
                reference: "ops/reports-api/token",
                target: &target(),
                runner: &runner,
                record: None,
                now: None,
            })
            .await
            .unwrap_err();
            assert!(err.to_string().contains(said), "{err}");
        }
        assert!(runner.0.lock().unwrap().is_empty());
    }
}
