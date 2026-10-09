//! The session cookie a cookie login sets and logout clears
//! (`runtime/http/testdata/README.md`, section `cookies`).

use std::time::Duration;

use super::config::Config;

impl Config {
    /// The `Set-Cookie` value a cookie login answers: the config's name, the
    /// token, `Path=/`, the `Domain` the config names, `Max-Age` the whole
    /// seconds left in the session, `HttpOnly`, `Secure` unless the config
    /// turns it off, and the config's `SameSite`, as Go writes it:
    ///
    /// ```text
    /// __Host-session=<token>; Path=/; Max-Age=1209600; HttpOnly; Secure; SameSite=Lax
    /// ```
    pub fn session_cookie(&self, token: &str, max_age: Duration) -> String {
        self.cookie(token, max_age.as_secs())
    }

    /// The `Set-Cookie` value logout answers: the session cookie with no
    /// value and `Max-Age=0`, which a browser takes to delete it.
    pub fn clear_cookie(&self) -> String {
        self.cookie("", 0)
    }

    fn cookie(&self, value: &str, max_age: u64) -> String {
        let cookie = &self.cookie;
        let mut out = format!("{}={value}; Path=/", cookie.name);
        if !cookie.domain.is_empty() {
            out.push_str("; Domain=");
            out.push_str(&cookie.domain);
        }
        out.push_str(&format!("; Max-Age={max_age}; HttpOnly"));
        if cookie.secure {
            out.push_str("; Secure");
        }
        out.push_str("; SameSite=");
        out.push_str(&cookie.same_site);
        out
    }
}
