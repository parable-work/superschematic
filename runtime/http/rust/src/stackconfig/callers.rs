//! The callers field: what the server of an API with a service clause
//! verifies a caller's service credential against (sections 3.4 and 9.2 of
//! `docs/stack-model.md`), Go's `LoadCallers`.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde_json::{Map, Value};

use super::{process_vars, quote, StackConfigError, Vars};
use crate::{JwsAlgorithm, ServiceAuthConfig, ServiceCallerConfig, ServiceIssuer};

/// What the name of an API's callers field adds to the API's name in upper
/// snake case: `SHOP_API_CALLERS` is shop-api's.
pub const CALLERS_SUFFIX: &str = "_CALLERS";

/// A list's length variable is at most this, so a mistaken value cannot
/// make the loader read an unbounded number of variables.
const MAX_COUNT: i64 = 1000;

/// The subject claim of an issuer whose field leaves it empty, as the
/// verifier reads an empty one in Go and TypeScript.
const DEFAULT_SUBJECT_CLAIM: &str = "sub";

/// The members of a JWK that Go's `serviceauth.JWK` holds, each a string.
const JWK_MEMBERS: [&str; 10] = ["kty", "kid", "use", "alg", "crv", "x", "y", "n", "e", "d"];

/// Reads the callers field named `field` from the environment. Its value
/// is an `ir.ServiceAuth`, one variable per member, a list of objects as a
/// variable holding its length and each object's members under its index:
///
/// ```text
/// field_ISSUERS                          the number of issuers
/// field_ISSUERS_<i>_ISSUER               the iss the issuer writes
/// field_ISSUERS_<i>_ISSUER_ALIASES       other iss values, comma-separated
/// field_ISSUERS_<i>_AUDIENCE             the aud a token must hold
/// field_ISSUERS_<i>_ALGORITHMS           RS256, ES256 or EdDSA, comma-separated
/// field_ISSUERS_<i>_JWKS_URL             where the issuer's keys are, or
/// field_ISSUERS_<i>_KEYS                 the number of keys, each
/// field_ISSUERS_<i>_KEYS_<j>_JWK         a public JWK as JSON
/// field_ISSUERS_<i>_SUBJECT_CLAIM        the claim that names the caller
/// field_ISSUERS_<i>_MAX_LIFETIME_SECONDS the longest a token may live
/// field_ISSUERS_<i>_CALLERS              the number of callers, each
/// field_ISSUERS_<i>_CALLERS_<k>_SUBJECT     the claim's value
/// field_ISSUERS_<i>_CALLERS_<k>_DEPLOYABLE  the deployable it is
/// field_ISSUERS_<i>_CALLERS_<k>_SERVES      the APIs it serves, comma-separated
/// ```
///
/// It returns the field as the [`ServiceAuthConfig`] a
/// [`crate::JwtServiceAuthenticator`] takes, each issuer's callers keyed by
/// subject, with the config's defaults where the field leaves a member
/// empty: `sub` as the subject claim, and 60 seconds of leeway. No issuers
/// means no server calls the API in the environment: the authenticator then
/// refuses every service credential. A variable under the field's name
/// that is no member, a list whose length its variables do not match, and
/// an algorithm the authenticator does not verify, which Go's verifier
/// refuses when it is built, are refused.
pub fn load_callers(field: &str) -> Result<ServiceAuthConfig, StackConfigError> {
    read_callers(field, &process_vars(field)?)
}

/// [`load_callers`] over `vars` in place of the environment.
pub fn load_callers_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<ServiceAuthConfig, StackConfigError> {
    read_callers(field, vars)
}

/// Reads a callers field's variables and records which it read.
struct CallerVars<'a> {
    vars: HashMap<&'a str, &'a str>,
    read: BTreeSet<String>,
    problems: Vec<String>,
}

impl<'a> CallerVars<'a> {
    fn get(&mut self, name: &str) -> Option<&'a str> {
        self.read.insert(name.to_string());
        self.vars.get(name).copied()
    }

    fn fail(&mut self, problem: String) {
        self.problems.push(problem);
    }

    /// A variable that must be set and not empty.
    fn required(&mut self, name: &str) -> String {
        let value = self.get(name).unwrap_or_default().to_string();
        if value.is_empty() {
            self.fail(format!("required environment variable {name} is not set"));
        }
        value
    }

    /// A list's length.
    fn count(&mut self, name: &str, least: i64) -> usize {
        let value = self.required(name);
        if value.is_empty() {
            return 0;
        }
        match value.parse::<i64>() {
            Ok(n) if (least..=MAX_COUNT).contains(&n) => usize::try_from(n).unwrap_or(0),
            _ => {
                self.fail(format!(
                    "environment variable {name} is {}; want a number of entries from {least} \
                     to {MAX_COUNT}",
                    quote(&value)
                ));
                0
            }
        }
    }

    /// A comma-separated list, which must not be empty when set.
    fn list(&mut self, name: &str, required: bool) -> Vec<String> {
        let value = self.get(name).unwrap_or_default().to_string();
        if value.is_empty() {
            if required {
                self.fail(format!("required environment variable {name} is not set"));
            }
            return Vec::new();
        }
        let mut out = Vec::new();
        for item in value.split(',').map(str::trim) {
            if item.is_empty() {
                self.fail(format!("environment variable {name} has an empty entry"));
                continue;
            }
            out.push(item.to_string());
        }
        out
    }

    /// The algorithms an issuer accepts.
    fn algorithms(&mut self, name: &str) -> Vec<JwsAlgorithm> {
        let mut out = Vec::new();
        for alg in self.list(name, true) {
            match JwsAlgorithm::from_header(&alg) {
                Some(algorithm) => out.push(algorithm),
                None => self.fail(format!(
                    "environment variable {name} names the algorithm {}, which is not RS256, \
                     ES256 or EdDSA",
                    quote(&alg)
                )),
            }
        }
        out
    }

    /// The issuer whose variables begin with `p`.
    fn issuer(&mut self, p: &str) -> ServiceIssuer {
        let issuer = self.required(&format!("{p}ISSUER"));
        let issuer_aliases = self.list(&format!("{p}ISSUER_ALIASES"), false);
        let audience = self.required(&format!("{p}AUDIENCE"));
        let algorithms = self.algorithms(&format!("{p}ALGORITHMS"));
        let jwks_url = self
            .get(&format!("{p}JWKS_URL"))
            .filter(|url| !url.is_empty())
            .map(str::to_string);
        let mut keys: Option<Vec<Map<String, Value>>> = None;
        if self.get(&format!("{p}KEYS")).is_some() {
            for j in 0..self.count(&format!("{p}KEYS"), 1) {
                let name = format!("{p}KEYS_{j}_JWK");
                let text = self.required(&name);
                if text.is_empty() {
                    continue;
                }
                match parse_jwk(&text) {
                    Ok(key) => keys.get_or_insert_with(Vec::new).push(key),
                    Err(reason) => {
                        self.fail(format!(
                            "environment variable {name} is not a JWK: {reason}"
                        ));
                    }
                }
            }
        }
        let subject_claim = self
            .get(&format!("{p}SUBJECT_CLAIM"))
            .filter(|claim| !claim.is_empty())
            .unwrap_or(DEFAULT_SUBJECT_CLAIM)
            .to_string();
        let mut max_lifetime_seconds = None;
        let lifetime_name = format!("{p}MAX_LIFETIME_SECONDS");
        if let Some(value) = self.get(&lifetime_name) {
            match value
                .parse::<i64>()
                .ok()
                .and_then(|n| u64::try_from(n).ok())
            {
                Some(n) if n >= 1 => max_lifetime_seconds = Some(n),
                _ => self.fail(format!(
                    "environment variable {lifetime_name} is {}; want a whole number of seconds \
                     above 0",
                    quote(value)
                )),
            }
        }
        let mut callers = BTreeMap::new();
        for k in 0..self.count(&format!("{p}CALLERS"), 1) {
            let q = format!("{p}CALLERS_{k}_");
            let subject = self.required(&format!("{q}SUBJECT"));
            let caller = ServiceCallerConfig {
                deployable: self.required(&format!("{q}DEPLOYABLE")),
                serves: self.list(&format!("{q}SERVES"), true),
            };
            if callers.contains_key(&subject) && !subject.is_empty() {
                self.fail(format!(
                    "environment variable {q}SUBJECT is {subject}, another caller's of the issuer \
                     {issuer}"
                ));
                continue;
            }
            callers.insert(subject, caller);
        }
        ServiceIssuer {
            issuer,
            issuer_aliases,
            audience,
            algorithms,
            jwks_url,
            jwks_bearer_token_file: None,
            keys,
            subject_claim,
            max_lifetime_seconds,
            callers,
        }
    }
}

/// A JWK's JSON as the object a config's keys hold, or why it is none.
/// Like Go's `serviceauth.JWK`, it keeps the members that type has, each a
/// string, and drops the rest and the empty ones.
fn parse_jwk(text: &str) -> Result<Map<String, Value>, String> {
    let object: Map<String, Value> = serde_json::from_str(text).map_err(|err| err.to_string())?;
    let mut key = Map::new();
    for name in JWK_MEMBERS {
        match object.get(name) {
            None | Some(Value::Null) => {}
            Some(Value::String(value)) if value.is_empty() => {}
            Some(Value::String(value)) => {
                key.insert(name.to_string(), Value::String(value.clone()));
            }
            Some(_) => return Err(format!("its {name} is not a string")),
        }
    }
    Ok(key)
}

fn read_callers(field: &str, vars: &impl Vars) -> Result<ServiceAuthConfig, StackConfigError> {
    let prefix = format!("{field}_");
    let mut c = CallerVars {
        vars: vars.with_prefix(&prefix).into_iter().collect(),
        read: BTreeSet::new(),
        problems: Vec::new(),
    };
    let mut issuers = Vec::new();
    for i in 0..c.count(&format!("{field}_ISSUERS"), 0) {
        issuers.push(c.issuer(&format!("{field}_ISSUERS_{i}_")));
    }
    let mut unknown: Vec<&str> = c
        .vars
        .keys()
        .copied()
        .filter(|name| !c.read.contains(*name))
        .collect();
    unknown.sort_unstable();
    let unknown: Vec<String> = unknown
        .into_iter()
        .map(|name| {
            format!("environment variable {name} is no member of the callers field {field}")
        })
        .collect();
    c.problems.extend(unknown);
    if !c.problems.is_empty() {
        return Err(StackConfigError::new(c.problems));
    }
    Ok(ServiceAuthConfig {
        issuers,
        ..ServiceAuthConfig::default()
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vars(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(name, value)| (name.to_string(), value.to_string()))
            .collect()
    }

    #[test]
    fn an_issuer_with_its_defaults() {
        let config = load_callers_from(
            "A_CALLERS",
            &vars(&[
                ("A_CALLERS_ISSUERS", "1"),
                ("A_CALLERS_ISSUERS_0_ISSUER", "orders"),
                ("A_CALLERS_ISSUERS_0_AUDIENCE", "a"),
                ("A_CALLERS_ISSUERS_0_ALGORITHMS", "EdDSA, ES256"),
                ("A_CALLERS_ISSUERS_0_JWKS_URL", ""),
                ("A_CALLERS_ISSUERS_0_SUBJECT_CLAIM", ""),
                ("A_CALLERS_ISSUERS_0_CALLERS", "1"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT", "orders"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE", "Orders"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_SERVES", "shop-orders"),
                ("A_SERVICE_URL", "http://unrelated"),
            ]),
        )
        .unwrap();
        assert_eq!(config.leeway_seconds, 60);
        let issuer = &config.issuers[0];
        assert_eq!(
            issuer.algorithms,
            [JwsAlgorithm::EdDsa, JwsAlgorithm::Es256]
        );
        assert_eq!(issuer.subject_claim, "sub");
        assert_eq!(issuer.jwks_url, None);
        assert_eq!(issuer.keys, None);
        assert_eq!(issuer.max_lifetime_seconds, None);
        assert_eq!(issuer.callers["orders"].deployable, "Orders");
    }

    #[test]
    fn an_algorithm_the_authenticator_does_not_verify_is_refused() {
        let refused = load_callers_from(
            "A_CALLERS",
            &vars(&[
                ("A_CALLERS_ISSUERS", "1"),
                ("A_CALLERS_ISSUERS_0_ISSUER", "orders"),
                ("A_CALLERS_ISSUERS_0_AUDIENCE", "a"),
                ("A_CALLERS_ISSUERS_0_ALGORITHMS", "HS256"),
                ("A_CALLERS_ISSUERS_0_CALLERS", "1"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT", "orders"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE", "Orders"),
                ("A_CALLERS_ISSUERS_0_CALLERS_0_SERVES", "shop-orders"),
            ]),
        )
        .unwrap_err();
        assert_eq!(
            refused.problems(),
            ["environment variable A_CALLERS_ISSUERS_0_ALGORITHMS names the algorithm \"HS256\", \
              which is not RS256, ES256 or EdDSA"]
        );
    }

    #[test]
    fn a_jwk_keeps_the_members_go_reads() {
        let key =
            parse_jwk(r#"{"kty":"OKP","crv":"Ed25519","x":"AA","kid":"","ext":true,"use":null}"#)
                .unwrap();
        assert_eq!(
            Value::Object(key),
            serde_json::json!({ "kty": "OKP", "crv": "Ed25519", "x": "AA" })
        );
        assert!(parse_jwk(r#"{"kty":1}"#).unwrap_err().contains("kty"));
        assert!(parse_jwk("[]").is_err());
        assert!(parse_jwk("key").is_err());
    }
}
