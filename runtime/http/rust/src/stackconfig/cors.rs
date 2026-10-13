//! The CORS field: the origins of the sites that call an API (section 8.10
//! of `docs/stack-model.md`, D55), Go's `LoadCORS`.

use std::collections::HashMap;

use super::{process_vars, quote, StackConfigError, Vars};

/// What the name of an API's CORS field adds to the API's name in upper
/// snake case: `SHOP_API_CORS` is shop-api's (D55).
pub const CORS_SUFFIX: &str = "_CORS";

/// An API's CORS field: the origins of the sites that call it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Cors {
    /// Each `<scheme>://<host>[:<port>]`, in the field's order, with no
    /// repeat. Empty means no site calls the API in the environment, and
    /// its server answers no CORS.
    pub origins: Vec<String>,
}

/// Reads the CORS field named `field` from the environment, its one member
/// a comma-separated list:
///
/// ```text
/// field_ORIGINS  the sites' origins, <scheme>://<host>[:<port>] each
/// ```
///
/// An unset or empty `<field>_ORIGINS` is no origin. An entry that is no
/// origin, and a variable under the field's name that is no member, are
/// refused, in the order of their messages, as Go sorts them.
pub fn load_cors(field: &str) -> Result<Cors, StackConfigError> {
    read_cors(field, &process_vars(field)?)
}

/// [`load_cors`] over `vars` in place of the environment.
pub fn load_cors_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<Cors, StackConfigError> {
    read_cors(field, vars)
}

fn read_cors(field: &str, vars: &impl Vars) -> Result<Cors, StackConfigError> {
    let prefix = format!("{field}_");
    let member = format!("{field}_ORIGINS");
    let mut problems = Vec::new();
    let mut origins: Vec<String> = Vec::new();
    for (name, value) in vars.with_prefix(&prefix) {
        if name != member {
            problems.push(format!(
                "environment variable {name} is no member of the CORS field {field}"
            ));
            continue;
        }
        if value.is_empty() {
            continue;
        }
        for origin in value.split(',').map(str::trim) {
            if origin.is_empty() {
                problems.push(format!("environment variable {name}: an entry is empty"));
            } else if !is_origin(origin) {
                problems.push(format!(
                    "environment variable {name}: {} is no origin: an origin is \
                     <scheme>://<host>[:<port>] and nothing more",
                    quote(origin)
                ));
            } else if !origins.iter().any(|seen| seen == origin) {
                origins.push(origin.to_string());
            }
        }
    }
    if !problems.is_empty() {
        problems.sort();
        return Err(StackConfigError::new(problems));
    }
    Ok(Cors { origins })
}

/// Whether `origin` is one a browser sends, as Go's `checkOrigin` reads it
/// with `net/url`: an http or https scheme, a host, an optional port of
/// digits, and nothing after it: no user, path, query or fragment. The host
/// holds the characters Go's URL parser allows in one, without a
/// percent-escape.
fn is_origin(origin: &str) -> bool {
    let Some((scheme, authority)) = origin.split_once("://") else {
        return false;
    };
    if !scheme.eq_ignore_ascii_case("http") && !scheme.eq_ignore_ascii_case("https") {
        return false;
    }
    if authority.is_empty() || !authority.chars().all(host_char) {
        return false;
    }
    let port = if let Some(bracketed) = authority.strip_prefix('[') {
        match bracketed.split_once(']') {
            Some((_, "")) => return true,
            Some((_, rest)) => match rest.strip_prefix(':') {
                Some(port) => port,
                None => return false,
            },
            None => return false,
        }
    } else {
        match authority.rsplit_once(':') {
            Some((_, port)) => port,
            None => return true,
        }
    };
    port.bytes().all(|b| b.is_ascii_digit())
}

/// A character Go's URL parser keeps in a host: a letter, a digit, one of
/// `-._~`, a sub-delimiter, `:`, `[`, `]`, `<`, `>` and `"`, or any
/// character beyond ASCII.
fn host_char(c: char) -> bool {
    !c.is_ascii()
        || c.is_ascii_alphanumeric()
        || matches!(
            c,
            '-' | '.'
                | '_'
                | '~'
                | '!'
                | '$'
                | '&'
                | '\''
                | '('
                | ')'
                | '*'
                | '+'
                | ','
                | ';'
                | '='
                | ':'
                | '['
                | ']'
                | '<'
                | '>'
                | '"'
        )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_origin_is_a_scheme_a_host_and_a_port() {
        for ok in [
            "http://127.0.0.1:20441",
            "https://shop-web.acme.dev",
            "https://admin.acme.dev:8443",
            "http://[::1]:8080",
            "http://[::1]",
            "HTTPS://shop.dev",
            "http://a:",
        ] {
            assert!(is_origin(ok), "{ok}");
        }
        for bad in [
            "https://shop-web.acme.dev/",
            "https://shop-web.acme.dev/app",
            "https://shop.dev?",
            "https://shop.dev#",
            "https://user@shop.dev",
            "ftp://shop.dev",
            "shop.dev",
            "https://",
            "http://a:b",
            "http://[::1",
            "http://[::1]x",
            "http://a b",
            "http://a%41",
            "http://a\\b",
        ] {
            assert!(!is_origin(bad), "{bad}");
        }
    }
}
