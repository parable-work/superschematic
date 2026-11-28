//! A connection string's `sslmode`, read as the HTTP runtime reads it
//! (`runtime/http/rust/src/postgres.rs`): tokio-postgres reads `disable`,
//! `prefer` (its default) and `require` itself, and refuses `verify-ca` and
//! `verify-full`, so those are read as `require` with the TLS verifying
//! what they ask.
//!
//! - `disable`: no TLS.
//! - `prefer` (the default): TLS when the server offers it, the
//!   certificate not checked.
//! - `require`: TLS, the certificate not checked.
//! - `verify-ca`: TLS, the certificate signed by a root the system trusts.
//! - `verify-full`: that, and the certificate for the host connected to.
//!
//! As libpq reads them, `prefer` and `require` check nothing of the
//! certificate. A server that offers TLS and fails its handshake fails the
//! connection under `prefer` too, where libpq would try again without TLS.

/// How much of the server's certificate a connection string's TLS checks.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Verify {
    /// Nothing: `disable`, `prefer` and `require`, and a string that names
    /// no `sslmode`.
    Nothing,
    /// That the system's roots sign it: `verify-ca`.
    Chain,
    /// That the system's roots sign it for the host: `verify-full`.
    Host,
}

/// The connection string with a `verify-ca` or `verify-full` `sslmode`
/// read as `require`, which tokio-postgres reads, and what the TLS
/// verifies. A string tokio-postgres will refuse is left as it is, for it
/// to refuse.
pub fn read_ssl_mode(url: &str) -> (String, Verify) {
    let rewritten = if url.starts_with("postgres://") || url.starts_with("postgresql://") {
        rewrite_url_ssl_mode(url)
    } else {
        rewrite_pairs_ssl_mode(url)
    };
    let Some((url, mode)) = rewritten else {
        return (url.to_string(), Verify::Nothing);
    };
    let verify = match mode.as_deref() {
        Some("verify-full") => Verify::Host,
        Some("verify-ca") => Verify::Chain,
        _ => Verify::Nothing,
    };
    (url, verify)
}

fn is_verify(mode: &str) -> bool {
    matches!(mode, "verify-ca" | "verify-full")
}

/// A URL's query with its `sslmode` rewritten, and the last `sslmode`,
/// which tokio-postgres reads.
fn rewrite_url_ssl_mode(url: &str) -> Option<(String, Option<String>)> {
    let Some((base, query)) = url.split_once('?') else {
        return Some((url.to_string(), None));
    };
    let mut mode = None;
    let mut pairs = Vec::new();
    for pair in query.split('&') {
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        if percent_decode(key)? == "sslmode" {
            let value = percent_decode(value)?;
            if is_verify(&value) {
                pairs.push("sslmode=require".to_string());
                mode = Some(value);
                continue;
            }
            mode = Some(value);
        }
        pairs.push(pair.to_string());
    }
    Some((format!("{base}?{}", pairs.join("&")), mode))
}

fn percent_decode(text: &str) -> Option<String> {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            let hex = text.get(i + 1..i + 3)?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

/// A string of `key=value` pairs with its `sslmode` rewritten, read as
/// tokio-postgres reads one: whitespace between pairs and around `=`, and
/// a value quoted in `'` or bare, `\` escaping the character after it.
fn rewrite_pairs_ssl_mode(text: &str) -> Option<(String, Option<String>)> {
    let chars: Vec<char> = text.chars().collect();
    let mut out = String::with_capacity(text.len());
    let mut mode = None;
    let mut i = 0;
    loop {
        let start = i;
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        out.extend(&chars[start..i]);
        if i == chars.len() {
            break;
        }
        let pair_start = i;
        let key_start = i;
        while i < chars.len() && chars[i] != '=' && !chars[i].is_whitespace() {
            i += 1;
        }
        let key: String = chars[key_start..i].iter().collect();
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        if chars.get(i) != Some(&'=') {
            return None;
        }
        i += 1;
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        let mut value = String::new();
        if chars.get(i) == Some(&'\'') {
            i += 1;
            loop {
                match chars.get(i) {
                    None => return None,
                    Some('\'') => {
                        i += 1;
                        break;
                    }
                    Some('\\') => {
                        value.push(*chars.get(i + 1)?);
                        i += 2;
                    }
                    Some(c) => {
                        value.push(*c);
                        i += 1;
                    }
                }
            }
        } else {
            while let Some(&c) = chars.get(i) {
                if c.is_whitespace() {
                    break;
                }
                if c == '\\' {
                    value.push(*chars.get(i + 1)?);
                    i += 2;
                } else {
                    value.push(c);
                    i += 1;
                }
            }
        }
        if key == "sslmode" && is_verify(&value) {
            out.push_str("sslmode=require");
        } else {
            out.extend(&chars[pair_start..i]);
        }
        if key == "sslmode" {
            mode = Some(value);
        }
    }
    Some((out, mode))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_verify_ssl_mode_is_read_as_require_and_verified_by_the_tls() {
        let cases = [
            ("postgres://u:p@h/d", "postgres://u:p@h/d", Verify::Nothing),
            (
                "postgres://u:p@h/d?sslmode=disable",
                "postgres://u:p@h/d?sslmode=disable",
                Verify::Nothing,
            ),
            (
                "postgresql://h/d?application_name=a&sslmode=verify-full",
                "postgresql://h/d?application_name=a&sslmode=require",
                Verify::Host,
            ),
            (
                "postgres://h/d?ssl%6dode=verify-ca&connect_timeout=5",
                "postgres://h/d?sslmode=require&connect_timeout=5",
                Verify::Chain,
            ),
            (
                "postgres://h/d?sslmode=verify-full&sslmode=require",
                "postgres://h/d?sslmode=require&sslmode=require",
                Verify::Nothing,
            ),
            (
                "host=h  dbname=d sslmode = verify-full",
                "host=h  dbname=d sslmode=require",
                Verify::Host,
            ),
            (
                "host=h password='a b\\' c' sslmode='verify-ca'",
                "host=h password='a b\\' c' sslmode=require",
                Verify::Chain,
            ),
            (
                "host=h sslmode=require",
                "host=h sslmode=require",
                Verify::Nothing,
            ),
            // tokio-postgres refuses these; they are left to it.
            (
                "host=h password='open",
                "host=h password='open",
                Verify::Nothing,
            ),
            (
                "postgres://h/d?sslmode=%zz",
                "postgres://h/d?sslmode=%zz",
                Verify::Nothing,
            ),
        ];
        for (url, want, verify) in cases {
            assert_eq!(read_ssl_mode(url), (want.to_string(), verify), "{url}");
            if !want.contains("'open") && !want.contains("%zz") {
                assert!(
                    want.parse::<tokio_postgres::Config>().is_ok(),
                    "tokio-postgres reads {want}"
                );
            }
        }
    }

    #[test]
    fn disable_prefer_and_require_are_left_to_tokio_postgres_and_verify_nothing() {
        for mode in ["disable", "prefer", "require"] {
            let url = format!("postgres://u@h/d?sslmode={mode}");
            assert_eq!(read_ssl_mode(&url), (url.clone(), Verify::Nothing));
            let pairs = format!("host=h sslmode={mode}");
            assert_eq!(read_ssl_mode(&pairs), (pairs.clone(), Verify::Nothing));
        }
    }
}
