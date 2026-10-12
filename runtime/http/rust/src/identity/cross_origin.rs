//! The cross-origin check a cookie request and a cookie login pass, Go's
//! `net/http.CrossOriginProtection` with the config's trusted origins, and
//! the credentialed CORS layer for those origins
//! (`runtime/http/testdata/README.md`, section `crossOrigin`).

use std::fmt;

use http::header::{HOST, ORIGIN};
use http::request::Parts;
use http::{HeaderMap, Method};

/// Why the cross-origin check refused a request, in the standard library's
/// words.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CrossOriginRefusal {
    /// `Sec-Fetch-Site` is neither `same-origin` nor `none`, and the
    /// `Origin` is not trusted.
    CrossOrigin,
    /// No `Sec-Fetch-Site`, and an `Origin` that is neither the request's
    /// host nor trusted.
    OldBrowser,
}

impl fmt::Display for CrossOriginRefusal {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            CrossOriginRefusal::CrossOrigin => "cross-origin request detected from Sec-Fetch-Site header",
            CrossOriginRefusal::OldBrowser => {
                "cross-origin request detected, and/or browser is out of date: Sec-Fetch-Site is missing, and Origin does not match Host"
            }
        })
    }
}

impl std::error::Error for CrossOriginRefusal {}

/// The cross-origin check on a request's head, with `trusted` the config's
/// trusted origins, as Go's `CrossOriginProtection.Check` makes it:
///
/// 1. `GET`, `HEAD` and `OPTIONS` pass.
/// 2. With a `Sec-Fetch-Site` header, `same-origin` and `none` pass; any
///    other value passes only when `Origin` is a trusted origin, compared
///    exactly.
/// 3. Without one, a request with no `Origin` passes; one whose `Origin`'s
///    host (with its port, if any) is `host`, whatever its scheme, passes;
///    a trusted `Origin` passes; anything else, `null` included, is
///    refused.
pub fn check_cross_origin(
    method: &Method,
    host: &[u8],
    headers: &HeaderMap,
    trusted: &[String],
) -> Result<(), CrossOriginRefusal> {
    if matches!(*method, Method::GET | Method::HEAD | Method::OPTIONS) {
        return Ok(());
    }
    let origin = first(headers, ORIGIN.as_str());
    let is_trusted = || trusted.iter().any(|t| t.as_bytes() == origin);
    match first(headers, "sec-fetch-site") {
        b"" => {}
        b"same-origin" | b"none" => return Ok(()),
        _ if is_trusted() => return Ok(()),
        _ => return Err(CrossOriginRefusal::CrossOrigin),
    }
    if origin.is_empty() {
        return Ok(());
    }
    let same_host = std::str::from_utf8(origin)
        .ok()
        .and_then(origin_parts)
        .is_some_and(|parts| parts.host == host);
    if same_host || is_trusted() {
        return Ok(());
    }
    Err(CrossOriginRefusal::OldBrowser)
}

/// The host a request names, as Go's `Request.Host` holds it: the `Host`
/// header, or the URI's authority (HTTP/2's `:authority`) without one.
pub fn request_host(parts: &Parts) -> Vec<u8> {
    match parts.headers.get(HOST) {
        Some(host) => host.as_bytes().to_vec(),
        None => parts
            .uri
            .authority()
            .map(|authority| authority.as_str().as_bytes().to_vec())
            .unwrap_or_default(),
    }
}

/// The first value of a header, as Go's `Header.Get` reads it: empty when
/// the request has none.
pub(crate) fn first<'a>(headers: &'a HeaderMap, name: &str) -> &'a [u8] {
    headers.get(name).map_or(&[][..], |value| value.as_bytes())
}

/// What Go's `url.Parse` finds in a URL, as far as an origin needs it.
pub(crate) struct OriginParts {
    /// The scheme, lower case; empty without one.
    pub scheme: String,
    /// The host with its port, percent-decoded; empty without one.
    pub host: Vec<u8>,
    /// The URL has a user (`user@`), even an empty one.
    pub user: bool,
    /// The URL has a path, `/` included.
    pub path: bool,
    /// The URL is opaque: a scheme and no `/` after it (`mailto:x`).
    pub opaque: bool,
}

/// Reads a URL as Go's `url.Parse` does, or `None` where it fails.
pub(crate) fn origin_parts(raw: &str) -> Option<OriginParts> {
    if raw.bytes().any(|b| b < b' ' || b == 0x7f) {
        return None;
    }
    let (url, fragment) = match raw.split_once('#') {
        Some((url, fragment)) => (url, Some(fragment)),
        None => (raw, None),
    };
    if let Some(fragment) = fragment {
        percent_decode(fragment.as_bytes(), Mode::Other)?;
    }
    let mut parts = OriginParts {
        scheme: String::new(),
        host: Vec::new(),
        user: false,
        path: false,
        opaque: false,
    };
    if url == "*" {
        parts.path = true;
        return Some(parts);
    }
    let (scheme, rest) = split_scheme(url)?;
    parts.scheme = scheme.to_ascii_lowercase();
    let rest = if rest.ends_with('?') && rest.matches('?').count() == 1 {
        &rest[..rest.len() - 1]
    } else {
        rest.split_once('?').map_or(rest, |(rest, _)| rest)
    };
    if !rest.starts_with('/') {
        if !parts.scheme.is_empty() {
            parts.opaque = true;
            return Some(parts);
        }
        if rest
            .split('/')
            .next()
            .is_some_and(|segment| segment.contains(':'))
        {
            return None;
        }
    }
    let mut path = rest;
    if rest.starts_with("//") && (!parts.scheme.is_empty() || !rest.starts_with("///")) {
        let authority = &rest[2..];
        let (authority, after) = match authority.find('/') {
            Some(i) => (&authority[..i], &authority[i..]),
            None => (authority, ""),
        };
        path = after;
        let (userinfo, host) = match authority.rfind('@') {
            Some(i) => (Some(&authority[..i]), &authority[i + 1..]),
            None => (None, authority),
        };
        parts.host = parse_host(host)?;
        if let Some(userinfo) = userinfo {
            if !userinfo.chars().all(is_userinfo_char) {
                return None;
            }
            percent_decode(userinfo.as_bytes(), Mode::Other)?;
            parts.user = true;
        }
    }
    percent_decode(path.as_bytes(), Mode::Other)?;
    parts.path = !path.is_empty();
    Some(parts)
}

/// A URL's scheme and the rest, as Go's `getScheme` splits them: no scheme
/// when the URL does not start with letters, digits, `+`, `-` and `.`
/// (a letter first) followed by `:`, and an error for a leading `:`.
fn split_scheme(url: &str) -> Option<(&str, &str)> {
    for (i, c) in url.bytes().enumerate() {
        match c {
            b'a'..=b'z' | b'A'..=b'Z' => {}
            b'0'..=b'9' | b'+' | b'-' | b'.' if i > 0 => {}
            b':' if i == 0 => return None,
            b':' => return Some((&url[..i], &url[i + 1..])),
            _ => return Some(("", url)),
        }
    }
    Some(("", url))
}

/// A host and its optional port, percent-decoded, as Go's `parseHost` reads
/// it: a port is `:` and digits, and a host holds no byte a host must
/// escape and no escape of an ASCII byte but `%25`.
fn parse_host(host: &str) -> Option<Vec<u8>> {
    let port = if host.starts_with('[') {
        &host[host.rfind(']')? + 1..]
    } else {
        host.rfind(':').map_or("", |i| &host[i..])
    };
    let valid_port =
        port.is_empty() || (port.starts_with(':') && port[1..].bytes().all(|c| c.is_ascii_digit()));
    if !valid_port {
        return None;
    }
    percent_decode(host.as_bytes(), Mode::Host)
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Mode {
    Host,
    Other,
}

/// `s` with its percent escapes decoded, or `None` for an escape without two
/// hex digits, and in a host, for a byte it must escape or an escape of an
/// ASCII byte but `%25`.
fn percent_decode(s: &[u8], mode: Mode) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        let c = s[i];
        if c == b'%' {
            let high = s.get(i + 1).and_then(|c| char::from(*c).to_digit(16))?;
            let low = s.get(i + 2).and_then(|c| char::from(*c).to_digit(16))?;
            if mode == Mode::Host && high < 8 && &s[i..i + 3] != b"%25" {
                return None;
            }
            out.push(u8::try_from(high << 4 | low).ok()?);
            i += 3;
            continue;
        }
        if mode == Mode::Host && c < 0x80 && host_escapes(c) {
            return None;
        }
        out.push(c);
        i += 1;
    }
    Some(out)
}

/// Whether a host must escape an ASCII byte, as Go's `shouldEscape` has it
/// for a host.
fn host_escapes(c: u8) -> bool {
    !(c.is_ascii_alphanumeric() || b"!$&'()*+,;=:[]<>\"-_.~".contains(&c))
}

fn is_userinfo_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || "-._:~!$&'()*+,;=%@".contains(c)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_origin_reads_as_go_reads_it() {
        let parts = origin_parts("https://app.example.com:8443").unwrap();
        assert_eq!(parts.scheme, "https");
        assert_eq!(parts.host, b"app.example.com:8443");
        assert!(!parts.user && !parts.path && !parts.opaque);
        assert!(origin_parts("https://app.example.com/").unwrap().path);
        assert!(origin_parts("https://me@app.example.com").unwrap().user);
        assert!(origin_parts("mailto:me").unwrap().opaque);
        assert!(origin_parts("app.example.com").unwrap().scheme.is_empty());
        assert!(origin_parts("null").unwrap().host.is_empty());
        assert!(origin_parts("https://app.example.com:x").is_none());
        assert!(origin_parts("https://app example.com").is_none());
        assert!(origin_parts(":x").is_none());
        assert_eq!(
            origin_parts("http://api%2Eexample.com").map(|p| p.host),
            None,
            "an escaped ASCII byte in a host"
        );
    }
}
