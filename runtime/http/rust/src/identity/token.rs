//! Session tokens, API keys and the credential a request carries
//! (`runtime/http/testdata/README.md`, sections `tokens`, `apiKeys` and
//! `credentials`).

use std::fmt;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use http::header::{AUTHORIZATION, COOKIE};
use http::HeaderMap;
use ring::rand::{SecureRandom, SystemRandom};
use serde::{Deserialize, Serialize};

/// The random bytes of a session token.
pub const TOKEN_BYTES: usize = 32;
/// The length of a session token's text: base64url without padding.
pub const TOKEN_LENGTH: usize = 43;

/// What starts an API key's text (D114).
pub const API_KEY_PREFIX: &str = "key_";
/// The length of an API key's text: the prefix, then [`TOKEN_BYTES`] random
/// bytes as [`TOKEN_LENGTH`] characters of base64url without padding.
pub const API_KEY_LENGTH: usize = API_KEY_PREFIX.len() + TOKEN_LENGTH;

/// How a session travels: the `Authorization` header or the cookie. It is
/// also the value of a login's `session` member.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Transport {
    Bearer,
    Cookie,
}

impl Transport {
    pub fn as_str(self) -> &'static str {
        match self {
            Transport::Bearer => "bearer",
            Transport::Cookie => "cookie",
        }
    }
}

impl fmt::Display for Transport {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The system had no random bytes to give.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct NoRandomness;

impl fmt::Display for NoRandomness {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("identity: the system gave no random bytes")
    }
}

impl std::error::Error for NoRandomness {}

/// `N` bytes from the system's secure random source.
pub(crate) fn random_bytes<const N: usize>() -> Result<[u8; N], NoRandomness> {
    let mut bytes = [0u8; N];
    SystemRandom::new()
        .fill(&mut bytes)
        .map_err(|_| NoRandomness)?;
    Ok(bytes)
}

/// A new session token: 32 random bytes in base64url without padding.
pub fn new_token() -> Result<String, NoRandomness> {
    random_bytes::<TOKEN_BYTES>().map(|bytes| token_from_bytes(&bytes))
}

/// The token whose random value is `bytes`, for the parity vectors and a
/// test's own tokens.
pub fn token_from_bytes(bytes: &[u8; TOKEN_BYTES]) -> String {
    URL_SAFE_NO_PAD.encode(bytes)
}

/// A new API key (D114): [`API_KEY_PREFIX`], then 32 random bytes in
/// base64url without padding.
pub fn new_api_key() -> Result<String, NoRandomness> {
    random_bytes::<TOKEN_BYTES>().map(|bytes| api_key_from_bytes(&bytes))
}

/// The API key whose random value is `bytes`, for the parity vectors and a
/// test's own keys.
pub fn api_key_from_bytes(bytes: &[u8; TOKEN_BYTES]) -> String {
    format!("{API_KEY_PREFIX}{}", token_from_bytes(bytes))
}

/// The hash a session's row keeps of its token, and an API key's row of
/// its key: the lowercase hexadecimal SHA-256 of the whole text, an API
/// key's prefix included, a `Crypto.SHA256`.
pub fn hash_token(token: &str) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, token.as_bytes());
    digest
        .as_ref()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// Whether `s` has a token's shape: 43 characters of the base64url
/// alphabet.
pub fn is_token(s: &[u8]) -> bool {
    s.len() == TOKEN_LENGTH
        && s.iter()
            .all(|c| c.is_ascii_alphanumeric() || *c == b'-' || *c == b'_')
}

/// Whether `s` has an API key's shape: [`API_KEY_PREFIX`], exactly, and a
/// token's 43 characters of the base64url alphabet.
pub fn is_api_key(s: &[u8]) -> bool {
    s.len() == API_KEY_LENGTH
        && s.strip_prefix(API_KEY_PREFIX.as_bytes())
            .is_some_and(is_token)
}

/// The session token or the API key a request carries, and how.
#[derive(Clone, PartialEq, Eq)]
pub struct Credential {
    pub token: String,
    pub transport: Transport,
    /// The token is an API key (D114), which only the `Authorization`
    /// header carries.
    pub api_key: bool,
}

// Debug leaves the token out, so a logged credential does not sign anyone
// in.
impl fmt::Debug for Credential {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Credential")
            .field("transport", &self.transport)
            .field("api_key", &self.api_key)
            .finish_non_exhaustive()
    }
}

/// What [`extract_credential`] found on a request.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CredentialOutcome {
    /// No `Authorization` header and no session cookie.
    None,
    /// A session token in the `Authorization` header or the cookie, or an
    /// API key in the `Authorization` header.
    Usable(Credential),
    /// An `Authorization` header that is not a usable bearer token or API
    /// key, or, without one, a session cookie whose value is not a session
    /// token. It is 401, with no fallback to the cookie; the transport says
    /// which was read.
    Invalid(Transport),
}

/// Reads a request's credential. `Authorization` comes first: when the
/// request has one it is the credential, and the cookie is not read. It is
/// usable when it appears once and is `Bearer`, in any case, one or more
/// spaces and either a session token or an API key ([`API_KEY_PREFIX`] and
/// as many characters); anything else is invalid. Without `Authorization`,
/// the credential is the first cookie named `cookie_name` across every
/// `Cookie` header, as Go's `Request.Cookie` finds it, usable when its
/// value is a session token: the cookie carries no API key. A usable
/// credential's `api_key` says it is an API key.
pub fn extract_credential(headers: &HeaderMap, cookie_name: &str) -> CredentialOutcome {
    let mut authorization = headers.get_all(AUTHORIZATION).iter();
    if let Some(first) = authorization.next() {
        return match (bearer_token(first.as_bytes()), authorization.next()) {
            (Some(token), None) => CredentialOutcome::Usable(Credential {
                api_key: is_api_key(token.as_bytes()),
                token,
                transport: Transport::Bearer,
            }),
            _ => CredentialOutcome::Invalid(Transport::Bearer),
        };
    }
    match find_cookie(headers, cookie_name) {
        None => CredentialOutcome::None,
        Some(value) if is_token(&value) => CredentialOutcome::Usable(Credential {
            token: String::from_utf8_lossy(&value).into_owned(),
            transport: Transport::Cookie,
            api_key: false,
        }),
        Some(_) => CredentialOutcome::Invalid(Transport::Cookie),
    }
}

/// Reads `Bearer <token>`: the scheme in any case, one or more spaces (not
/// tabs), and a session token or an API key.
fn bearer_token(value: &[u8]) -> Option<String> {
    let space = value.iter().position(|c| *c == b' ')?;
    let (scheme, rest) = (&value[..space], &value[space + 1..]);
    if !scheme.eq_ignore_ascii_case(b"Bearer") {
        return None;
    }
    let start = rest.iter().position(|c| *c != b' ').unwrap_or(rest.len());
    let token = &rest[start..];
    (is_token(token) || is_api_key(token)).then(|| String::from_utf8_lossy(token).into_owned())
}

/// The value of the first cookie named `name` across the request's `Cookie`
/// headers, read as Go's `net/http` reads cookies: each header split on
/// `;`, each part trimmed, a part without `=` a cookie with an empty value,
/// a name that is not a token skipped, and a value in double quotes
/// unquoted. A value with a byte a cookie value cannot hold is skipped,
/// and the search goes on.
pub(crate) fn find_cookie(headers: &HeaderMap, name: &str) -> Option<Vec<u8>> {
    if name.is_empty() {
        return None;
    }
    for line in headers.get_all(COOKIE) {
        for part in trim(line.as_bytes()).split(|c| *c == b';') {
            let part = trim(part);
            if part.is_empty() {
                continue;
            }
            let (found, value) = match part.iter().position(|c| *c == b'=') {
                Some(eq) => (&part[..eq], &part[eq + 1..]),
                None => (part, &[][..]),
            };
            let found = trim(found);
            if found.is_empty() || !found.iter().all(|c| is_token_byte(*c)) {
                continue;
            }
            if found != name.as_bytes() {
                continue;
            }
            if let Some(value) = cookie_value(value) {
                return Some(value.to_vec());
            }
        }
    }
    None
}

/// A cookie's value, unquoted, or `None` when it holds a byte a cookie
/// value cannot.
fn cookie_value(raw: &[u8]) -> Option<&[u8]> {
    let value = match raw {
        [b'"', inner @ .., b'"'] => inner,
        _ => raw,
    };
    value
        .iter()
        .all(|b| (0x20..0x7f).contains(b) && !matches!(b, b'"' | b';' | b'\\'))
        .then_some(value)
}

/// `s` without leading and trailing ASCII spaces, tabs, line feeds and
/// carriage returns, as Go's `textproto.TrimString` trims.
fn trim(s: &[u8]) -> &[u8] {
    let is_space = |c: &u8| matches!(c, b' ' | b'\t' | b'\n' | b'\r');
    let start = s.iter().position(|c| !is_space(c)).unwrap_or(s.len());
    let end = s
        .iter()
        .rposition(|c| !is_space(c))
        .map_or(start, |i| i + 1);
    &s[start..end]
}

/// Whether `c` may appear in an RFC 7230 token.
fn is_token_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&c)
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::HeaderValue;

    #[test]
    fn a_new_token_has_a_tokens_shape() {
        let token = new_token().unwrap();
        assert!(is_token(token.as_bytes()), "{token}");
        assert_ne!(token, new_token().unwrap());
        assert_eq!(hash_token(&token).len(), 64);
    }

    /// An API key is its prefix and 43 characters of base64url, 47 in all,
    /// two differ, and its hash is the whole text's.
    #[test]
    fn a_new_api_key_has_an_api_keys_shape() {
        let a = new_api_key().unwrap();
        let b = new_api_key().unwrap();
        assert_eq!((API_KEY_PREFIX, API_KEY_LENGTH), ("key_", 47));
        assert!(is_api_key(a.as_bytes()), "{a}");
        assert!(
            a != b && a.len() == API_KEY_LENGTH && a.starts_with(API_KEY_PREFIX),
            "keys {a:?} and {b:?}"
        );
        assert!(!a.contains(['+', '/', '=']), "{a}");
        let hash = hash_token(&a);
        assert_eq!(hash.len(), 64);
        assert_ne!(
            hash,
            hash_token(a.strip_prefix(API_KEY_PREFIX).unwrap()),
            "the key's hash is not its whole text's"
        );
        assert_eq!(
            api_key_from_bytes(&[0; TOKEN_BYTES]),
            format!("key_{}", "A".repeat(TOKEN_LENGTH))
        );
        // A session token is no key, whatever it starts with, and a key no
        // session token.
        assert!(!is_api_key(&a.as_bytes()[API_KEY_PREFIX.len()..]));
        assert!(!is_api_key(format!("key_{}", "A".repeat(39)).as_bytes()));
        assert!(!is_api_key(format!("KEY_{}", &a[4..]).as_bytes()));
        assert!(!is_token(a.as_bytes()));
    }

    #[test]
    fn a_quoted_cookie_is_unquoted_and_a_bad_one_skipped() {
        let token = token_from_bytes(&[7; TOKEN_BYTES]);
        let mut headers = HeaderMap::new();
        headers.append(
            COOKIE,
            HeaderValue::from_str(&format!("sid=\"{token}\"")).unwrap(),
        );
        assert_eq!(
            extract_credential(&headers, "sid"),
            CredentialOutcome::Usable(Credential {
                token: token.clone(),
                transport: Transport::Cookie,
                api_key: false,
            })
        );
        let mut headers = HeaderMap::new();
        headers.append(
            COOKIE,
            HeaderValue::from_str(&format!("sid=a\\b; sid={token}")).unwrap(),
        );
        assert!(matches!(
            extract_credential(&headers, "sid"),
            CredentialOutcome::Usable(_)
        ));
        let mut headers = HeaderMap::new();
        headers.append(COOKIE, HeaderValue::from_static("sid"));
        assert_eq!(
            extract_credential(&headers, "sid"),
            CredentialOutcome::Invalid(Transport::Cookie)
        );
    }
}
