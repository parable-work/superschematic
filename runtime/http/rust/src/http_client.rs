//! The `http-client` feature: [`HttpFetcher`], a reqwest client that fetches
//! an issuer's JWKS for [`crate::JwtServiceAuthenticator`] and asks the
//! Google metadata server for [`crate::GoogleIdTokenSource`]. It uses the
//! TLS of the generated Rust SDKs (native-tls), so a service that also calls
//! other APIs builds one TLS stack.

use crate::{HttpGetter, KeyFetcher};
use async_trait::async_trait;
use std::time::Duration;

/// A request gives up after this long.
const TIMEOUT: Duration = Duration::from_secs(10);
/// A larger answer is refused: a JWKS or an ID token is a few kilobytes.
const MAX_BODY_BYTES: usize = 1024 * 1024;

/// An HTTP client for JWKS fetches and the metadata server. [`HttpFetcher::new`]
/// trusts the system's roots; a JWKS served under a private CA (a Kubernetes
/// API server's) needs a client that trusts it, passed to
/// [`HttpFetcher::from_client`].
#[derive(Clone, Debug)]
pub struct HttpFetcher {
    client: Result<reqwest::Client, String>,
}

impl HttpFetcher {
    pub fn new() -> Self {
        Self {
            client: reqwest::Client::builder()
                .timeout(TIMEOUT)
                .build()
                .map_err(|err| format!("building the HTTP client: {err}")),
        }
    }

    pub fn from_client(client: reqwest::Client) -> Self {
        Self { client: Ok(client) }
    }

    async fn request(&self, url: &str, headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
        let client = self.client.as_ref().map_err(Clone::clone)?;
        let mut request = client.get(url);
        for (name, value) in headers {
            request = request.header(*name, *value);
        }
        let mut response = request
            .send()
            .await
            .map_err(|err| format!("GET {url}: {err}"))?;
        let status = response.status();
        if !status.is_success() {
            return Err(format!("GET {url}: {status}"));
        }
        let mut body = Vec::new();
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|err| format!("GET {url}: {err}"))?
        {
            if body.len() + chunk.len() > MAX_BODY_BYTES {
                return Err(format!("GET {url}: the answer is too large"));
            }
            body.extend_from_slice(&chunk);
        }
        Ok(body)
    }
}

impl Default for HttpFetcher {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl KeyFetcher for HttpFetcher {
    async fn fetch(&self, url: &str, bearer: Option<&str>) -> Result<Vec<u8>, String> {
        match bearer {
            Some(token) => {
                let authorization = format!("Bearer {token}");
                self.request(url, &[("authorization", &authorization)])
                    .await
            }
            None => self.request(url, &[]).await,
        }
    }
}

#[async_trait]
impl HttpGetter for HttpFetcher {
    async fn get(&self, url: &str, headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
        self.request(url, headers).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::jws::testing::Signer;
    use crate::{JwtServiceAuthenticator, ServiceAuthConfig, ServiceCaller, SERVICE_AUTHORIZATION};
    use axum::http::{HeaderMap, StatusCode};
    use axum::routing::get;
    use axum::Router;
    use serde_json::{json, Value};
    use std::time::{SystemTime, UNIX_EPOCH};

    /// Serves `/jwks` to a request with `Authorization: Bearer k8s`, and
    /// `/identity` to one with `Metadata-Flavor: Google`; 403 otherwise.
    async fn serve(jwks: Value) -> String {
        let app = Router::new()
            .route(
                "/jwks",
                get(move |headers: HeaderMap| async move {
                    match headers.get("authorization").map(|v| v.to_str().unwrap()) {
                        Some("Bearer k8s") => Ok(jwks.to_string()),
                        _ => Err(StatusCode::FORBIDDEN),
                    }
                }),
            )
            .route(
                "/identity",
                get(|headers: HeaderMap| async move {
                    match headers.get("metadata-flavor").map(|v| v.to_str().unwrap()) {
                        Some("Google") => Ok("a.b.c"),
                        _ => Err(StatusCode::FORBIDDEN),
                    }
                }),
            );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        format!("http://{address}")
    }

    #[tokio::test]
    async fn a_fetch_sends_the_bearer_and_refuses_an_answer_that_is_not_2xx() {
        let base = serve(json!({ "keys": [] })).await;
        let fetcher = HttpFetcher::new();
        let url = format!("{base}/jwks");
        let body = fetcher.fetch(&url, Some("k8s")).await.unwrap();
        assert_eq!(body, br#"{"keys":[]}"#);
        let err = fetcher.fetch(&url, None).await.unwrap_err();
        assert!(err.contains("403"), "{err}");

        let identity = format!("{base}/identity");
        let token = fetcher
            .get(&identity, &[("Metadata-Flavor", "Google")])
            .await
            .unwrap();
        assert_eq!(token, b"a.b.c");
        assert!(fetcher.get(&identity, &[]).await.is_err());
    }

    #[tokio::test]
    async fn the_authenticator_fetches_a_jwks_over_http_by_default() {
        let signer = Signer::ed25519(8);
        let base = serve(json!({ "keys": [signer.jwk("ed-1")] })).await;
        let dir = std::env::temp_dir().join(format!("sa-http-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("token");
        std::fs::write(&file, "k8s").unwrap();
        let config: ServiceAuthConfig = serde_json::from_value(json!({
            "issuers": [{
                "issuer": "https://cluster.test", "audience": "shop-api",
                "algorithms": ["EdDSA"], "jwksUrl": format!("{base}/jwks"),
                "jwksBearerTokenFile": file.display().to_string(),
                "callers": { "sa:orders": { "deployable": "orders", "serves": ["shop-orders"] } }
            }]
        }))
        .unwrap();
        let authenticator = JwtServiceAuthenticator::new(config).unwrap();
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs();
        let token = signer.token(
            &json!({ "alg": "EdDSA", "kid": "ed-1" }),
            &json!({ "iss": "https://cluster.test", "aud": "shop-api", "sub": "sa:orders", "exp": now + 60 }),
        );
        let mut headers = HeaderMap::new();
        headers.insert(
            SERVICE_AUTHORIZATION,
            format!("Bearer {token}").parse().unwrap(),
        );
        assert_eq!(
            authenticator.verify(&headers).await.unwrap(),
            Some(ServiceCaller::new("orders", ["shop-orders"], "sa:orders"))
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
