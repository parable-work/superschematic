//! Service auth (D37): which deployable is calling, beside the end user.
//!
//! A calling service sends a short-lived JWT in `Service-Authorization:
//! Bearer <token>`, beside the end user's `Authorization`. The server's
//! [`ServiceAuthenticator`] reads only that header and establishes a
//! [`ServiceCaller`]; [`crate::RouteControls`] applies a route's
//! `@requireService` or `@allowService` rule to it. Services hold no
//! permissions: `from` lists the APIs a caller must serve.
//!
//! [`crate::JwtServiceAuthenticator`] is the implementation over a
//! [`ServiceAuthConfig`], the JSON the stack's connectors write, with the
//! same members in the Go and TypeScript runtimes. A deployment whose
//! credential that config cannot express passes its own authenticator.

use crate::{ApiError, JwsAlgorithm};
use async_trait::async_trait;
use http::request::Parts;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// The header a service credential travels in.
pub const SERVICE_AUTHORIZATION: &str = "service-authorization";

/// The calling service a [`ServiceAuthenticator`] verified.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ServiceCaller {
    /// The calling deployable's name in the stack.
    pub deployable: String,
    /// The APIs the caller serves, which a route's `from` is checked against.
    pub serves: Vec<String>,
    /// The credential's subject, for logs.
    pub subject: String,
}

impl ServiceCaller {
    pub fn new(
        deployable: impl Into<String>,
        serves: impl IntoIterator<Item = impl Into<String>>,
        subject: impl Into<String>,
    ) -> Self {
        Self {
            deployable: deployable.into(),
            serves: serves.into_iter().map(Into::into).collect(),
            subject: subject.into(),
        }
    }

    /// Whether a route's `from` lists this caller: `from` is empty, or the
    /// caller serves one of its APIs.
    pub fn is_listed(&self, from: &[String]) -> bool {
        from.is_empty() || self.serves.iter().any(|api| from.contains(api))
    }
}

/// Establishes the calling service of a request. The router runs it on every
/// route of a server that has one, after the rate limit and the body limit
/// and before the end-user step; it sees the request's head, not its body.
#[async_trait]
pub trait ServiceAuthenticator: Send + Sync + 'static {
    /// `Ok(None)` when the request carries no service credential. A
    /// credential that does not verify is [`ApiError::service_unauthorized`]
    /// (401), a verified identity that is no caller of this server
    /// [`ApiError::service_forbidden`] (403), and a failure that is not the
    /// caller's, such as keys that cannot be fetched,
    /// [`ApiError::service_unavailable`] (503).
    async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError>;
}

/// The callee's service auth config: what each inbound edge's connector
/// writes (section 9.2 of `docs/stack-model.md`). The JSON members are those
/// of the Go `serviceauth.Config` and the TypeScript `ServiceAuthConfig`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ServiceAuthConfig {
    pub issuers: Vec<ServiceIssuer>,
    /// Seconds of clock skew allowed on `exp`, `nbf` and `iat`; 60 when
    /// left out.
    #[serde(default = "default_leeway_seconds")]
    pub leeway_seconds: u64,
}

impl Default for ServiceAuthConfig {
    fn default() -> Self {
        Self {
            issuers: Vec::new(),
            leeway_seconds: default_leeway_seconds(),
        }
    }
}

/// One issuer of service credentials: Google, a Kubernetes cluster, or a
/// caller signing with an edge's key.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ServiceIssuer {
    /// The `iss` this entry verifies.
    pub issuer: String,
    /// Other spellings of `iss` (Google's `accounts.google.com`).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub issuer_aliases: Vec<String>,
    /// The audience the token must name in `aud`: this server.
    pub audience: String,
    pub algorithms: Vec<JwsAlgorithm>,
    /// Where to fetch the issuer's keys (a JWKS); or `keys`, not both.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jwks_url: Option<String>,
    /// With `jwks_url`: a file whose trimmed contents the fetch sends as
    /// `Authorization: Bearer` (a Kubernetes service account token).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jwks_bearer_token_file: Option<String>,
    /// The issuer's public JWKs, each with a `kid`; or `jwks_url`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keys: Option<Vec<serde_json::Map<String, serde_json::Value>>>,
    /// The claim that names the caller; `sub` when left out.
    #[serde(default = "default_subject_claim")]
    pub subject_claim: String,
    /// The longest `exp - iat` accepted; with it, `iat` is required.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_lifetime_seconds: Option<u64>,
    /// The callers of this server, keyed by the subject claim's value. An
    /// identity left out is no caller (403).
    #[serde(default)]
    pub callers: BTreeMap<String, ServiceCallerConfig>,
}

/// The deployable a caller identity is, and the APIs it serves.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ServiceCallerConfig {
    pub deployable: String,
    #[serde(default)]
    pub serves: Vec<String>,
}

fn default_leeway_seconds() -> u64 {
    60
}

fn default_subject_claim() -> String {
    "sub".to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_config_reads_the_shared_json() {
        let config: ServiceAuthConfig = serde_json::from_str(
            r#"{
              "issuers": [{
                "issuer": "https://accounts.google.com",
                "issuerAliases": ["accounts.google.com"],
                "audience": "https://shop-api-abc.a.run.app",
                "algorithms": ["RS256"],
                "jwksUrl": "https://www.googleapis.com/oauth2/v3/certs",
                "jwksBearerTokenFile": "/var/run/token",
                "maxLifetimeSeconds": 3600,
                "callers": {
                  "1234": { "deployable": "orders", "serves": ["shop-orders"] }
                }
              }, {
                "issuer": "orders",
                "audience": "shop-api",
                "algorithms": ["EdDSA"],
                "keys": [{ "kty": "OKP", "crv": "Ed25519", "x": "AA", "kid": "k1" }],
                "subjectClaim": "iss"
              }]
            }"#,
        )
        .unwrap();
        assert_eq!(config.leeway_seconds, 60);
        let google = &config.issuers[0];
        assert_eq!(google.issuer_aliases, ["accounts.google.com"]);
        assert_eq!(google.algorithms, [JwsAlgorithm::Rs256]);
        assert_eq!(
            google.jwks_bearer_token_file.as_deref(),
            Some("/var/run/token")
        );
        assert_eq!(google.subject_claim, "sub");
        assert_eq!(google.max_lifetime_seconds, Some(3600));
        assert_eq!(google.callers["1234"].serves, ["shop-orders"]);
        let keypair = &config.issuers[1];
        assert_eq!(keypair.subject_claim, "iss");
        assert_eq!(keypair.keys.as_ref().map(Vec::len), Some(1));
        assert!(keypair.callers.is_empty());

        let written = serde_json::to_value(&config).unwrap();
        assert_eq!(written["leewaySeconds"], 60);
        assert_eq!(
            written["issuers"][0]["jwksUrl"],
            google.jwks_url.as_deref().unwrap()
        );
        assert!(written["issuers"][1].get("jwksUrl").is_none());
    }

    #[test]
    fn a_caller_is_listed_when_from_is_empty_or_names_an_api_it_serves() {
        let caller = ServiceCaller::new("orders", ["shop-orders", "shop-admin"], "sa-1");
        assert!(caller.is_listed(&[]));
        assert!(caller.is_listed(&["shop-admin".to_string()]));
        assert!(!caller.is_listed(&["shop-stock".to_string()]));
        assert!(!ServiceCaller::new("job", Vec::<String>::new(), "s").is_listed(&["a".to_string()]));
    }
}
