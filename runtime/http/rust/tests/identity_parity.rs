//! The shared identity vectors (D50): `runtime/http/testdata/identity_parity.json`,
//! which the Go runtime writes and the Go, TypeScript and Rust identity
//! runtimes each run. This runs every case of every section through the
//! `identity` module, with the harness `runtime/http/testdata/README.md`
//! states for each, and compares what it computes with the case's `want`
//! as JSON values.

use std::collections::{BTreeMap, HashSet};
use std::time::Duration;

use base64::engine::general_purpose::STANDARD_NO_PAD;
use base64::Engine;
use http::{HeaderMap, HeaderName, HeaderValue, Method};
use serde::Deserialize;
use serde_json::{json, Value};
use superschematic_http_runtime::has_any_permission;
use superschematic_http_runtime::identity::{
    capabilities_of, check_cross_origin, check_password, effective_permissions, extract_credential,
    hash_password_with_salt, hash_token, token_from_bytes, uncovered, valid_permission,
    verify_password, Argon2Params, Config, CredentialOutcome, Route, TOKEN_BYTES,
};

const VECTORS: &str = "../testdata/identity_parity.json";

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    config: Vec<ConfigCase>,
    password_rule: Vec<PasswordRuleCase>,
    hashes: Vec<HashCase>,
    verify: Vec<VerifyCase>,
    tokens: Vec<TokenCase>,
    cookies: Vec<CookieCase>,
    credentials: Vec<CredentialCase>,
    cross_origin: Vec<CrossOriginCase>,
    permission_names: Vec<PermissionCase>,
    effective_permissions: Vec<EffectiveCase>,
    grants: Vec<GrantCase>,
    capabilities: CapabilitiesTable,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ConfigCase {
    name: String,
    input: Value,
    want: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PasswordRuleCase {
    name: String,
    password: String,
    valid: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct HashCase {
    name: String,
    password: String,
    salt: String,
    params: Argon2Params,
    phc: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct VerifyCase {
    name: String,
    phc: String,
    password: String,
    current: Argon2Params,
    want: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct TokenCase {
    name: String,
    bytes: String,
    token: String,
    hash: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct CookieCase {
    name: String,
    config: Value,
    token: String,
    max_age_seconds: u64,
    want: Value,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct CredentialCase {
    name: String,
    cookie_name: String,
    headers: Vec<(String, String)>,
    want: Value,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct CrossOriginCase {
    name: String,
    method: String,
    host: String,
    headers: BTreeMap<String, String>,
    trusted_origins: Vec<String>,
    want: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PermissionCase {
    permission: String,
    valid: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct EffectiveCase {
    name: String,
    roles: Vec<ParityRole>,
    want: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityRole {
    #[allow(dead_code)]
    name: String,
    permissions: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct GrantCase {
    name: String,
    held: Vec<String>,
    given: Vec<String>,
    want: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CapabilitiesTable {
    routes: Vec<Route>,
    callers: Vec<CapabilityCase>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CapabilityCase {
    name: String,
    permissions: Vec<String>,
    want: Value,
}

fn vectors() -> ParityFile {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&data).unwrap_or_else(|err| panic!("parse {}: {err}", path.display()))
}

/// Fails when two cases of a section share a name, or the section is empty.
fn unique<'a>(section: &str, names: impl IntoIterator<Item = &'a str>) {
    let mut seen = HashSet::new();
    for name in names {
        assert!(seen.insert(name), "{section}: case {name:?} repeats");
    }
    assert!(!seen.is_empty(), "{section} has no cases");
}

/// Collects every mismatch of a section, so one run reports them all.
#[derive(Default)]
struct Check {
    failures: Vec<String>,
}

impl Check {
    fn eq(&mut self, section: &str, name: &str, got: Value, want: &Value) {
        if &got != want {
            self.failures
                .push(format!("{section}: {name}: got {got}, want {want}"));
        }
    }

    fn done(self) {
        assert!(
            self.failures.is_empty(),
            "{} vectors failed:\n{}",
            self.failures.len(),
            self.failures.join("\n")
        );
    }
}

fn config_of(input: &Value) -> Option<Config> {
    let json = serde_json::to_vec(input).unwrap();
    Config::parse(&json).ok()
}

#[test]
fn config() {
    let file = vectors();
    unique("config", file.config.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.config {
        let got = config_of(&case.input).map_or(Value::Null, |c| serde_json::to_value(c).unwrap());
        check.eq("config", &case.name, got, &case.want);
    }
    check.done();
}

#[test]
fn password_rule() {
    let file = vectors();
    unique(
        "passwordRule",
        file.password_rule.iter().map(|c| c.name.as_str()),
    );
    let mut check = Check::default();
    for case in &file.password_rule {
        let got = check_password(&case.password).is_ok();
        check.eq("passwordRule", &case.name, json!(got), &json!(case.valid));
    }
    check.done();
}

#[test]
fn hashes() {
    let file = vectors();
    unique("hashes", file.hashes.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.hashes {
        let salt = STANDARD_NO_PAD
            .decode(&case.salt)
            .unwrap_or_else(|err| panic!("{}: salt: {err}", case.name));
        let got = hash_password_with_salt(&case.password, &salt, case.params)
            .unwrap_or_else(|err| panic!("{}: {err}", case.name));
        check.eq("hashes", &case.name, json!(got), &json!(case.phc));
    }
    check.done();
}

#[test]
fn verify() {
    let file = vectors();
    unique("verify", file.verify.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.verify {
        let got = match verify_password(&case.phc, &case.password, case.current) {
            Ok(verified) => {
                json!({"malformed": false, "ok": verified.ok, "rehash": verified.rehash})
            }
            Err(_) => json!({"malformed": true, "ok": false, "rehash": false}),
        };
        check.eq("verify", &case.name, got, &case.want);
    }
    check.done();
}

#[test]
fn tokens() {
    let file = vectors();
    unique("tokens", file.tokens.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.tokens {
        let bytes: Vec<u8> = (0..case.bytes.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&case.bytes[i..i + 2], 16).unwrap())
            .collect();
        let bytes: [u8; TOKEN_BYTES] = bytes.try_into().expect("32 bytes");
        let got = json!([token_from_bytes(&bytes), hash_token(&case.token)]);
        check.eq("tokens", &case.name, got, &json!([case.token, case.hash]));
    }
    check.done();
}

#[test]
fn cookies() {
    let file = vectors();
    unique("cookies", file.cookies.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.cookies {
        let config = config_of(&case.config)
            .unwrap_or_else(|| panic!("{}: the config is refused", case.name));
        let got = json!({
            "set": config.session_cookie(&case.token, Duration::from_secs(case.max_age_seconds)),
            "clear": config.clear_cookie(),
        });
        check.eq("cookies", &case.name, got, &case.want);
    }
    check.done();
}

#[test]
fn credentials() {
    let file = vectors();
    unique(
        "credentials",
        file.credentials.iter().map(|c| c.name.as_str()),
    );
    let mut check = Check::default();
    for case in &file.credentials {
        let mut headers = HeaderMap::new();
        for (name, value) in &case.headers {
            headers.append(
                HeaderName::from_bytes(name.as_bytes()).unwrap(),
                HeaderValue::from_str(value).unwrap(),
            );
        }
        let got = match extract_credential(&headers, &case.cookie_name) {
            CredentialOutcome::None => {
                json!({"outcome": "none", "transport": null, "token": null})
            }
            CredentialOutcome::Usable(credential) => json!({
                "outcome": "usable",
                "transport": credential.transport.as_str(),
                "token": credential.token,
            }),
            CredentialOutcome::Invalid(transport) => json!({
                "outcome": "invalid",
                "transport": transport.as_str(),
                "token": null,
            }),
        };
        check.eq("credentials", &case.name, got, &case.want);
    }
    check.done();
}

#[test]
fn cross_origin() {
    let file = vectors();
    unique(
        "crossOrigin",
        file.cross_origin.iter().map(|c| c.name.as_str()),
    );
    let mut check = Check::default();
    for case in &file.cross_origin {
        // The trusted origins are read as a config's, as a runtime takes
        // them.
        let config = config_of(&json!({"trustedOrigins": case.trusted_origins}))
            .unwrap_or_else(|| panic!("{}: the trusted origins are refused", case.name));
        let mut headers = HeaderMap::new();
        for (name, value) in &case.headers {
            headers.insert(
                HeaderName::from_bytes(name.as_bytes()).unwrap(),
                HeaderValue::from_str(value).unwrap(),
            );
        }
        let method = Method::from_bytes(case.method.as_bytes()).unwrap();
        let got = match check_cross_origin(
            &method,
            case.host.as_bytes(),
            &headers,
            &config.trusted_origins,
        ) {
            Ok(()) => "allow",
            Err(_) => "refuse",
        };
        check.eq("crossOrigin", &case.name, json!(got), &json!(case.want));
    }
    check.done();
}

#[test]
fn permission_names() {
    let file = vectors();
    unique(
        "permissionNames",
        file.permission_names.iter().map(|c| c.permission.as_str()),
    );
    let mut check = Check::default();
    for case in &file.permission_names {
        let got = valid_permission(&case.permission);
        check.eq(
            "permissionNames",
            &case.permission,
            json!(got),
            &json!(case.valid),
        );
    }
    check.done();
}

#[test]
fn effective() {
    let file = vectors();
    unique(
        "effectivePermissions",
        file.effective_permissions.iter().map(|c| c.name.as_str()),
    );
    let mut check = Check::default();
    for case in &file.effective_permissions {
        let got = effective_permissions(case.roles.iter().map(|r| r.permissions.as_slice()));
        check.eq(
            "effectivePermissions",
            &case.name,
            json!(got),
            &json!(case.want),
        );
    }
    check.done();
}

#[test]
fn grants() {
    let file = vectors();
    unique("grants", file.grants.iter().map(|c| c.name.as_str()));
    let mut check = Check::default();
    for case in &file.grants {
        let missing = uncovered(&case.held, &case.given);
        let got = json!({"allowed": missing.is_empty(), "uncovered": missing});
        check.eq("grants", &case.name, got, &case.want);
    }
    check.done();
}

#[test]
fn capabilities() {
    let file = vectors();
    let table = &file.capabilities;
    unique(
        "capabilities",
        table.callers.iter().map(|c| c.name.as_str()),
    );
    let mut check = Check::default();
    for case in &table.callers {
        let got = capabilities_of(&table.routes, &case.permissions, &has_any_permission);
        check.eq(
            "capabilities",
            &case.name,
            serde_json::to_value(got).unwrap(),
            &case.want,
        );
    }
    check.done();
}
