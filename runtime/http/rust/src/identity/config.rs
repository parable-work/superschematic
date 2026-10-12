//! The identity config: the JSON the Go, TypeScript and Rust runtimes read
//! the same way (`runtime/http/testdata/README.md`, section `config`).

use std::fmt;
use std::time::Duration;

use serde::Serialize;
use serde_json::Value;

use super::cross_origin::origin_parts;
use super::json;
use super::password::Argon2Params;

/// A session's lifetime without `sessionTtlSeconds`: 14 days.
pub const DEFAULT_SESSION_TTL_SECONDS: i64 = 14 * 24 * 60 * 60;
/// How often a request writes `lastSeenAt` at most, without
/// `touchIntervalSeconds`.
pub const DEFAULT_TOUCH_INTERVAL_SECONDS: i64 = 60;
/// The cookie's `SameSite` without `cookie.sameSite`.
pub const DEFAULT_SAME_SITE: &str = "Lax";

/// The session cookie's name by default: a Secure cookie with no domain.
pub const HOST_COOKIE_NAME: &str = "__Host-session";
/// The session cookie's name when the cookie names a domain.
pub const SECURE_COOKIE_NAME: &str = "__Secure-session";
/// The session cookie's name when it is not Secure, as on the local target
/// over plain HTTP.
pub const PLAIN_COOKIE_NAME: &str = "session";

/// The most memory a config may ask of every login: 4 GiB.
pub(crate) const MAX_ARGON2_MEMORY_KIB: u32 = 1 << 22;

/// The config the identity runtime runs with, every member resolved: the
/// defaults filled in and the cookie's name chosen. It serializes as the
/// `want` of the `config` vectors. [`Config::parse`] reads one from the
/// shared JSON; `Config::default()` is the config of `{}`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Config {
    /// How long a session lasts after login.
    pub session_ttl_seconds: i64,
    /// How long a session may go unseen; 0 is no limit.
    pub idle_timeout_seconds: i64,
    /// How often a request writes a session's `lastSeenAt`, at most; 0
    /// writes it on every request.
    pub touch_interval_seconds: i64,
    pub cookie: CookieConfig,
    /// The origins (`scheme://host[:port]`) a cookie request may come from
    /// across origins, and the only ones the CORS layer answers.
    pub trusted_origins: Vec<String>,
    pub password: PasswordConfig,
}

/// The session cookie.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CookieConfig {
    pub name: String,
    /// The `Domain` attribute, so APIs on sibling hosts share the session;
    /// empty for none.
    pub domain: String,
    pub secure: bool,
    /// `Lax`, `Strict` or `None`.
    pub same_site: String,
}

/// How passwords are hashed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct PasswordConfig {
    pub argon2: Argon2Params,
}

/// A config the runtime refuses, with every reason.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigError(pub String);

impl fmt::Display for ConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "identity: config: {}", self.0)
    }
}

impl std::error::Error for ConfigError {}

impl Default for Config {
    fn default() -> Self {
        Config {
            session_ttl_seconds: DEFAULT_SESSION_TTL_SECONDS,
            idle_timeout_seconds: 0,
            touch_interval_seconds: DEFAULT_TOUCH_INTERVAL_SECONDS,
            cookie: CookieConfig {
                name: HOST_COOKIE_NAME.to_owned(),
                domain: String::new(),
                secure: true,
                same_site: DEFAULT_SAME_SITE.to_owned(),
            },
            trusted_origins: Vec::new(),
            password: PasswordConfig {
                argon2: Argon2Params::DEFAULT,
            },
        }
    }
}

impl Config {
    /// Reads a config from its JSON, as the Go runtime's `ParseConfig` does:
    /// every member optional, a null member as an absent one, a member's
    /// name matched exactly, any other member refused at every level, a
    /// number only an integer literal, and then every rule
    /// [`Config::validate`] checks.
    pub fn parse(json: &[u8]) -> Result<Config, ConfigError> {
        let value: Value =
            serde_json::from_slice(json).map_err(|err| ConfigError(err.to_string()))?;
        Config::from_value(&value)
    }

    /// [`Config::parse`] of a JSON value already read. The config is an
    /// object; `{}` is every default, and `null` is refused.
    pub fn from_value(value: &Value) -> Result<Config, ConfigError> {
        let config = read(value).map_err(ConfigError)?;
        config.validate()?;
        Ok(config)
    }

    /// Refuses a config no deployment means: a session that never lasts, an
    /// idle timeout the touch interval outlasts, a cookie name or `SameSite`
    /// a browser would refuse, an origin that is not `scheme://host`, and an
    /// argon2 cost the algorithm does not take. Every problem is reported.
    pub fn validate(&self) -> Result<(), ConfigError> {
        let mut problems: Vec<String> = Vec::new();
        if self.session_ttl_seconds <= 0 {
            problems.push(format!(
                "sessionTtlSeconds must be positive, not {}",
                self.session_ttl_seconds
            ));
        }
        if self.idle_timeout_seconds < 0 {
            problems.push(format!(
                "idleTimeoutSeconds must not be negative, not {}",
                self.idle_timeout_seconds
            ));
        }
        if self.touch_interval_seconds < 0 {
            problems.push(format!(
                "touchIntervalSeconds must not be negative, not {}",
                self.touch_interval_seconds
            ));
        }
        if self.idle_timeout_seconds > 0 && self.touch_interval_seconds >= self.idle_timeout_seconds
        {
            problems.push(format!(
                "touchIntervalSeconds ({}) must be less than idleTimeoutSeconds ({}), or a session idles out between two writes of lastSeenAt",
                self.touch_interval_seconds, self.idle_timeout_seconds
            ));
        }
        self.cookie.problems(&mut problems);
        for (i, origin) in self.trusted_origins.iter().enumerate() {
            if let Err(err) = check_origin(origin) {
                problems.push(format!("trustedOrigins[{i}]: {err}"));
            }
        }
        if let Err(err) = self.password.argon2.check() {
            problems.push(format!("password.argon2.{err}"));
        }
        if problems.is_empty() {
            Ok(())
        } else {
            Err(ConfigError(problems.join("; ")))
        }
    }

    /// How long a session lasts after login.
    pub fn session_ttl(&self) -> Duration {
        seconds(self.session_ttl_seconds)
    }

    /// How long a session may go unseen, or zero for no limit.
    pub fn idle_timeout(&self) -> Duration {
        seconds(self.idle_timeout_seconds)
    }

    /// How often a request writes a session's `lastSeenAt`, at most.
    pub fn touch_interval(&self) -> Duration {
        seconds(self.touch_interval_seconds)
    }

    /// The session cookie's name.
    pub fn cookie_name(&self) -> &str {
        &self.cookie.name
    }

    /// The cost new password hashes are written with.
    pub fn argon2_params(&self) -> Argon2Params {
        self.password.argon2
    }
}

fn seconds(n: i64) -> Duration {
    Duration::from_secs(u64::try_from(n).unwrap_or(0))
}

impl CookieConfig {
    fn problems(&self, problems: &mut Vec<String>) {
        match self.same_site.as_str() {
            "Lax" | "Strict" => {}
            "None" => {
                if !self.secure {
                    problems.push(
                        "cookie.sameSite None needs cookie.secure, which browsers require of it"
                            .to_owned(),
                    );
                }
            }
            other => problems.push(format!(
                "cookie.sameSite must be Lax, Strict or None, not {other:?}"
            )),
        }
        if !self.domain.is_empty() && !is_cookie_domain(&self.domain) {
            problems.push(format!(
                "cookie.domain {:?} is not a domain name (letters, digits and hyphens in dot-separated labels, with no leading dot, port or scheme)",
                self.domain
            ));
        }
        let name = &self.name;
        if !is_cookie_name(name) {
            problems.push(format!("cookie.name {name:?} is not a cookie name"));
        } else if name.starts_with("__Host-") && (!self.secure || !self.domain.is_empty()) {
            problems.push(format!(
                "cookie.name {name:?} has the __Host- prefix, which needs cookie.secure and no cookie.domain"
            ));
        } else if name.starts_with("__Secure-") && !self.secure {
            problems.push(format!(
                "cookie.name {name:?} has the __Secure- prefix, which needs cookie.secure"
            ));
        }
    }
}

/// The config's members, read with Go's rules and the defaults filled in.
fn read(value: &Value) -> Result<Config, String> {
    let mut config = Config::default();
    let object = json::object(value, "the config")?;
    let [ttl, idle, touch, cookie, origins, password] = take(
        object,
        "the config",
        [
            "sessionTtlSeconds",
            "idleTimeoutSeconds",
            "touchIntervalSeconds",
            "cookie",
            "trustedOrigins",
            "password",
        ],
    )?;
    if let Some(v) = ttl {
        config.session_ttl_seconds = json::int64(v, "sessionTtlSeconds")?;
    }
    if let Some(v) = idle {
        config.idle_timeout_seconds = json::int64(v, "idleTimeoutSeconds")?;
    }
    if let Some(v) = touch {
        config.touch_interval_seconds = json::int64(v, "touchIntervalSeconds")?;
    }
    let mut name = String::new();
    if let Some(v) = cookie {
        let cookie = json::object(v, "cookie")?;
        let [n, domain, secure, same_site] =
            take(cookie, "cookie", ["name", "domain", "secure", "sameSite"])?;
        if let Some(v) = n {
            name = json::string(v, "cookie.name")?;
        }
        if let Some(v) = domain {
            config.cookie.domain = json::string(v, "cookie.domain")?;
        }
        if let Some(v) = secure {
            config.cookie.secure = json::boolean(v, "cookie.secure")?;
        }
        if let Some(v) = same_site {
            let same_site = json::string(v, "cookie.sameSite")?;
            if !same_site.is_empty() {
                config.cookie.same_site = same_site;
            }
        }
    }
    config.cookie.name = if !name.is_empty() {
        name
    } else if !config.cookie.secure {
        PLAIN_COOKIE_NAME.to_owned()
    } else if !config.cookie.domain.is_empty() {
        SECURE_COOKIE_NAME.to_owned()
    } else {
        HOST_COOKIE_NAME.to_owned()
    };
    if let Some(v) = origins {
        config.trusted_origins = json::strings(v, "trustedOrigins")?;
    }
    if let Some(v) = password {
        let password = json::object(v, "password")?;
        let [argon2] = take(password, "password", ["argon2"])?;
        if let Some(v) = argon2 {
            let argon2 = json::object(v, "password.argon2")?;
            let [memory, iterations, parallelism] = take(
                argon2,
                "password.argon2",
                ["memoryKiB", "iterations", "parallelism"],
            )?;
            let params = &mut config.password.argon2;
            if let Some(v) = memory {
                params.memory_kib = json::uint32(v, "password.argon2.memoryKiB")?;
            }
            if let Some(v) = iterations {
                params.iterations = json::uint32(v, "password.argon2.iterations")?;
            }
            if let Some(v) = parallelism {
                params.parallelism = json::uint32(v, "password.argon2.parallelism")?;
            }
        }
    }
    Ok(config)
}

/// The members `names` of `object`, the object at `path`.
fn take<'a, const N: usize>(
    object: &'a serde_json::Map<String, Value>,
    path: &str,
    names: [&str; N],
) -> Result<[Option<&'a Value>; N], String> {
    let values = json::members(object, &names).map_err(|err| format!("{path}: {err}"))?;
    let mut out = [None; N];
    out.copy_from_slice(&values);
    Ok(out)
}

/// Refuses an origin that is not `scheme://host[:port]`, the form a
/// browser's `Origin` header has and the cross-origin check compares with:
/// no path (a trailing slash included), query, fragment or user.
fn check_origin(origin: &str) -> Result<(), String> {
    let parts = origin_parts(origin).ok_or_else(|| format!("{origin:?} is not a URL"))?;
    if parts.scheme.is_empty() || parts.host.is_empty() || parts.opaque {
        return Err(format!("{origin:?} is not scheme://host[:port]"));
    }
    if parts.user {
        return Err(format!("{origin:?} has a user, which an origin never does"));
    }
    if parts.path || origin.contains(['?', '#']) {
        return Err(format!(
            "{origin:?} has a path, query or fragment, which an origin never does"
        ));
    }
    Ok(())
}

/// Whether `name` is an RFC 6265 cookie name: a token of visible ASCII
/// without separators.
fn is_cookie_name(name: &str) -> bool {
    !name.is_empty()
        && name
            .bytes()
            .all(|c| c > b' ' && c < 0x7f && !br#"()<>@,;:\"/[]?={}"#.contains(&c))
}

/// Whether `domain` is a name a `Domain` attribute takes: dot-separated
/// labels of letters, digits and hyphens, none empty, starting or ending
/// with a hyphen, or longer than 63 bytes, and at most 253 bytes in all.
fn is_cookie_domain(domain: &str) -> bool {
    domain.len() <= 253
        && domain.split('.').all(|label| {
            !label.is_empty()
                && label.len() <= 63
                && !label.starts_with('-')
                && !label.ends_with('-')
                && label
                    .bytes()
                    .all(|c| c.is_ascii_alphanumeric() || c == b'-')
        })
}
