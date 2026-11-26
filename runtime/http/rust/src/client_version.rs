//! The oldest version of each client an API serves (D143).
//!
//! A generated SDK whose caller names its client sends
//! `X-Client-Version: <name>/<version>` on every call. An API whose config
//! says `clientVersions: true` reads `MIN_CLIENT_VERSIONS`, a list such as
//! `cli=0.5.0,desktop=2.1.0`, and answers a request whose header names a
//! listed client at a lower version with 426 and the problem
//! `client_too_old`, whose details hold the client, its version and the
//! minimum. Versions compare as semantic versions (semver.org 2.0.0). A
//! request with no header, a client the list does not name, or a version
//! that is no semantic version passes: a browser sends none.
//!
//! The Go and TypeScript runtimes read the setting and decide each request
//! alike (`runtime/http/testdata/client_version_parity.json`).

use crate::{error_response, ApiError};
use axum::extract::Request;
use axum::http::{HeaderMap, StatusCode};
use axum::middleware::Next;
use axum::response::Response;
use serde_json::json;
use std::cmp::Ordering;
use std::collections::BTreeMap;
use std::fmt;
use std::sync::Arc;

/// The request header a client names itself in.
pub const HEADER: &str = "x-client-version";
/// The environment variable that lists the minimums.
pub const SETTING: &str = "MIN_CLIENT_VERSIONS";
/// The problem code of a client that is too old.
pub const CODE: &str = "client_too_old";

/// A semantic version, parsed: its three numbers as their digits and its
/// pre-release identifiers, none for a release.
#[derive(Debug, Clone, PartialEq, Eq)]
struct Version {
    core: [String; 3],
    pre: Vec<String>,
}

fn is_number(text: &str) -> bool {
    !text.is_empty() && text.bytes().all(|byte| byte.is_ascii_digit())
}

/// A number of a semantic version: digits, with no leading zero.
fn is_core_number(text: &str) -> bool {
    is_number(text) && (text == "0" || !text.starts_with('0'))
}

fn is_identifier(text: &str) -> bool {
    !text.is_empty()
        && text
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-')
}

impl Version {
    /// Reads a semantic version as semver.org 2.0.0 writes one: three
    /// numbers without leading zeros, an optional pre-release after `-`
    /// and optional build metadata after `+`.
    fn parse(text: &str) -> Option<Self> {
        let (rest, build) = match text.split_once('+') {
            Some((rest, build)) => (rest, Some(build)),
            None => (text, None),
        };
        if let Some(build) = build {
            if !build.split('.').all(is_identifier) {
                return None;
            }
        }
        let (core, pre) = match rest.split_once('-') {
            Some((core, pre)) => (core, Some(pre)),
            None => (rest, None),
        };
        let mut numbers = core.split('.');
        let core = [numbers.next()?, numbers.next()?, numbers.next()?];
        if numbers.next().is_some() || !core.iter().all(|number| is_core_number(number)) {
            return None;
        }
        let mut identifiers = Vec::new();
        if let Some(pre) = pre {
            for identifier in pre.split('.') {
                // A numeric identifier has no leading zero.
                if !is_identifier(identifier)
                    || (is_number(identifier) && !is_core_number(identifier))
                {
                    return None;
                }
                identifiers.push(identifier.to_owned());
            }
        }
        Some(Self {
            core: core.map(str::to_owned),
            pre: identifiers,
        })
    }

    /// Semantic version precedence. Build metadata takes no part.
    fn compare(&self, other: &Self) -> Ordering {
        for (a, b) in self.core.iter().zip(&other.core) {
            let order = compare_numbers(a, b);
            if order != Ordering::Equal {
                return order;
            }
        }
        match (self.pre.is_empty(), other.pre.is_empty()) {
            (true, true) => return Ordering::Equal,
            (true, false) => return Ordering::Greater,
            (false, true) => return Ordering::Less,
            (false, false) => {}
        }
        for (a, b) in self.pre.iter().zip(&other.pre) {
            let order = match (is_number(a), is_number(b)) {
                (true, true) => compare_numbers(a, b),
                (true, false) => Ordering::Less,
                (false, true) => Ordering::Greater,
                (false, false) => a.as_bytes().cmp(b.as_bytes()),
            };
            if order != Ordering::Equal {
                return order;
            }
        }
        // Every shared identifier is equal: the longer list is the newer.
        self.pre.len().cmp(&other.pre.len())
    }
}

/// Compares two numbers written as digits without leading zeros, of any
/// length.
fn compare_numbers(a: &str, b: &str) -> Ordering {
    a.len().cmp(&b.len()).then_with(|| a.cmp(b))
}

/// Orders two versions by semantic version precedence (semver.org 2.0.0);
/// `None` when either is no semantic version.
pub fn compare(a: &str, b: &str) -> Option<Ordering> {
    Some(Version::parse(a)?.compare(&Version::parse(b)?))
}

/// A client's name: a letter or a digit, then up to 63 more of letters,
/// digits, `.`, `_` and `-`.
fn is_client_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    bytes
        .next()
        .is_some_and(|first| first.is_ascii_alphanumeric())
        && name.len() <= 64
        && bytes.all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct Minimum {
    text: String,
    version: Version,
}

/// The oldest version of each client an API serves. The default lists
/// none, and turns no request away.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Minimums {
    by_client: BTreeMap<String, Minimum>,
}

/// A `MIN_CLIENT_VERSIONS` a server does not start with.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SettingError(String);

impl fmt::Display for SettingError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for SettingError {}

/// What a `client_too_old` problem carries in details: the client, the
/// version it sent and the oldest version the API serves.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TooOld {
    pub client: String,
    pub version: String,
    pub minimum: String,
}

impl TooOld {
    /// The 426 the client is answered with.
    pub fn into_error(self) -> ApiError {
        ApiError::new(
            StatusCode::UPGRADE_REQUIRED,
            CODE,
            format!(
                "{client} {version} is older than {minimum}, the oldest version of {client} this API serves. Upgrade {client}.",
                client = self.client,
                version = self.version,
                minimum = self.minimum,
            ),
        )
        .with_details(json!({
            "client": self.client,
            "version": self.version,
            "minimum": self.minimum,
        }))
    }
}

impl Minimums {
    /// Reads `MIN_CLIENT_VERSIONS`: entries `<client>=<version>` separated
    /// by commas, with space allowed around each entry, name and version.
    /// An empty setting lists none. An entry without one `=`, a name that
    /// is not letters, digits, `.`, `_` and `-`, a minimum that is no
    /// semantic version and a client listed twice are errors: a server
    /// does not start on a minimum it cannot enforce.
    pub fn parse(setting: &str) -> Result<Self, SettingError> {
        let mut by_client = BTreeMap::new();
        for entry in setting.split(',') {
            let entry = entry.trim();
            if entry.is_empty() {
                continue;
            }
            let mut parts = entry.split('=');
            let (Some(name), Some(text), None) = (parts.next(), parts.next(), parts.next()) else {
                return Err(SettingError(format!(
                    "{SETTING}: {entry:?} is not <client>=<version>, as in cli=0.5.0,desktop=2.1.0"
                )));
            };
            let (name, text) = (name.trim(), text.trim());
            if !is_client_name(name) {
                return Err(SettingError(format!(
                    "{SETTING}: {name:?} is not a client's name: a letter or a digit, then letters, digits, '.', '_' and '-', 64 characters at most"
                )));
            }
            let Some(version) = Version::parse(text) else {
                return Err(SettingError(format!(
                    "{SETTING}: the minimum of {name}, {text:?}, is not a semantic version such as 0.5.0"
                )));
            };
            let minimum = Minimum {
                text: text.to_owned(),
                version,
            };
            if by_client.insert(name.to_owned(), minimum).is_some() {
                return Err(SettingError(format!("{SETTING} lists {name} twice")));
            }
        }
        Ok(Self { by_client })
    }

    /// Reads the setting from the environment variable `MIN_CLIENT_VERSIONS`,
    /// which lists none when it is unset.
    pub fn from_env() -> Result<Self, SettingError> {
        match std::env::var(SETTING) {
            Ok(setting) => Self::parse(&setting),
            Err(std::env::VarError::NotPresent) => Ok(Self::default()),
            Err(std::env::VarError::NotUnicode(_)) => {
                Err(SettingError(format!("{SETTING} is not UTF-8")))
            }
        }
    }

    /// Whether the list names no client.
    pub fn is_empty(&self) -> bool {
        self.by_client.is_empty()
    }

    /// Each client's minimum as it was written, by client.
    pub fn listed(&self) -> BTreeMap<String, String> {
        self.by_client
            .iter()
            .map(|(name, minimum)| (name.clone(), minimum.text.clone()))
            .collect()
    }

    /// Decides a request by its `X-Client-Version` header,
    /// `<name>/<version>`: the refusal of a listed client at a version
    /// under its minimum, and `None` for every other request.
    pub fn check(&self, header: Option<&str>) -> Option<TooOld> {
        let header = header?.trim_matches([' ', '\t']);
        let (name, text) = header.split_once('/')?;
        let minimum = self.by_client.get(name)?;
        let version = Version::parse(text)?;
        (version.compare(&minimum.version) == Ordering::Less).then(|| TooOld {
            client: name.to_owned(),
            version: text.to_owned(),
            minimum: minimum.text.clone(),
        })
    }

    /// [`Minimums::check`] over a request's headers.
    pub fn check_headers(&self, headers: &HeaderMap) -> Option<TooOld> {
        if self.by_client.is_empty() {
            return None;
        }
        self.check(headers.get(HEADER).and_then(|value| value.to_str().ok()))
    }
}

/// axum middleware the generated router of an API with `clientVersions`
/// runs around every route, inside [`crate::request_ids`]: a client under
/// its minimum is answered 426 with the problem `client_too_old`, and every
/// other request runs. Layer it with
/// `axum::middleware::from_fn_with_state(Arc::new(minimums), require_client_version)`.
pub async fn require_client_version(
    axum::extract::State(minimums): axum::extract::State<Arc<Minimums>>,
    request: Request,
    next: Next,
) -> Response {
    if let Some(refusal) = minimums.check_headers(request.headers()) {
        return error_response(refusal.into_error());
    }
    next.run(request).await
}
