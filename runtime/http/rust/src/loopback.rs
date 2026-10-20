//! A server's credential for its own calls between the APIs it serves
//! (section 9.9 of `docs/stack-model.md`).
//!
//! Such a call reaches the server on its own loopback address through the
//! callee's SDK client, and no connector gives it a credential, since no
//! edge between two deployables carries it. So the server makes one: when
//! it starts, it generates an Ed25519 key pair that only its process holds
//! ([`Loopback::generate`]). Each client of an API the server serves signs
//! the generic connector's token with it ([`crate::SignedKeySource`], D63):
//! `iss`, `sub` and `aud` the server's deployable, five minutes long. The
//! service authenticator of each API with a service clause accepts the key
//! as one more issuer beside its callers field's ([`Loopback::accept`]),
//! whose one caller is the server, serving every API it serves, as a caller
//! on another server is the deployable and every API it serves.
//!
//! No other process holds the key, so nothing outside the process can sign
//! as the server, and the key dies with the process: a token an earlier
//! process, or another instance of the server, signed does not verify. The
//! token goes only to a loopback URL ([`Loopback::credential_for`]), so it
//! never leaves the host. Nothing is configured or stored.
//!
//! This is the HTTP call between two APIs of one server. An in-process
//! call (D43) is not one: it runs the implementation without a request.

use crate::jws::{ed25519_thumbprint, encode};
use crate::stackconfig::SERVICE_AUTHORIZATION_HEADER;
use crate::{
    JwsAlgorithm, ServiceAuthConfig, ServiceCallerConfig, ServiceIssuer, SignedKeySource,
    TokenSource,
};
use ring::rand::{SecureRandom, SystemRandom};
use ring::signature::{Ed25519KeyPair, KeyPair};
use serde_json::{json, Map, Value};
use std::collections::BTreeMap;
use std::time::SystemTime;

/// The longest a loopback token lives: a [`SignedKeySource`] signs it for
/// 300 seconds, and the loopback issuer refuses a longer one.
pub const LOOPBACK_LIFETIME_SECONDS: u64 = 300;

/// A server's loopback key: the issuer it adds to a callers field, and the
/// token its in-server clients send.
pub struct Loopback {
    issuer: ServiceIssuer,
    token: TokenSource,
}

impl Loopback {
    /// Generates the loopback key of the server named `deployable`, which
    /// serves the APIs in `serves`, from a random seed.
    ///
    /// # Errors
    ///
    /// Why no key could be made: no random bytes, or no deployable.
    pub fn generate(
        deployable: impl Into<String>,
        serves: impl IntoIterator<Item = impl Into<String>>,
    ) -> Result<Self, String> {
        let mut seed = [0u8; 32];
        SystemRandom::new()
            .fill(&mut seed)
            .map_err(|_| "no random bytes for the loopback key".to_string())?;
        Self::with_seed(&seed, deployable, serves, SystemTime::now)
    }

    /// The loopback key whose Ed25519 seed is `seed`, its tokens signed on
    /// `clock`: for tests and the parity vectors.
    ///
    /// # Errors
    ///
    /// Why no key could be made from the seed, or no deployable.
    pub fn with_seed(
        seed: &[u8; 32],
        deployable: impl Into<String>,
        serves: impl IntoIterator<Item = impl Into<String>>,
        clock: impl Fn() -> SystemTime + Send + Sync + 'static,
    ) -> Result<Self, String> {
        let deployable = deployable.into();
        if deployable.is_empty() {
            return Err("a loopback key needs the server's deployable".to_string());
        }
        let pair = Ed25519KeyPair::from_seed_unchecked(seed)
            .map_err(|err| format!("the loopback key: {err}"))?;
        let x = encode(pair.public_key().as_ref());
        let kid = ed25519_thumbprint(pair.public_key().as_ref());
        let private =
            json!({ "kty": "OKP", "crv": "Ed25519", "d": encode(seed), "x": x, "kid": kid });
        let token = SignedKeySource::new(&private, &deployable, &deployable, &deployable)?
            .with_clock(clock)
            .build();
        let mut key = Map::new();
        key.insert("kty".to_string(), Value::from("OKP"));
        key.insert("kid".to_string(), Value::from(kid));
        key.insert("crv".to_string(), Value::from("Ed25519"));
        key.insert("x".to_string(), Value::from(x));
        let mut callers = BTreeMap::new();
        callers.insert(
            deployable.clone(),
            ServiceCallerConfig {
                deployable: deployable.clone(),
                serves: serves.into_iter().map(Into::into).collect(),
            },
        );
        Ok(Self {
            issuer: ServiceIssuer {
                issuer: deployable.clone(),
                issuer_aliases: Vec::new(),
                audience: deployable,
                algorithms: vec![JwsAlgorithm::EdDsa],
                jwks_url: None,
                jwks_bearer_token_file: None,
                keys: Some(vec![key]),
                subject_claim: "sub".to_string(),
                max_lifetime_seconds: Some(LOOPBACK_LIFETIME_SECONDS),
                callers,
            },
            token,
        })
    }

    /// The loopback issuer: the server's deployable as issuer, audience and
    /// the one caller, named in `sub`, serving every API the server serves;
    /// the key's public half as the one key; EdDSA; and 300 seconds at most.
    pub fn issuer(&self) -> ServiceIssuer {
        self.issuer.clone()
    }

    /// An API's callers field with the loopback issuer added after its own,
    /// for a [`crate::JwtServiceAuthenticator`] to verify like any other.
    ///
    /// # Errors
    ///
    /// A field that already names an issuer, or an alias, the loopback
    /// issuer's name: no connector writes one, since a server is no caller of
    /// itself through an edge, so such a field was written by hand.
    pub fn accept(&self, mut config: ServiceAuthConfig) -> Result<ServiceAuthConfig, String> {
        let name = &self.issuer.issuer;
        if let Some(i) = config
            .issuers
            .iter()
            .position(|entry| &entry.issuer == name || entry.issuer_aliases.contains(name))
        {
            return Err(format!(
                "issuers[{i}] is named {name:?}, the server's own loopback issuer; a callers \
                 field lists the server's callers, never the server"
            ));
        }
        config.issuers.push(self.issuer());
        Ok(config)
    }

    /// Signs the loopback tokens; every in-server client shares it.
    pub fn token_source(&self) -> TokenSource {
        TokenSource::clone(&self.token)
    }

    /// The credential an in-server client sends to `url`, the URL its edge
    /// derives, and the headers that carry it: the loopback token in
    /// `Service-Authorization` for a loopback URL ([`is_loopback_url`]), and
    /// none for any other, which may reach what is not this process.
    pub fn credential_for(&self, url: &str) -> Option<(TokenSource, Vec<String>)> {
        is_loopback_url(url).then(|| {
            (
                self.token_source(),
                vec![SERVICE_AUTHORIZATION_HEADER.to_string()],
            )
        })
    }
}

/// Whether `url` is an http or https URL whose host only this host answers,
/// read literally, as the Go and TypeScript runtimes read it
/// (`serviceauth_parity.json`'s `loopback.urls`): the scheme http or https in
/// any case, `://`, then a host that is `localhost` in any case, `[::1]`, or
/// four decimal octets from 0 to 255 whose first is 127, written without
/// leading zeros; then nothing, or a colon and a port of decimal digits,
/// before the path, query or fragment. A URL with user information, a
/// percent-encoded or shortened host, or any other spelling of a loopback
/// address is not one.
pub fn is_loopback_url(url: &str) -> bool {
    let rest = ["http://", "https://"].iter().find_map(|scheme| {
        url.get(..scheme.len())
            .filter(|prefix| prefix.eq_ignore_ascii_case(scheme))
            .map(|_| &url[scheme.len()..])
    });
    let Some(rest) = rest else {
        return false;
    };
    let authority = rest.find(['/', '?', '#']).map_or(rest, |end| &rest[..end]);
    let (host, port) = if authority.starts_with('[') {
        match authority.find(']') {
            Some(end) => authority.split_at(end + 1),
            None => return false,
        }
    } else {
        authority
            .find(':')
            .map_or((authority, ""), |colon| authority.split_at(colon))
    };
    if !port.is_empty() {
        let digits = &port[1..];
        if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
            return false;
        }
    }
    host.eq_ignore_ascii_case("localhost") || host == "[::1]" || is_loopback_ipv4(host)
}

/// Whether `host` is four decimal octets from 0 to 255, without leading
/// zeros, the first 127.
fn is_loopback_ipv4(host: &str) -> bool {
    let octets: Vec<&str> = host.split('.').collect();
    octets.len() == 4
        && octets[0] == "127"
        && octets.iter().all(|octet| {
            !octet.is_empty()
                && octet.len() <= 3
                && octet.bytes().all(|b| b.is_ascii_digit())
                && (octet.len() == 1 || !octet.starts_with('0'))
                && octet.parse::<u16>().is_ok_and(|n| n <= 255)
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{JwtServiceAuthenticator, ServiceAuthenticator, ServiceCaller};

    async fn authenticate(
        authenticator: &JwtServiceAuthenticator,
        token: &str,
    ) -> Result<Option<ServiceCaller>, crate::ApiError> {
        let (parts, ()) = http::Request::builder()
            .header("service-authorization", format!("Bearer {token}"))
            .body(())
            .unwrap()
            .into_parts();
        authenticator.authenticate(&parts).await
    }

    #[tokio::test]
    async fn a_random_key_admits_the_server_as_itself_and_another_key_is_refused() {
        let loopback = Loopback::generate("Monolith", ["shop-api", "shop-orders"]).unwrap();
        let authenticator =
            JwtServiceAuthenticator::new(loopback.accept(ServiceAuthConfig::default()).unwrap())
                .unwrap();
        let token = loopback.token_source()(false).await.unwrap();
        assert_eq!(
            authenticate(&authenticator, &token).await.unwrap(),
            Some(ServiceCaller::new(
                "Monolith",
                ["shop-api", "shop-orders"],
                "Monolith"
            ))
        );
        let earlier = Loopback::generate("Monolith", ["shop-api"]).unwrap();
        assert_ne!(earlier.issuer().keys, loopback.issuer().keys);
        let refused = authenticate(
            &authenticator,
            &earlier.token_source()(false).await.unwrap(),
        )
        .await
        .unwrap_err();
        assert_eq!(refused.status, http::StatusCode::UNAUTHORIZED);
    }

    #[test]
    fn the_credential_goes_to_a_loopback_url_alone_and_a_field_naming_the_server_is_refused() {
        let loopback = Loopback::generate("Monolith", Vec::<String>::new()).unwrap();
        let (_, headers) = loopback.credential_for("http://127.0.0.1:8080").unwrap();
        assert_eq!(headers, ["Service-Authorization"]);
        assert!(loopback
            .credential_for("https://shop-api-abc.a.run.app")
            .is_none());
        let named = ServiceAuthConfig {
            issuers: vec![loopback.issuer()],
            ..ServiceAuthConfig::default()
        };
        let err = loopback.accept(named).unwrap_err();
        assert!(err.contains("the server's own loopback issuer"), "{err}");
        assert!(Loopback::generate("", Vec::<String>::new()).is_err());
    }
}
