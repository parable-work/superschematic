//! The Rust runtime of the core user model (D50): it signs users in and
//! out, resolves a request's session to a principal, and manages users,
//! roles and grants over the tables the core owns. A generated Rust server
//! wires it in and adds no logic. The Go and TypeScript runtimes do what it
//! does, held to the same vectors (`runtime/http/testdata/identity_parity.json`,
//! whose harness `runtime/http/testdata/README.md` states).
//!
//! The pieces:
//!
//! - [`Config`], the JSON every runtime reads alike: the session's
//!   lifetime, idle timeout and touch interval, the cookie, the trusted
//!   origins and the password hash's cost.
//! - Passwords: argon2id as PHC strings ([`hash_password`],
//!   [`verify_password`], which also says when a hash should be written
//!   again at the config's cost), and `Auth.Password`'s rule
//!   ([`check_password`]).
//! - Tokens: 32 random bytes in base64url ([`new_token`]), kept as their
//!   SHA-256 ([`hash_token`]).
//! - The credential on a request ([`extract_credential`]): `Authorization:
//!   Bearer` first, the session cookie only without an `Authorization`
//!   header, and no fallback to the cookie when `Authorization` is not a
//!   usable bearer token.
//! - The session cookie ([`Config::session_cookie`], [`Config::clear_cookie`]).
//! - The cross-origin check on a cookie request and a cookie login
//!   ([`check_cross_origin`], Go's `net/http.CrossOriginProtection`), and
//!   the credentialed CORS of the trusted origins ([`CorsLayer`]).
//! - Roles: the permission form ([`valid_permission`]), a principal's
//!   permissions ([`effective_permissions`]), the rule that no one grants
//!   what they do not hold ([`uncovered`]), and capabilities over the
//!   router's operation table ([`Route`], [`capabilities_of`]).
//! - [`Store`], and [`SqlStore`], its implementation for Postgres and
//!   SQLite built from the schema's identity descriptor, over a SQL
//!   [`Client`]: [`TokioPostgres`] with the `identity-postgres` feature,
//!   [`Rusqlite`] with `identity-sqlite`. The store parses logins with the
//!   scalar catalog the service passes ([`Scalars`]).
//! - [`Service`], every session and administration operation, its axum
//!   handlers ([`Service::handler`], [`Service::routes`]) and the router's
//!   [`IdentityAuthenticator`], which hands `admit` and the router a
//!   [`crate::Principal`].

mod config;
mod cookie;
mod cross_origin;
mod descriptor;
mod errors;
mod handlers;
mod json;
mod password;
mod permissions;
mod scalars;
mod service;
mod sql;
mod store;
mod time;
mod token;

pub use config::{
    Config, ConfigError, CookieConfig, PasswordConfig, DEFAULT_SAME_SITE,
    DEFAULT_SESSION_TTL_SECONDS, DEFAULT_TOUCH_INTERVAL_SECONDS, HOST_COOKIE_NAME,
    PLAIN_COOKIE_NAME, SECURE_COOKIE_NAME,
};
pub use cross_origin::{check_cross_origin, request_host, CrossOriginRefusal};
pub use descriptor::{
    CredentialColumns, Descriptor, DescriptorCredential, DescriptorRole, DescriptorRoleGrant,
    DescriptorSession, DescriptorUser, RoleColumns, RoleGrantColumns, SessionColumns, UserColumns,
    DESCRIPTOR_VERSION,
};
pub use errors::{
    CODE_CONFLICT, CODE_CROSS_ORIGIN, CODE_FORBIDDEN, CODE_INVALID_CREDENTIALS,
    CODE_INVALID_PERMISSION, CODE_NOT_FOUND, CODE_UNAUTHORIZED,
};
pub use handlers::{
    Cors, CorsLayer, IdentityAuthenticator, Operation, UserAdministration, UserSessions,
    DEFAULT_USER_ADMINISTRATION_PATH, DEFAULT_USER_SESSIONS_PATH, OPERATIONS, OP_CAPABILITIES,
    OP_CHANGE_PASSWORD, OP_CREATE_ROLE, OP_CREATE_USER, OP_DELETE_ROLE, OP_DISABLE_USER,
    OP_ENABLE_USER, OP_GET_USER, OP_GRANT_ROLE, OP_LIST_ROLES, OP_LIST_USERS, OP_LOGIN, OP_LOGOUT,
    OP_ME, OP_REGISTER, OP_REVOKE_ROLE, OP_SET_USER_PASSWORD, OP_UPDATE_ROLE,
};
pub use password::{
    check_password, hash_password, hash_password_with_salt, verify_password, Argon2Params,
    HashError, MalformedHash, Verified, KEY_BYTES, PASSWORD_SCALAR, SALT_BYTES,
};
pub use permissions::{capabilities_of, effective_permissions, uncovered, valid_permission, Route};
pub use scalars::{parse_uuid, uuid_to_base62, Scalars};
pub use service::{
    Capabilities, ChangePasswordInput, Clock, CreateUserInput, CurrentUser, IdentityPrincipal,
    IdentityRole, IdentityUser, Input, IssuedSession, LoginInput, LoginResult, Permits,
    RegisterInput, RoleInput, RoleRef, Service, SessionUser, SetPasswordInput,
    DEFAULT_PERMISSION_PREFIX, PERMISSION_ROLES_READ, PERMISSION_ROLES_WRITE,
    PERMISSION_USERS_READ, PERMISSION_USERS_WRITE,
};
#[cfg(feature = "identity-sqlite")]
pub use sql::Rusqlite;
#[cfg(feature = "identity-postgres")]
pub use sql::TokioPostgres;
pub use sql::{Client, ClientError, Conn, Dialect, SqlStore, SqlValue};
pub use store::{
    LoginRecord, NewSession, NewUser, Role, Session, SessionRecord, Store, StoreError, User,
};
pub use time::{format_rfc3339, format_sqlite, parse_timestamp};
pub use token::{
    extract_credential, hash_token, is_token, new_token, token_from_bytes, Credential,
    CredentialOutcome, NoRandomness, Transport, TOKEN_BYTES, TOKEN_LENGTH,
};
