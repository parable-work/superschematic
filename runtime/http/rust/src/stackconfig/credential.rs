//! A service's credential as the token source its client sends (section
//! 9.6 of `docs/stack-model.md`), the TypeScript runtime's
//! `serviceCredentialFor`.

use serde_json::Value;

use super::{Credential, Source, StackConfigError};
use crate::{is_pem, GoogleIdTokenSource, SignedKeySource, TokenFileSource, TokenSource};

/// The token source a [`Credential`] names, over the runtime's sources: a
/// Google ID token for its audience from the metadata server
/// ([`GoogleIdTokenSource`], which needs the `http-client` feature), the
/// token in its file ([`TokenFileSource`]), or a token signed with its key
/// as its issuer, for its audience ([`SignedKeySource`], `iss` and `sub`
/// both the issuer). It refuses a member the source reads that is missing,
/// a `signed-token` key that is neither an Ed25519 private JWK's JSON nor an
/// Ed25519 private key's PKCS#8 PEM, as the generic connector gives it
/// (D63), and a
/// `google-id-token` source without the `http-client` feature. Its
/// messages never carry the key.
///
/// A generated Rust SDK's `ServiceCredential` takes the token source and
/// the headers. The SDK spells its `ServiceTokenSource` out, since it does
/// not depend on this crate, and it is the same type as [`TokenSource`], so
/// the entrypoint builds a client's credential from a loaded
/// [`super::Service`] with no conversion:
///
/// ```ignore
/// let service_credential = match &endpoint.credential {
///     Some(credential) => Some(shop_api_sdk::ServiceCredential {
///         token: stackconfig::credential_source(credential)?,
///         headers: credential.header_names(),
///     }),
///     None => None,
/// };
/// ```
pub fn credential_source(credential: &Credential) -> Result<TokenSource, StackConfigError> {
    credential_source_with(credential, GoogleIdTokenSource::new)
}

/// [`credential_source`], with the Google ID token source `google` builds
/// for an audience, for tests.
pub(crate) fn credential_source_with(
    credential: &Credential,
    google: impl FnOnce(String) -> GoogleIdTokenSource,
) -> Result<TokenSource, StackConfigError> {
    let source = credential.source;
    let member = |value: &Option<String>, name: &str| {
        value
            .as_deref()
            .filter(|value| !value.is_empty())
            .map(str::to_string)
            .ok_or_else(|| StackConfigError::one(format!("a {source} credential has no {name}")))
    };
    match source {
        Source::GoogleIdToken => {
            let audience = member(&credential.audience, "audience")?;
            if cfg!(not(feature = "http-client")) {
                return Err(StackConfigError::one(
                    "a google-id-token credential asks the metadata server for its token, which \
                     needs the HTTP runtime's http-client feature",
                ));
            }
            Ok(google(audience).build())
        }
        Source::TokenFile => {
            let path = member(&credential.token_file, "token file")?;
            Ok(TokenFileSource::new(path).build())
        }
        Source::SignedToken => {
            let audience = member(&credential.audience, "audience")?;
            let issuer = member(&credential.issuer, "issuer")?;
            let key = member(&credential.key, "key")?;
            // The generic connector's key is a PKCS#8 PEM, the local
            // target's a JWK's JSON (D63).
            let signer = if is_pem(&key) {
                SignedKeySource::from_pem(&key, issuer.clone(), issuer, audience)
            } else {
                let jwk: Value = serde_json::from_str(&key).map_err(|_| {
                    StackConfigError::one(
                        "a signed-token credential's key is not a JWK's JSON or a PEM",
                    )
                })?;
                SignedKeySource::new(&jwk, issuer.clone(), issuer, audience)
            }
            .map_err(|err| StackConfigError::one(format!("a signed-token credential: {err}")))?;
            Ok(signer.build())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::jws::encode;
    use crate::{HttpGetter, JwtServiceAuthenticator, ServiceAuthConfig, ServiceCaller};
    use async_trait::async_trait;
    use http::HeaderMap;
    use ring::signature::{Ed25519KeyPair, KeyPair};
    use serde_json::json;
    use std::future::Future;
    use std::pin::Pin;
    use std::sync::{Arc, Mutex};

    fn credential(source: Source) -> Credential {
        Credential {
            source,
            audience: None,
            token_file: None,
            issuer: None,
            key: None,
            headers: Vec::new(),
        }
    }

    /// What a generated SDK's `ServiceTokenSource` is, spelled as the
    /// generated client spells it.
    type SdkServiceTokenSource = Arc<
        dyn Fn(bool) -> Pin<Box<dyn Future<Output = Result<String, String>> + Send>> + Send + Sync,
    >;

    #[tokio::test]
    async fn a_token_file_credential_reads_its_file_and_is_an_sdks_token_source() {
        let dir = std::env::temp_dir().join(format!("stackconfig-token-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("token");
        std::fs::write(&file, "the-token\n").unwrap();
        let mut token_file = credential(Source::TokenFile);
        token_file.token_file = Some(file.display().to_string());
        let sdk: SdkServiceTokenSource = credential_source(&token_file).unwrap();
        assert_eq!(sdk(false).await.unwrap(), "the-token");
        std::fs::remove_dir_all(&dir).unwrap();

        let refused = credential_source(&credential(Source::TokenFile))
            .err()
            .unwrap();
        assert_eq!(
            refused.problems(),
            ["a token-file credential has no token file"]
        );
    }

    #[tokio::test]
    async fn a_signed_token_credential_signs_as_its_issuer_for_its_audience() {
        let seed = [7u8; 32];
        let pair = Ed25519KeyPair::from_seed_unchecked(&seed).unwrap();
        let x = encode(pair.public_key().as_ref());
        let mut signed = credential(Source::SignedToken);
        signed.audience = Some("shop-api".to_string());
        signed.issuer = Some("Orders".to_string());
        signed.key = Some(
            json!({ "kty": "OKP", "crv": "Ed25519", "kid": "orders-key", "x": x, "d": encode(&seed) })
                .to_string(),
        );
        let token = credential_source(&signed).unwrap()(false).await.unwrap();

        let config: ServiceAuthConfig = serde_json::from_value(json!({
            "issuers": [{
                "issuer": "Orders", "audience": "shop-api", "algorithms": ["EdDSA"],
                "keys": [{ "kty": "OKP", "crv": "Ed25519", "kid": "orders-key", "x": x }],
                "callers": { "Orders": { "deployable": "Orders", "serves": ["shop-orders"] } }
            }]
        }))
        .unwrap();
        let mut headers = HeaderMap::new();
        headers.insert(
            crate::SERVICE_AUTHORIZATION,
            format!("Bearer {token}").parse().unwrap(),
        );
        let caller = JwtServiceAuthenticator::new(config)
            .unwrap()
            .verify(&headers)
            .await
            .unwrap();
        assert_eq!(
            caller,
            Some(ServiceCaller::new("Orders", ["shop-orders"], "Orders"))
        );

        signed.key = Some("{\"d\":\"secret\"".to_string());
        let refused = credential_source(&signed).err().unwrap().to_string();
        assert!(refused.contains("not a JWK"), "{refused}");
        assert!(!refused.contains("secret"), "{refused}");
        signed.key = Some(json!({ "kty": "EC", "d": "secret" }).to_string());
        let refused = credential_source(&signed).err().unwrap().to_string();
        assert!(!refused.contains("secret"), "{refused}");
        signed.key = None;
        assert!(credential_source(&signed).is_err());
    }

    /// Answers the identity request with a JWT whose `exp` is far off,
    /// recording the URL.
    struct Metadata(Arc<Mutex<Vec<String>>>);

    #[async_trait]
    impl HttpGetter for Metadata {
        async fn get(&self, url: &str, _headers: &[(&str, &str)]) -> Result<Vec<u8>, String> {
            self.0.lock().unwrap().push(url.to_string());
            let claims = encode(br#"{"exp":99999999999}"#);
            Ok(format!("{}.{claims}.sig", encode(br#"{"alg":"RS256"}"#)).into_bytes())
        }
    }

    #[tokio::test]
    async fn a_google_id_token_credential_asks_for_its_audience() {
        let mut google = credential(Source::GoogleIdToken);
        assert!(credential_source(&google).is_err(), "no audience");
        google.audience = Some("https://shop-api.run.app".to_string());
        let urls = Arc::new(Mutex::new(Vec::new()));
        let getter = Arc::new(Metadata(Arc::clone(&urls)));
        let built = credential_source_with(&google, |audience| {
            GoogleIdTokenSource::new(audience)
                .with_metadata_host("metadata.test")
                .with_getter(getter)
        });
        if cfg!(feature = "http-client") {
            assert!(built.unwrap()(false).await.is_ok());
            assert_eq!(
                urls.lock().unwrap().as_slice(),
                [
                    "http://metadata.test/computeMetadata/v1/instance/service-accounts/default/\
                  identity?audience=https%3A%2F%2Fshop-api.run.app"
                ]
            );
        } else {
            assert!(built.err().unwrap().to_string().contains("http-client"));
        }
    }
}
