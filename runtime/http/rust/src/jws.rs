//! The parts of JOSE a service credential needs: a compact JWS (RFC 7515)
//! split and decoded, and public JWKs (RFC 7517, 7518 and 8037) checked with
//! ring. Nothing here decides whether a token is good; `jwt.rs` does.

use base64::alphabet;
use base64::engine::general_purpose::{GeneralPurpose, GeneralPurposeConfig};
use base64::engine::DecodePaddingMode;
use base64::Engine;
use ring::signature::{self, RsaPublicKeyComponents, UnparsedPublicKey};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

/// An algorithm a service credential may be signed with. There is no `none`
/// and no HMAC: a callee holds no secret it could sign with as its caller.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum JwsAlgorithm {
    /// RSASSA-PKCS1-v1_5 with SHA-256, over an RSA key of 2048 to 8192 bits.
    #[serde(rename = "RS256")]
    Rs256,
    /// ECDSA over P-256 with SHA-256; the signature is `r || s`, 64 bytes.
    #[serde(rename = "ES256")]
    Es256,
    /// Ed25519. A token header's `alg` of `Ed25519` (RFC 9864) reads as this.
    #[serde(rename = "EdDSA", alias = "Ed25519")]
    EdDsa,
}

impl JwsAlgorithm {
    /// The algorithm a token header's `alg` names, if it is one of these.
    pub fn from_header(alg: &str) -> Option<Self> {
        match alg {
            "RS256" => Some(Self::Rs256),
            "ES256" => Some(Self::Es256),
            "EdDSA" | "Ed25519" => Some(Self::EdDsa),
            _ => None,
        }
    }
}

/// The smallest RSA modulus read, as RFC 7518 requires for RS256.
const MIN_RSA_BITS: usize = 2048;

/// Unpadded base64url, as JOSE writes every binary member. Like Go's
/// `base64.RawURLEncoding`, it refuses padding and allows nonzero trailing
/// bits.
const BASE64URL: GeneralPurpose = GeneralPurpose::new(
    &alphabet::URL_SAFE,
    GeneralPurposeConfig::new()
        .with_encode_padding(false)
        .with_decode_padding_mode(DecodePaddingMode::RequireNone)
        .with_decode_allow_trailing_bits(true),
);

pub(crate) fn decode(segment: &str) -> Option<Vec<u8>> {
    BASE64URL.decode(segment).ok()
}

pub(crate) fn encode(bytes: &[u8]) -> String {
    BASE64URL.encode(bytes)
}

/// A compact JWS, split and decoded but not verified.
pub(crate) struct Jws<'a> {
    pub header: Map<String, Value>,
    pub payload: Map<String, Value>,
    /// `<header>.<payload>` as sent, which the signature covers.
    pub signing_input: &'a str,
    pub signature: Vec<u8>,
}

impl<'a> Jws<'a> {
    /// Three base64url segments, the first two JSON objects; `None` otherwise.
    pub fn parse(token: &'a str) -> Option<Self> {
        let (signing_input, signature) = token.rsplit_once('.')?;
        let (header, payload) = signing_input.split_once('.')?;
        if payload.contains('.') {
            return None;
        }
        Some(Self {
            header: object(header)?,
            payload: object(payload)?,
            signing_input,
            signature: decode(signature)?,
        })
    }
}

/// The JSON object a segment encodes; `None` for anything else.
pub(crate) fn object(segment: &str) -> Option<Map<String, Value>> {
    match serde_json::from_slice(&decode(segment)?).ok()? {
        Value::Object(map) => Some(map),
        _ => None,
    }
}

/// A public key from a JWK, held in the form ring verifies with.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum PublicKey {
    /// `kty` `RSA`: the modulus and exponent, big-endian, no leading zeros.
    Rsa { n: Vec<u8>, e: Vec<u8> },
    /// `kty` `EC`, `crv` `P-256`: the uncompressed point `04 || x || y`.
    P256(Vec<u8>),
    /// `kty` `OKP`, `crv` `Ed25519`: the 32-byte public key.
    Ed25519(Vec<u8>),
}

impl PublicKey {
    /// The key a public JWK holds, as the Go runtime reads one: a key with
    /// the private member `d` is refused, since it belongs in neither a
    /// callee's config nor a published key set, and so is an RSA modulus
    /// under 2048 bits. Padding on a member is tolerated. Its `kid`, `use`
    /// and `alg` are the caller's business.
    pub fn from_jwk(jwk: &Map<String, Value>) -> Result<Self, String> {
        if jwk.contains_key("d") {
            return Err("the key carries a private member (d)".to_string());
        }
        let member = |name: &str| -> Result<Vec<u8>, String> {
            jwk.get(name)
                .and_then(Value::as_str)
                .filter(|value| !value.is_empty())
                .and_then(|value| decode(value.trim_end_matches('=')))
                .ok_or_else(|| format!("the key has no base64url member {name}"))
        };
        let sized = |name: &str, len: usize| -> Result<Vec<u8>, String> {
            let bytes = member(name)?;
            if bytes.len() == len {
                Ok(bytes)
            } else {
                Err(format!("the key's {name} is not {len} bytes"))
            }
        };
        let kty = jwk.get("kty").and_then(Value::as_str).unwrap_or_default();
        let crv = jwk.get("crv").and_then(Value::as_str).unwrap_or_default();
        match (kty, crv) {
            ("RSA", _) => {
                let n = without_leading_zeros(member("n")?);
                let bits = (n.len() * 8)
                    .saturating_sub(n.first().map_or(0, |b| b.leading_zeros()) as usize);
                if bits < MIN_RSA_BITS {
                    return Err(format!(
                        "the RSA modulus has {bits} bits, under {MIN_RSA_BITS}"
                    ));
                }
                let e = without_leading_zeros(member("e")?);
                Ok(Self::Rsa { n, e })
            }
            ("EC", "P-256") => {
                let mut point = vec![4];
                point.extend(sized("x", 32)?);
                point.extend(sized("y", 32)?);
                Ok(Self::P256(point))
            }
            ("OKP", "Ed25519") => Ok(Self::Ed25519(sized("x", 32)?)),
            _ => Err(format!("unsupported key type {kty:?} {crv:?}")),
        }
    }

    /// Whether `signature` is this key's signature of `message` under
    /// `algorithm`. A key of another type than the algorithm's is false.
    pub fn verify(&self, algorithm: JwsAlgorithm, message: &[u8], signature: &[u8]) -> bool {
        match (self, algorithm) {
            (Self::Rsa { n, e }, JwsAlgorithm::Rs256) => RsaPublicKeyComponents { n, e }
                .verify(&signature::RSA_PKCS1_2048_8192_SHA256, message, signature)
                .is_ok(),
            (Self::P256(point), JwsAlgorithm::Es256) => {
                UnparsedPublicKey::new(&signature::ECDSA_P256_SHA256_FIXED, point)
                    .verify(message, signature)
                    .is_ok()
            }
            (Self::Ed25519(key), JwsAlgorithm::EdDsa) => {
                UnparsedPublicKey::new(&signature::ED25519, key)
                    .verify(message, signature)
                    .is_ok()
            }
            _ => false,
        }
    }
}

fn without_leading_zeros(mut bytes: Vec<u8>) -> Vec<u8> {
    let zeros = bytes.iter().take_while(|b| **b == 0).count();
    bytes.drain(..zeros);
    bytes
}

/// Test keys and a signer for tokens, shared by the verifier's and the
/// sources' tests.
#[cfg(test)]
pub(crate) mod testing {
    use super::*;
    use ring::rand::SystemRandom;
    use ring::signature::{EcdsaKeyPair, Ed25519KeyPair, KeyPair, RsaKeyPair};
    use serde_json::json;

    /// A 2048-bit RSA key, a DER RSAPrivateKey (PKCS #1), for tests only.
    const RSA_DER: &str = concat!(
        "MIIEpAIBAAKCAQEA5hy+NFQqP9l45CvUDk3cPPT3YurtP66DhFzSaJQgt8L7TgAa",
        "yVRQteqPrCh6UyQHSntCfGwUIDoyB1LHwoyTK67h1AnlyKuP3sE/W5vLtbnbcvQM",
        "sUl5VYYXzoXFnRfuHEJkEFJdJ3WS+1/xxp28UsKxlrmsMY3fG0KUSZLFUGAhFJ0D",
        "lS5Zd14QT2HJBfs70j61Cc+lf9nV2YMpWb3/TICbUpMjTtssQw0VhLWCM04b51hL",
        "xLDG55rlHzBQaCtRIwp/L4h7NlJYs6b1oq31U95devPgydBGRyIQHJCVrfSGIuk3",
        "CcNsS1liNusjI1MoZScwmgd6qYnjVk8C3xaCmQIDAQABAoIBACdxXtL2aEWIf9XQ",
        "g2kuRGV4cd0VOrRzM9zg0joVxePpuoy8rNq8ppcADT9rssgEgXFtXlCYb/y0LPYf",
        "ZNk+ok1XDSN8zNPQQHlks3j+4/SS1oBGP9S1rOQRd4wxVtWeD/TtFlzL2WbVmuuW",
        "nwk16V1gPPOCgPb/g/IWv/c6frLPbDKggw9gPBf6HH+ohskTbysE+pk7u1dESdB+",
        "A05wvODy7v9xc1IDFo/ES8tJwnvE4scAJQuQcK10hukrVTiwZj72Z24SWNhDG0n6",
        "dXTy9FYuwQ7VMfMdNBDyVEYpTUU8E4zIr1ErhIr3zWPNGSSQJ9zp/KZ4iUNPvrrq",
        "Vd99Y08CgYEA9+rNImqzEm0GXPCBNe3Y32RhIhDTQDxfnxVIvFcAMvG/vGwGiqNl",
        "EIMrX9+WKN6dbXhx/9urfJxv9zW0FYR4klWvpqEC+dQesUKBh88OqFGcHSBFH2J0",
        "Vh87KVZnLR2/0QxTXq0dxow5352QVoeakR9+QcTGcifRXvxa8ytKMH8CgYEA7Z1W",
        "At+XGoGDS+n1MyxR/AV9GCKaOvefD9Yn234kiPxsNfrmpBlrlvBM0+gnrvuIAIKi",
        "+hH7895p/VAcIaOFjmbKJlYIX35wPMa2ktC1EbVyS+UV8u21REz+T+ZcYKeaLiKU",
        "ELjsZBeDgAKYLsG57P3DmyFL7rOg4kXCxgp7QOcCgYEA8YcnYqhg3UqqMFF/EHMw",
        "HjNFDlMl/CbVYb7ypcp8vyUWjxMPLHITPAsObtD9EcQPy17UcVgpsbUWv9jqISx1",
        "6trfzY5/v7UQUdFhMFZhCUq4tQeDUBgzDtROZu9uhV2+SoOflVVC9PQYTerLfAGQ",
        "bGIqNxjl3ME+ETP5x34dQTECgYBg4oBJl6Vi91/zuidygCXFnu9Mwf8lAAZpTKbf",
        "xmVbPaFZuT4Ftx+5Ya3R0Z0sqf7gRmPxlxembg/Fa76ssKIqWBsg2n97gHB/N38G",
        "CfdqixNZgsUaUnZQrRwctA8CkhQ5r3uz/dLVVQkXTveCSRdoXGg/fqoZYEC/QjaS",
        "zX9IDQKBgQCeJX1jTKwfM78erz18u++bxAOzT9n4NiNzra46J5l7eTId+mIQtFP9",
        "TIC94zLQFfsFWa1gDP7/WDD85Y5UiLloI+BF3ozslbgosudk/D1KxGJqPIWPTRNG",
        "VTNbu0Ocai1RTvEECdICiFdwA6y2YuGReyDqKhL2/bvULovkC569Dw==",
    );

    pub(crate) enum Signer {
        Rsa(RsaKeyPair),
        P256(EcdsaKeyPair),
        Ed25519(Ed25519KeyPair),
    }

    impl Signer {
        pub fn rsa() -> Self {
            let der = decode_std(RSA_DER);
            Self::Rsa(RsaKeyPair::from_der(&der).expect("the test RSA key"))
        }

        pub fn p256() -> Self {
            let rng = SystemRandom::new();
            let alg = &signature::ECDSA_P256_SHA256_FIXED_SIGNING;
            let pkcs8 = EcdsaKeyPair::generate_pkcs8(alg, &rng).unwrap();
            Self::P256(EcdsaKeyPair::from_pkcs8(alg, pkcs8.as_ref(), &rng).unwrap())
        }

        pub fn ed25519(seed: u8) -> Self {
            Self::Ed25519(Ed25519KeyPair::from_seed_unchecked(&[seed; 32]).unwrap())
        }

        /// The public JWK, with `kid`.
        pub fn jwk(&self, kid: &str) -> Value {
            match self {
                Self::Rsa(pair) => {
                    let public = ring::rsa::PublicKeyComponents::<Vec<u8>>::from(pair.public());
                    json!({ "kty": "RSA", "kid": kid, "n": encode(&public.n), "e": encode(&public.e) })
                }
                Self::P256(pair) => {
                    let point = pair.public_key().as_ref();
                    json!({
                        "kty": "EC", "crv": "P-256", "kid": kid,
                        "x": encode(&point[1..33]), "y": encode(&point[33..65]),
                    })
                }
                Self::Ed25519(pair) => {
                    json!({ "kty": "OKP", "crv": "Ed25519", "kid": kid, "x": encode(pair.public_key().as_ref()) })
                }
            }
        }

        pub fn sign(&self, message: &[u8]) -> Vec<u8> {
            let rng = SystemRandom::new();
            match self {
                Self::Rsa(pair) => {
                    let mut signature = vec![0; pair.public().modulus_len()];
                    pair.sign(&signature::RSA_PKCS1_SHA256, &rng, message, &mut signature)
                        .unwrap();
                    signature
                }
                Self::P256(pair) => pair.sign(&rng, message).unwrap().as_ref().to_vec(),
                Self::Ed25519(pair) => pair.sign(message).as_ref().to_vec(),
            }
        }

        /// A compact JWS of `header` and `payload`, signed with this key.
        pub fn token(&self, header: &Value, payload: &Value) -> String {
            let input = format!(
                "{}.{}",
                encode(header.to_string().as_bytes()),
                encode(payload.to_string().as_bytes())
            );
            let signature = encode(&self.sign(input.as_bytes()));
            format!("{input}.{signature}")
        }
    }

    fn decode_std(text: &str) -> Vec<u8> {
        base64::engine::general_purpose::STANDARD
            .decode(text)
            .expect("base64")
    }
}

#[cfg(test)]
mod tests {
    use super::testing::Signer;
    use super::*;
    use serde_json::json;

    fn key(signer: &Signer) -> PublicKey {
        match signer.jwk("k") {
            Value::Object(jwk) => PublicKey::from_jwk(&jwk).unwrap(),
            _ => unreachable!(),
        }
    }

    #[test]
    fn a_token_splits_into_two_objects_and_a_signature() {
        let token = format!(
            "{}.{}.{}",
            encode(br#"{"alg":"EdDSA"}"#),
            encode(br#"{"iss":"a"}"#),
            encode(b"sig")
        );
        let jws = Jws::parse(&token).unwrap();
        assert_eq!(jws.header["alg"], "EdDSA");
        assert_eq!(jws.payload["iss"], "a");
        assert_eq!(jws.signature, b"sig");
        assert_eq!(jws.signing_input, token.rsplit_once('.').unwrap().0);

        let object = encode(b"{}");
        for bad in [
            format!("{object}.{object}"),
            format!("{object}.{object}.{object}.{object}"),
            format!("{object}.{}.sig", encode(b"[]")),
            format!("{object}.{}.sig", encode(b"not json")),
            format!("{object}=.{object}.sig"),
            format!("{object}.{object}.s+g"),
        ] {
            assert!(Jws::parse(&bad).is_none(), "{bad}");
        }
    }

    #[test]
    fn each_key_type_verifies_only_its_own_algorithm() {
        let cases = [
            (Signer::rsa(), JwsAlgorithm::Rs256),
            (Signer::p256(), JwsAlgorithm::Es256),
            (Signer::ed25519(1), JwsAlgorithm::EdDsa),
        ];
        for (signer, algorithm) in &cases {
            let key = key(signer);
            let signature = signer.sign(b"message");
            assert!(
                key.verify(*algorithm, b"message", &signature),
                "{algorithm:?}"
            );
            assert!(
                !key.verify(*algorithm, b"messagf", &signature),
                "{algorithm:?}"
            );
            for (_, other) in &cases {
                if other != algorithm {
                    assert!(!key.verify(*other, b"message", &signature), "{other:?}");
                }
            }
        }
    }

    #[test]
    fn an_es256_signature_is_raw_r_and_s() {
        let signer = Signer::p256();
        let signature = signer.sign(b"m");
        assert_eq!(signature.len(), 64);
    }

    #[test]
    fn a_jwk_of_another_type_or_size_is_refused() {
        let refused = |jwk: Value| match jwk {
            Value::Object(jwk) => PublicKey::from_jwk(&jwk).is_err(),
            _ => unreachable!(),
        };
        assert!(refused(json!({ "kty": "oct", "k": "c2VjcmV0" })));
        assert!(refused(
            json!({ "kty": "EC", "crv": "P-384", "x": "", "y": "" })
        ));
        assert!(refused(
            json!({ "kty": "OKP", "crv": "Ed25519", "x": encode(&[1; 31]) })
        ));
        assert!(refused(
            json!({ "kty": "OKP", "crv": "X25519", "x": encode(&[1; 32]) })
        ));
        assert!(refused(json!({ "kty": "RSA", "n": "AQAB" })));
        assert!(refused(
            json!({ "kty": "RSA", "n": encode(&[0xff; 255]), "e": "AQAB" })
        ));
        let mut private = Signer::ed25519(2).jwk("k");
        private["d"] = Value::String(encode(&[2; 32]));
        assert!(refused(private));
    }

    #[test]
    fn a_padded_jwk_member_is_read() {
        let signer = Signer::ed25519(3);
        let mut jwk = signer.jwk("k");
        jwk["x"] = Value::String(format!("{}=", jwk["x"].as_str().unwrap()));
        let key = match jwk {
            Value::Object(jwk) => PublicKey::from_jwk(&jwk).unwrap(),
            _ => unreachable!(),
        };
        assert!(key.verify(JwsAlgorithm::EdDsa, b"m", &signer.sign(b"m")));
    }

    #[test]
    fn an_rsa_modulus_with_a_leading_zero_still_verifies() {
        let signer = Signer::rsa();
        let mut jwk = signer.jwk("k");
        let n = decode(jwk["n"].as_str().unwrap()).unwrap();
        let mut padded = vec![0];
        padded.extend(n);
        jwk["n"] = Value::String(encode(&padded));
        let key = match jwk {
            Value::Object(jwk) => PublicKey::from_jwk(&jwk).unwrap(),
            _ => unreachable!(),
        };
        assert!(key.verify(JwsAlgorithm::Rs256, b"m", &signer.sign(b"m")));
    }

    #[test]
    fn ed25519_is_read_as_eddsa_and_hmac_and_none_are_not_read() {
        assert_eq!(
            JwsAlgorithm::from_header("Ed25519"),
            Some(JwsAlgorithm::EdDsa)
        );
        assert_eq!(
            JwsAlgorithm::from_header("EdDSA"),
            Some(JwsAlgorithm::EdDsa)
        );
        for alg in ["none", "HS256", "RS384", "es256", ""] {
            assert_eq!(JwsAlgorithm::from_header(alg), None, "{alg}");
        }
        let parsed: Vec<JwsAlgorithm> =
            serde_json::from_str(r#"["RS256","ES256","EdDSA","Ed25519"]"#).unwrap();
        assert_eq!(
            parsed,
            [
                JwsAlgorithm::Rs256,
                JwsAlgorithm::Es256,
                JwsAlgorithm::EdDsa,
                JwsAlgorithm::EdDsa
            ]
        );
        assert!(serde_json::from_str::<JwsAlgorithm>(r#""HS256""#).is_err());
        assert!(serde_json::from_str::<JwsAlgorithm>(r#""none""#).is_err());
    }
}
