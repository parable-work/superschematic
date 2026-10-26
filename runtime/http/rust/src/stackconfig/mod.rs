//! The config fields an API's edges derive in a stack (section 3.4 of
//! `docs/stack-model.md`), the twin of the Go runtime's `stackconfig`
//! package and of the TypeScript runtime's readers (D57): a database
//! connection for the API's database, a service endpoint for each API it
//! calls, a bucket connection for each bucket it lists (D54), a memory
//! store connection for each memory store it uses (D75), for an API
//! with a service clause its callers field, what its server verifies a
//! service credential against, and for an API a site calls its CORS field
//! (D55). The generated `config.rs` and entrypoint read each from the
//! environment variables a platform sets, one per member of the value the
//! edges' connectors derived: the field's name, an underscore and the
//! member's path in upper snake case (`SHOP_DB_DATABASE_URL`,
//! `SHOP_API_SERVICE_CREDENTIAL_SOURCE`, `SHOP_MEDIA_BUCKET_NAME`,
//! `SHOP_CACHE_MEMORY_STORE_URL`,
//! `SHOP_API_CALLERS_ISSUERS_0_AUDIENCE`, `SHOP_API_CORS_ORIGINS`).
//!
//! | Reader | Variables | Value |
//! |--------|-----------|-------|
//! | [`load_database`] | `<field>_URL`, or `<field>_CLOUD_SQL_INSTANCE`, `_DATABASE` and `_USER` | [`Database`] |
//! | [`load_service`] | `<field>_URL`, and `<field>_CREDENTIAL_SOURCE`, `_AUDIENCE`, `_TOKEN_FILE`, `_ISSUER`, `_KEY` and `_HEADERS` | [`Service`] |
//! | [`load_bucket`] | `<field>_NAME` and `<field>_ENDPOINT` | [`Bucket`] |
//! | [`load_memory_store`] | `<field>_STORE`, `_URL`, `_AUTH`, `_CA` and `_MEMBER` | [`MemoryStore`], `None` when none is set |
//! | [`load_callers`] | `<field>_ISSUERS` and the members of each issuer under its index | [`crate::ServiceAuthConfig`] |
//! | [`load_cors`] | `<field>_ORIGINS` | [`Cors`] |
//!
//! Each reader reads the variables Go's reads and refuses what Go's
//! refuses, with the same messages, all of a field's at once in one
//! [`StackConfigError`]: the vectors in
//! `runtime/http/testdata/stackconfig_parity.json` hold the three runtimes
//! to one encoding. An unset variable and an empty one are alike, as Go's
//! `os.Getenv` reads them. Each reader has a twin ending `_from` that reads
//! a map instead of the process's environment, for tests.
//!
//! A service's credential becomes the token source its client sends with
//! [`credential_source`].
//!
//! The members a Worker alone reads (D64) are refused, with a message that
//! says so: a database's D1 binding (`<field>_D1_BINDING`,
//! `<field>_D1_DATABASE`) and Hyperdrive binding
//! (`<field>_HYPERDRIVE_BINDING`, `<field>_HYPERDRIVE_CONFIG`, D92), a
//! service's binding (`<field>_BINDING_NAME`,
//! `<field>_BINDING_WORKER`), and a bucket's R2 binding and credential
//! (`<field>_BINDING`, `<field>_R2_ACCOUNT`, `<field>_R2_ACCESS_KEY_ID`,
//! `<field>_R2_TOKEN`, `<field>_R2_JURISDICTION`). The TypeScript runtime reads them for its Workers
//! entry, as Go's refuses them.

mod callers;
mod cors;
mod credential;

use std::collections::HashMap;
use std::fmt;

pub use callers::{load_callers, load_callers_from, CALLERS_SUFFIX};
pub use cors::{load_cors, load_cors_from, Cors, CORS_SUFFIX};
pub use credential::credential_source;

/// The refused variables of one field, one message each, which names the
/// variable and never carries a value the platform may have set from a
/// secret store. Its `Display` joins them with newlines, as Go's
/// `errors.Join` does.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StackConfigError {
    problems: Vec<String>,
}

impl StackConfigError {
    pub(crate) fn new(problems: Vec<String>) -> Self {
        Self { problems }
    }

    pub(crate) fn one(problem: impl Into<String>) -> Self {
        Self::new(vec![problem.into()])
    }

    /// The messages, in the order the reader found them.
    pub fn problems(&self) -> &[String] {
        &self.problems
    }
}

impl fmt::Display for StackConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.problems.join("\n"))
    }
}

impl std::error::Error for StackConfigError {}

/// A database connection: a connection string, or a Cloud SQL connector
/// configuration. Its `Debug` leaves out a connection string's password.
#[derive(Clone, PartialEq, Eq)]
pub enum Database {
    /// A connection string, which `postgres::connect` opens as it says.
    Url(String),
    /// A Cloud SQL instance, which connects with IAM database
    /// authentication.
    CloudSql(CloudSql),
}

/// A Cloud SQL connector configuration: what gcp's sql connector derives.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CloudSql {
    /// The instance connection name, `project:region:instance`.
    pub instance: String,
    /// The database's name on the instance.
    pub database: String,
    /// The IAM database user, `<account>@<project>.iam` for a service
    /// account.
    pub user: String,
}

impl fmt::Debug for Database {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Database::Url(url) => f.debug_tuple("Url").field(&redact_url(url)).finish(),
            Database::CloudSql(cloud_sql) => f.debug_tuple("CloudSql").field(cloud_sql).finish(),
        }
    }
}

/// A connection string without its password: a URL's user info keeps its
/// user, and a string of `key=value` pairs is left out whole.
fn redact_url(url: &str) -> String {
    let Some((scheme, rest)) = url.split_once("://") else {
        return "<a connection string>".to_string();
    };
    let authority_end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
    let (authority, tail) = rest.split_at(authority_end);
    match authority.rsplit_once('@') {
        Some((user_info, host)) => {
            let user = user_info.split(':').next().unwrap_or_default();
            format!("{scheme}://{user}:***@{host}{tail}")
        }
        None => url.to_string(),
    }
}

/// The credential sources the HTTP runtimes ship (D37), by the names
/// `<field>_CREDENTIAL_SOURCE` takes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Source {
    /// A Google ID token for the audience, from the metadata server.
    GoogleIdToken,
    /// A token read from the token file.
    TokenFile,
    /// A token signed with the key.
    SignedToken,
}

/// [`Source::GoogleIdToken`]'s name.
pub const SOURCE_GOOGLE_ID_TOKEN: &str = "google-id-token";
/// [`Source::TokenFile`]'s name.
pub const SOURCE_TOKEN_FILE: &str = "token-file";
/// [`Source::SignedToken`]'s name.
pub const SOURCE_SIGNED_TOKEN: &str = "signed-token";

/// The credential members, in the order the messages name them.
const MEMBERS: [&str; 4] = ["AUDIENCE", "TOKEN_FILE", "ISSUER", "KEY"];

impl Source {
    /// The source `<field>_CREDENTIAL_SOURCE` names, if the runtime ships it.
    pub fn parse(name: &str) -> Option<Self> {
        match name {
            SOURCE_GOOGLE_ID_TOKEN => Some(Source::GoogleIdToken),
            SOURCE_TOKEN_FILE => Some(Source::TokenFile),
            SOURCE_SIGNED_TOKEN => Some(Source::SignedToken),
            _ => None,
        }
    }

    /// Its name.
    pub const fn as_str(self) -> &'static str {
        match self {
            Source::GoogleIdToken => SOURCE_GOOGLE_ID_TOKEN,
            Source::TokenFile => SOURCE_TOKEN_FILE,
            Source::SignedToken => SOURCE_SIGNED_TOKEN,
        }
    }

    /// The credential members it reads.
    fn reads(self) -> &'static [&'static str] {
        match self {
            Source::GoogleIdToken => &["AUDIENCE"],
            Source::TokenFile => &["TOKEN_FILE"],
            Source::SignedToken => &["AUDIENCE", "ISSUER", "KEY"],
        }
    }
}

impl fmt::Display for Source {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The header a callee reads the service credential from, and the one a
/// credential with no headers travels in.
pub const SERVICE_AUTHORIZATION_HEADER: &str = "Service-Authorization";

/// A service endpoint: how the server reaches an API it calls.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Service {
    /// The callee's base URL.
    pub url: String,
    /// The source of the service credential the client sends, or `None`
    /// for none.
    pub credential: Option<Credential>,
}

/// The source of a service credential and the headers that carry it. Only
/// the members its source reads are set. Its `Debug` leaves out the key.
#[derive(Clone, PartialEq, Eq)]
pub struct Credential {
    pub source: Source,
    /// The credential's audience (`google-id-token` and `signed-token`).
    pub audience: Option<String>,
    /// The file a `token-file` source reads.
    pub token_file: Option<String>,
    /// A signed token's `iss` and `sub` (`signed-token`).
    pub issuer: Option<String>,
    /// The private JWK's JSON a `signed-token` source signs with.
    pub key: Option<String>,
    /// The headers the credential travels in; empty means
    /// [`SERVICE_AUTHORIZATION_HEADER`] alone.
    pub headers: Vec<String>,
}

impl Credential {
    /// The headers the credential travels in: its own, or
    /// [`SERVICE_AUTHORIZATION_HEADER`] alone. What a generated SDK's
    /// `ServiceCredential::headers` takes.
    pub fn header_names(&self) -> Vec<String> {
        if self.headers.is_empty() {
            vec![SERVICE_AUTHORIZATION_HEADER.to_string()]
        } else {
            self.headers.clone()
        }
    }
}

impl fmt::Debug for Credential {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Credential")
            .field("source", &self.source)
            .field("audience", &self.audience)
            .field("token_file", &self.token_file)
            .field("issuer", &self.issuer)
            .field("key", &self.key.as_ref().map(|_| "<a private JWK>"))
            .field("headers", &self.headers)
            .finish()
    }
}

/// A bucket connection: how the server reaches a bucket an API it serves
/// lists in its buckets (D54). It holds no credential: on gcp the
/// workload's own account reaches the bucket, and an emulator checks none.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Bucket {
    /// The bucket's name with its provider.
    pub name: String,
    /// The base URL of an emulator that serves the provider's API in its
    /// place, such as the local target's fake-gcs-server, without a
    /// trailing slash; `None` reaches the provider itself.
    pub endpoint: Option<String>,
}

/// A memory store connection: how the server, a job or a worker reaches a
/// memory store an API it serves uses (D75). It holds no credential: with
/// [`MemoryStoreAuth::Iam`] the client sends the workload's access token.
/// [`load_memory_store`] gives `None` for a field none of whose variables
/// is set, a disabled store.
#[derive(Clone, PartialEq, Eq)]
pub struct MemoryStore {
    /// The MemoryStore service's name, the first segment of every name the
    /// store holds for it.
    pub store: String,
    /// The server's `redis://` or `rediss://` URL.
    pub url: String,
    /// How the client authenticates.
    pub auth: MemoryStoreAuth,
    /// The PEM of the CA a `rediss://` server is verified against; `None`
    /// verifies it against the system's roots.
    pub ca: Option<String>,
    /// The member's name in a member of a parameterized environment, which
    /// prefixes the store's names `<store>@<member>`.
    pub member: Option<String>,
}

impl MemoryStore {
    /// Whether the URL is `rediss://`.
    pub fn tls(&self) -> bool {
        self.url.starts_with("rediss://")
    }
}

/// The URL is left out: it names the server, which no log holds.
impl fmt::Debug for MemoryStore {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("MemoryStore")
            .field("store", &self.store)
            .field("url", &"<redacted>")
            .field("auth", &self.auth)
            .field("ca", &self.ca.as_ref().map(|_| "<pem>"))
            .field("member", &self.member)
            .finish()
    }
}

/// How a memory store's client authenticates.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MemoryStoreAuth {
    /// The workload's OAuth access token as the password of user
    /// `default`, on each new connection; it needs `rediss://`.
    Iam,
    /// No credential.
    None,
}

impl MemoryStoreAuth {
    /// The authentication as the field's `<field>_AUTH` spells it.
    pub fn as_str(&self) -> &'static str {
        match self {
            MemoryStoreAuth::Iam => "iam",
            MemoryStoreAuth::None => "none",
        }
    }
}

/// The variables a reader reads: the process's environment, or a map.
pub(crate) trait Vars {
    /// A variable's value, `None` when it is unset.
    fn lookup(&self, name: &str) -> Option<&str>;

    /// Every variable whose name begins with `prefix`, in no order.
    fn with_prefix<'a>(&'a self, prefix: &'a str) -> Vec<(&'a str, &'a str)>;

    /// A variable's value, with an unset one and an empty one alike empty.
    fn get(&self, name: &str) -> &str {
        self.lookup(name).unwrap_or_default()
    }
}

impl Vars for HashMap<String, String> {
    fn lookup(&self, name: &str) -> Option<&str> {
        self.get(name).map(String::as_str)
    }

    fn with_prefix<'a>(&'a self, prefix: &'a str) -> Vec<(&'a str, &'a str)> {
        self.iter()
            .filter(|(name, _)| name.starts_with(prefix))
            .map(|(name, value)| (name.as_str(), value.as_str()))
            .collect()
    }
}

/// The process's variables whose names begin with `<field>_`: every
/// variable a reader of the field reads. A variable among them that is not
/// UTF-8 is refused.
pub(crate) fn process_vars(field: &str) -> Result<HashMap<String, String>, StackConfigError> {
    let prefix = format!("{field}_");
    let mut vars = HashMap::new();
    let mut problems = Vec::new();
    for (name, value) in std::env::vars_os() {
        let lossy = name.to_string_lossy();
        if !lossy.starts_with(&prefix) {
            continue;
        }
        match (name.to_str(), value.into_string()) {
            (Some(name), Ok(value)) => {
                vars.insert(name.to_string(), value);
            }
            _ => problems.push(format!("environment variable {lossy} is not UTF-8")),
        }
    }
    if problems.is_empty() {
        Ok(vars)
    } else {
        problems.sort();
        Err(StackConfigError::new(problems))
    }
}

/// Go's `%q` for the values the messages quote, as the TypeScript runtime
/// writes it: the value as a JSON string.
pub(crate) fn quote(value: &str) -> String {
    serde_json::to_string(value).unwrap_or_else(|_| format!("\"{value}\""))
}

/// Reads the database field named `field` from the environment:
/// `<field>_URL`, or the three `<field>_CLOUD_SQL_` variables. Refuses
/// neither, both, and only some of the three, and a D1 or a Hyperdrive
/// binding, which only a Worker reads.
pub fn load_database(field: &str) -> Result<Database, StackConfigError> {
    read_database(field, &process_vars(field)?)
}

/// [`load_database`] over `vars` in place of the environment.
pub fn load_database_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<Database, StackConfigError> {
    read_database(field, vars)
}

/// Refuses the variables of members only a Worker reads (D64), naming each
/// that is set and what they encode, as Go's `workerOnly` does.
fn worker_only(vars: &impl Vars, what: &str, names: &[String]) -> Result<(), StackConfigError> {
    let set: Vec<&str> = names
        .iter()
        .filter(|name| !vars.get(name).is_empty())
        .map(String::as_str)
        .collect();
    match set.as_slice() {
        [] => Ok(()),
        [one] => Err(StackConfigError::one(format!(
            "environment variable {one} is set: only a Worker reads {what} (D64)"
        ))),
        many => Err(StackConfigError::one(format!(
            "environment variables {} are set: only a Worker reads {what} (D64)",
            many.join(", ")
        ))),
    }
}

fn read_database(field: &str, vars: &impl Vars) -> Result<Database, StackConfigError> {
    worker_only(
        vars,
        "a D1 binding",
        &[
            format!("{field}_D1_BINDING"),
            format!("{field}_D1_DATABASE"),
        ],
    )?;
    worker_only(
        vars,
        "a Hyperdrive binding",
        &[
            format!("{field}_HYPERDRIVE_BINDING"),
            format!("{field}_HYPERDRIVE_CONFIG"),
        ],
    )?;
    let url = vars.get(&format!("{field}_URL"));
    let cloud = [
        format!("{field}_CLOUD_SQL_INSTANCE"),
        format!("{field}_CLOUD_SQL_DATABASE"),
        format!("{field}_CLOUD_SQL_USER"),
    ];
    let values = cloud.each_ref().map(|name| vars.get(name));
    let (mut set, mut unset) = (Vec::new(), Vec::new());
    for (name, value) in cloud.iter().zip(values) {
        if value.is_empty() {
            unset.push(name.as_str());
        } else {
            set.push(name.as_str());
        }
    }
    if !url.is_empty() && !set.is_empty() {
        return Err(StackConfigError::one(format!(
            "environment variables {field}_URL and {} are both set; a database connection is a \
             connection string or a Cloud SQL connector configuration, not both",
            set.join(", ")
        )));
    }
    if !url.is_empty() {
        return Ok(Database::Url(url.to_string()));
    }
    if set.is_empty() {
        return Err(StackConfigError::one(format!(
            "required environment variable {field}_URL, or {}, is not set",
            cloud.join(", ")
        )));
    }
    if !unset.is_empty() {
        return Err(StackConfigError::one(format!(
            "environment variables {} are set and {} is not; a Cloud SQL connection sets all three",
            set.join(", "),
            unset.join(", ")
        )));
    }
    let [instance, database, user] = values.map(str::to_string);
    Ok(Database::CloudSql(CloudSql {
        instance,
        database,
        user,
    }))
}

/// Reads the service field named `field` from the environment:
/// `<field>_URL`, and the `<field>_CREDENTIAL_` variables when
/// `<field>_CREDENTIAL_SOURCE` is set. Refuses no URL, a credential member
/// set without a source, a source the runtimes do not ship, a member the
/// source reads that is unset or one it does not read that is set, and
/// headers with an empty name or without `Service-Authorization`.
pub fn load_service(field: &str) -> Result<Service, StackConfigError> {
    read_service(field, &process_vars(field)?)
}

/// [`load_service`] over `vars` in place of the environment.
pub fn load_service_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<Service, StackConfigError> {
    read_service(field, vars)
}

fn read_service(field: &str, vars: &impl Vars) -> Result<Service, StackConfigError> {
    worker_only(
        vars,
        "a service binding",
        &[
            format!("{field}_BINDING_NAME"),
            format!("{field}_BINDING_WORKER"),
        ],
    )?;
    let url = vars.get(&format!("{field}_URL"));
    if url.is_empty() {
        return Err(StackConfigError::one(format!(
            "required environment variable {field}_URL is not set"
        )));
    }
    let url = url.to_string();
    let prefix = format!("{field}_CREDENTIAL_");
    let members = MEMBERS.map(|name| vars.get(&format!("{prefix}{name}")));
    let headers = vars.get(&format!("{prefix}HEADERS"));
    let source = vars.get(&format!("{prefix}SOURCE"));
    if source.is_empty() {
        if let Some(name) = MEMBERS
            .iter()
            .zip(members)
            .find_map(|(name, value)| (!value.is_empty()).then_some(name))
        {
            return Err(StackConfigError::one(format!(
                "environment variable {prefix}{name} is set and {prefix}SOURCE is not"
            )));
        }
        if !headers.is_empty() {
            return Err(StackConfigError::one(format!(
                "environment variable {prefix}HEADERS is set and {prefix}SOURCE is not"
            )));
        }
        return Ok(Service {
            url,
            credential: None,
        });
    }
    let Some(source) = Source::parse(source) else {
        return Err(StackConfigError::one(format!(
            "environment variable {prefix}SOURCE is {}; want {SOURCE_GOOGLE_ID_TOKEN}, \
             {SOURCE_TOKEN_FILE} or {SOURCE_SIGNED_TOKEN}",
            quote(source)
        )));
    };
    let mut problems = Vec::new();
    for (name, value) in MEMBERS.iter().zip(members) {
        let needed = source.reads().contains(name);
        if needed && value.is_empty() {
            problems.push(format!(
                "required environment variable {prefix}{name} is not set: a {source} credential \
                 reads it"
            ));
        } else if !needed && !value.is_empty() {
            problems.push(format!(
                "environment variable {prefix}{name} is set, which a {source} credential does \
                 not read"
            ));
        }
    }
    let mut header_names = Vec::new();
    if !headers.is_empty() {
        let mut has_service_header = false;
        for header in headers.split(',').map(str::trim) {
            if header.is_empty() {
                problems.push(format!(
                    "environment variable {prefix}HEADERS has an empty header name"
                ));
                continue;
            }
            has_service_header |= header.eq_ignore_ascii_case(SERVICE_AUTHORIZATION_HEADER);
            header_names.push(header.to_string());
        }
        if !has_service_header {
            problems.push(format!(
                "environment variable {prefix}HEADERS lacks {SERVICE_AUTHORIZATION_HEADER}, the \
                 header the callee reads"
            ));
        }
    }
    if !problems.is_empty() {
        return Err(StackConfigError::new(problems));
    }
    let [audience, token_file, issuer, key] =
        members.map(|value| (!value.is_empty()).then(|| value.to_string()));
    Ok(Service {
        url,
        credential: Some(Credential {
            source,
            audience,
            token_file,
            issuer,
            key,
            headers: header_names,
        }),
    })
}

/// Reads the bucket field named `field` from the environment:
/// `<field>_NAME`, and `<field>_ENDPOINT` when an emulator serves the
/// bucket (D54), its trailing slashes dropped. Refuses no name, and an
/// endpoint that is no http or https URL with a host.
pub fn load_bucket(field: &str) -> Result<Bucket, StackConfigError> {
    read_bucket(field, &process_vars(field)?)
}

/// [`load_bucket`] over `vars` in place of the environment.
pub fn load_bucket_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<Bucket, StackConfigError> {
    read_bucket(field, vars)
}

fn read_bucket(field: &str, vars: &impl Vars) -> Result<Bucket, StackConfigError> {
    worker_only(
        vars,
        "an R2 binding",
        &[
            format!("{field}_BINDING"),
            format!("{field}_R2_ACCOUNT"),
            format!("{field}_R2_ACCESS_KEY_ID"),
            format!("{field}_R2_TOKEN"),
            format!("{field}_R2_JURISDICTION"),
        ],
    )?;
    let name = vars.get(&format!("{field}_NAME"));
    let endpoint = vars.get(&format!("{field}_ENDPOINT"));
    let mut problems = Vec::new();
    if name.is_empty() {
        problems.push(format!(
            "required environment variable {field}_NAME is not set"
        ));
    }
    if !endpoint.is_empty() && !is_endpoint(endpoint) {
        problems.push(format!(
            "environment variable {field}_ENDPOINT is {}; want an http or https URL",
            quote(endpoint)
        ));
    }
    if !problems.is_empty() {
        return Err(StackConfigError::new(problems));
    }
    Ok(Bucket {
        name: name.to_string(),
        endpoint: (!endpoint.is_empty()).then(|| endpoint.trim_end_matches('/').to_string()),
    })
}

/// Reads the memory store field named `field` from the environment:
/// `<field>_STORE`, `<field>_URL` and `<field>_AUTH`, and `<field>_CA` and
/// `<field>_MEMBER` when set (D75). `Ok(None)` when none of them is set: a
/// disabled store. Refuses a required variable that is not set, a store or
/// a member that is no segment, a URL that is no `redis://` or
/// `rediss://` URL with a host and no user or password, a CA with
/// `redis://` or one that holds no PEM certificate, and an authentication
/// other than `iam` and `none`, or `iam` with `redis://`. No message holds
/// the URL or the CA.
pub fn load_memory_store(field: &str) -> Result<Option<MemoryStore>, StackConfigError> {
    read_memory_store(field, &process_vars(field)?)
}

/// [`load_memory_store`] over `vars` in place of the environment.
pub fn load_memory_store_from(
    field: &str,
    vars: &HashMap<String, String>,
) -> Result<Option<MemoryStore>, StackConfigError> {
    read_memory_store(field, vars)
}

fn read_memory_store(
    field: &str,
    vars: &impl Vars,
) -> Result<Option<MemoryStore>, StackConfigError> {
    let store = vars.get(&format!("{field}_STORE"));
    let url = vars.get(&format!("{field}_URL"));
    let ca = vars.get(&format!("{field}_CA"));
    let auth = vars.get(&format!("{field}_AUTH"));
    let member = vars.get(&format!("{field}_MEMBER"));
    if [store, url, ca, auth, member].iter().all(|v| v.is_empty()) {
        return Ok(None);
    }
    let mut problems = Vec::new();
    for (name, value) in [("STORE", store), ("URL", url), ("AUTH", auth)] {
        if value.is_empty() {
            problems.push(format!(
                "required environment variable {field}_{name} is not set: a memory store connection sets STORE, URL and AUTH, or none of its variables for a disabled store"
            ));
        }
    }
    let segment = "want one segment: 1 to 128 bytes, with no : * ? [ ] @ \\ or control character";
    if !store.is_empty() && !is_memory_store_segment(store) {
        problems.push(format!(
            "environment variable {field}_STORE is {}; {segment}",
            quote(store)
        ));
    }
    if !member.is_empty() && !is_memory_store_segment(member) {
        problems.push(format!(
            "environment variable {field}_MEMBER is {}; {segment}",
            quote(member)
        ));
    }
    if !url.is_empty() && !is_memory_store_url(url) {
        problems.push(format!(
            "environment variable {field}_URL is no redis:// or rediss:// URL with a host and no user or password"
        ));
    }
    let tls = url.starts_with("rediss://");
    if !ca.is_empty() {
        if url.starts_with("redis://") {
            problems.push(format!(
                "environment variable {field}_CA is set with a redis:// URL; a CA verifies a rediss:// server"
            ));
        } else if !ca.contains("-----BEGIN CERTIFICATE-----") {
            problems.push(format!(
                "environment variable {field}_CA holds no PEM certificate"
            ));
        }
    }
    let parsed = match auth {
        "iam" => {
            if !url.is_empty() && !tls {
                problems.push(format!(
                    "environment variable {field}_AUTH is iam with a redis:// URL; an access token travels only over rediss://"
                ));
            }
            MemoryStoreAuth::Iam
        }
        "none" | "" => MemoryStoreAuth::None,
        other => {
            problems.push(format!(
                "environment variable {field}_AUTH is {}; want iam or none",
                quote(other)
            ));
            MemoryStoreAuth::None
        }
    };
    if !problems.is_empty() {
        return Err(StackConfigError::new(problems));
    }
    Ok(Some(MemoryStore {
        store: store.to_string(),
        url: url.to_string(),
        auth: parsed,
        ca: (!ca.is_empty()).then(|| ca.to_string()),
        member: (!member.is_empty()).then(|| member.to_string()),
    }))
}

/// Whether `s` is one segment of a memory store's names (D75): 1 to 128
/// bytes, none of them a control byte or one of `:` `*` `?` `[` `]` `@`
/// `\`.
fn is_memory_store_segment(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 128
        && s.bytes()
            .all(|b| b >= 0x20 && b != 0x7f && !b":*?[]@\\".contains(&b))
}

/// Whether `url` is as Go's `^rediss?://[^/?#@\t\n\f\r ]+(/[0-9]*)?$`
/// reads one: redis or rediss, a host with no user or password, and a
/// database number at most.
fn is_memory_store_url(url: &str) -> bool {
    let Some(rest) = url
        .strip_prefix("redis://")
        .or_else(|| url.strip_prefix("rediss://"))
    else {
        return false;
    };
    let (host, path) = match rest.find('/') {
        Some(i) => (&rest[..i], Some(&rest[i + 1..])),
        None => (rest, None),
    };
    !host.is_empty()
        && !host
            .chars()
            .any(|c| matches!(c, '?' | '#' | '@' | '\t' | '\n' | '\x0c' | '\r' | ' '))
        && path.is_none_or(|p| p.bytes().all(|b| b.is_ascii_digit()))
}

/// Whether `endpoint` begins as Go's `^https?://[^/?#]+` does: an http or
/// https URL with a host.
fn is_endpoint(endpoint: &str) -> bool {
    let rest = endpoint
        .strip_prefix("http://")
        .or_else(|| endpoint.strip_prefix("https://"));
    rest.and_then(|rest| rest.chars().next())
        .is_some_and(|first| !matches!(first, '/' | '?' | '#'))
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
    fn a_database_is_a_url_or_a_cloud_sql_instance() {
        let url = load_database_from(
            "DB",
            &vars(&[("DB_URL", "postgres://u:p@h/d"), ("DB_CLOUD_SQL_USER", "")]),
        );
        assert_eq!(url, Ok(Database::Url("postgres://u:p@h/d".to_string())));
        let cloud = load_database_from(
            "DB",
            &vars(&[
                ("DB_CLOUD_SQL_INSTANCE", "p:r:i"),
                ("DB_CLOUD_SQL_DATABASE", "d"),
                ("DB_CLOUD_SQL_USER", "u@p.iam"),
            ]),
        );
        assert_eq!(
            cloud,
            Ok(Database::CloudSql(CloudSql {
                instance: "p:r:i".to_string(),
                database: "d".to_string(),
                user: "u@p.iam".to_string(),
            }))
        );
    }

    #[test]
    fn a_databases_debug_leaves_out_its_password() {
        let db = Database::Url("postgres://shop:secret@db:5432/shop?sslmode=disable".to_string());
        let debug = format!("{db:?}");
        assert!(!debug.contains("secret"), "{debug}");
        assert!(
            debug.contains("shop:***@db:5432/shop?sslmode=disable"),
            "{debug}"
        );
        let pairs = format!("{:?}", Database::Url("host=db password=secret".to_string()));
        assert!(!pairs.contains("secret"), "{pairs}");
        let plain = format!("{:?}", Database::Url("postgres://db/shop".to_string()));
        assert!(plain.contains("postgres://db/shop"), "{plain}");
    }

    #[test]
    fn a_credentials_debug_leaves_out_its_key() {
        let service = load_service_from(
            "S",
            &vars(&[
                ("S_URL", "http://s"),
                ("S_CREDENTIAL_SOURCE", "signed-token"),
                ("S_CREDENTIAL_AUDIENCE", "s"),
                ("S_CREDENTIAL_ISSUER", "o"),
                ("S_CREDENTIAL_KEY", "{\"d\":\"secret\"}"),
            ]),
        )
        .unwrap();
        let debug = format!("{service:?}");
        assert!(!debug.contains("secret"), "{debug}");
        let credential = service.credential.unwrap();
        assert_eq!(credential.header_names(), ["Service-Authorization"]);
    }

    #[test]
    fn the_process_environment_is_read_by_the_fields_prefix() {
        // A name no other test or process sets.
        let field = format!("STACKCONFIG_TEST_{}_BUCKET", std::process::id());
        assert_eq!(
            load_bucket(&field).unwrap_err().problems(),
            [format!(
                "required environment variable {field}_NAME is not set"
            )]
        );
    }

    #[test]
    fn an_endpoint_is_an_http_or_https_url_with_a_host() {
        for ok in ["http://a", "https://a:1/b", "http://127.0.0.1:24443"] {
            assert!(is_endpoint(ok), "{ok}");
        }
        for bad in [
            "a",
            "http://",
            "https:///a",
            "ftp://a",
            "http://?a",
            "HTTP://a",
        ] {
            assert!(!is_endpoint(bad), "{bad}");
        }
    }

    #[test]
    fn a_source_has_its_name() {
        for source in [
            Source::GoogleIdToken,
            Source::TokenFile,
            Source::SignedToken,
        ] {
            assert_eq!(Source::parse(source.as_str()), Some(source));
        }
        assert_eq!(Source::parse("api-key"), None);
    }
}
