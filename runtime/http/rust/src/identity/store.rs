//! The identity store: the storage the service runs over, the tables the
//! core owns and the project's user and role tables.

use std::fmt;
use std::time::SystemTime;

use async_trait::async_trait;

/// What a store refuses or fails with. The service turns the refusals into
/// problems and answers a database failure with a 500.
#[derive(Debug)]
pub enum StoreError {
    /// No row has the id, token hash or login.
    NotFound,
    /// Another user has the login, in the login column's case rule.
    LoginTaken,
    /// Another role has the name.
    RoleNameTaken,
    /// The schema has no `UserRole` table.
    NoRoles,
    /// A login the login scalar does not parse, with the scalar's reason.
    InvalidLogin { scalar: String, reason: String },
    /// A display name the name scalar does not parse, with its reason.
    InvalidName { scalar: String, reason: String },
    /// A descriptor the store cannot be built from.
    Descriptor(String),
    /// The database refused or failed a statement.
    Database(Box<dyn std::error::Error + Send + Sync>),
}

impl StoreError {
    pub(crate) fn database(message: impl Into<String>) -> StoreError {
        StoreError::Database(message.into().into())
    }
}

impl fmt::Display for StoreError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            StoreError::NotFound => f.write_str("identity: not found"),
            StoreError::LoginTaken => f.write_str("identity: the login is taken"),
            StoreError::RoleNameTaken => f.write_str("identity: the role name is taken"),
            StoreError::NoRoles => f.write_str("identity: the schema has no roles"),
            StoreError::InvalidLogin { scalar, reason } => {
                write!(f, "identity: the login is not a {scalar}: {reason}")
            }
            StoreError::InvalidName { scalar, reason } => {
                write!(f, "identity: the name is not a {scalar}: {reason}")
            }
            StoreError::Descriptor(reason) => write!(f, "identity: descriptor: {reason}"),
            StoreError::Database(err) => write!(f, "identity: {err}"),
        }
    }
}

impl std::error::Error for StoreError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            StoreError::Database(err) => Some(err.as_ref()),
            _ => None,
        }
    }
}

/// A user as the identity routes show one. Every id is in its key scalar's
/// wire form.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct User {
    pub id: String,
    pub login: String,
    pub name: String,
    /// The user's credential has `disabledAt` set.
    pub disabled: bool,
    /// The roles the user holds, by name. `get_user` and `list_users` fill
    /// them in; other methods leave them empty.
    pub roles: Vec<Role>,
}

/// A user and the password hash they sign in with.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LoginRecord {
    pub user: User,
    /// The credential's PHC string: empty when the user has no credential,
    /// or one with no password.
    pub password_hash: String,
}

/// A session row.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Session {
    pub id: String,
    pub user_id: String,
    pub created_at: SystemTime,
    pub expires_at: SystemTime,
    pub last_seen_at: Option<SystemTime>,
    pub revoked_at: Option<SystemTime>,
}

/// A session and its user.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionRecord {
    pub session: Session,
    pub user: User,
}

/// A role row.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Role {
    pub id: String,
    pub name: String,
    pub permissions: Vec<String>,
}

/// A user the store creates, with their credential.
#[derive(Clone, Debug)]
pub struct NewUser {
    /// The login as the caller gave it; the store parses it with the login
    /// scalar.
    pub login: String,
    /// The display name, written when the name column is not the login's,
    /// after the name scalar parses it. Empty means the parsed login.
    pub name: String,
    pub password_hash: String,
    pub at: SystemTime,
}

/// A session the store creates.
#[derive(Clone, Debug)]
pub struct NewSession {
    pub user_id: String,
    pub token_hash: String,
    pub created_at: SystemTime,
    pub expires_at: SystemTime,
}

/// The identity runtime's storage. Mutations the user model ties together
/// run in one transaction: `set_password` with its revocations,
/// `set_disabled` with a disable's revocations, `create_user` with the
/// credential, and `delete_role` with the role's grants.
#[async_trait]
pub trait Store: Send + Sync + 'static {
    /// The user whose login equals `login` once the login scalar parses it,
    /// with their password hash. A login the scalar refuses is
    /// [`StoreError::InvalidLogin`]; one no user has is
    /// [`StoreError::NotFound`].
    async fn find_login(&self, login: &str) -> Result<LoginRecord, StoreError>;
    /// [`Store::find_login`] by the user's id.
    async fn find_credential(&self, user_id: &str) -> Result<LoginRecord, StoreError>;
    /// The user with `id` and their roles.
    async fn get_user(&self, id: &str) -> Result<User, StoreError>;
    /// Every user with their roles, by login.
    async fn list_users(&self) -> Result<Vec<User>, StoreError>;
    /// Creates a user and their credential; the database generates the
    /// user's key. A taken login is [`StoreError::LoginTaken`], and a name
    /// the name scalar refuses [`StoreError::InvalidName`].
    async fn create_user(&self, user: NewUser) -> Result<User, StoreError>;

    /// Writes the user's password hash and `passwordChangedAt`, and revokes
    /// the user's sessions but `keep_session` (none when `None`).
    async fn set_password(
        &self,
        user_id: &str,
        password_hash: &str,
        at: SystemTime,
        keep_session: Option<&str>,
    ) -> Result<(), StoreError>;
    /// Replaces the user's password hash when it is still `old_hash`,
    /// keeping `passwordChangedAt`: the same password at a new cost.
    async fn rehash_password(
        &self,
        user_id: &str,
        old_hash: &str,
        new_hash: &str,
    ) -> Result<(), StoreError>;
    /// Sets the credential's `disabledAt` to `at`, or clears it. Disabling
    /// also revokes the user's sessions.
    async fn set_disabled(
        &self,
        user_id: &str,
        disabled: bool,
        at: SystemTime,
    ) -> Result<(), StoreError>;

    /// Creates a session; the database generates its key.
    async fn create_session(&self, session: NewSession) -> Result<Session, StoreError>;
    /// The session whose token hash is `token_hash`, and its user.
    async fn find_session(&self, token_hash: &str) -> Result<SessionRecord, StoreError>;
    /// Sets `lastSeenAt` to `at` when it is null or before `stale_before`,
    /// so concurrent requests write it once.
    async fn touch_session(
        &self,
        session_id: &str,
        at: SystemTime,
        stale_before: SystemTime,
    ) -> Result<(), StoreError>;
    /// Sets the session's `revokedAt` when it is null.
    async fn revoke_session(&self, session_id: &str, at: SystemTime) -> Result<(), StoreError>;
    /// Revokes every live session of the user but `except_session`.
    async fn revoke_user_sessions(
        &self,
        user_id: &str,
        except_session: Option<&str>,
        at: SystemTime,
    ) -> Result<(), StoreError>;

    /// Whether the schema has a `UserRole` table. Without one, `user_roles`
    /// answers none and the other role methods [`StoreError::NoRoles`].
    fn has_roles(&self) -> bool;
    /// The roles the user holds, by name.
    async fn user_roles(&self, user_id: &str) -> Result<Vec<Role>, StoreError>;
    /// Every role, by name.
    async fn list_roles(&self) -> Result<Vec<Role>, StoreError>;
    /// The role with `id`.
    async fn get_role(&self, id: &str) -> Result<Role, StoreError>;
    /// Creates a role. A taken name is [`StoreError::RoleNameTaken`].
    async fn create_role(&self, name: &str, permissions: &[String]) -> Result<Role, StoreError>;
    /// Rewrites a role's name and permissions.
    async fn update_role(
        &self,
        id: &str,
        name: &str,
        permissions: &[String],
    ) -> Result<Role, StoreError>;
    /// Deletes a role and its grants.
    async fn delete_role(&self, id: &str) -> Result<(), StoreError>;
    /// Grants the user the role; a role already held stays granted. A user
    /// or role that does not exist is [`StoreError::NotFound`].
    async fn grant_role(
        &self,
        user_id: &str,
        role_id: &str,
        at: SystemTime,
    ) -> Result<(), StoreError>;
    /// Revokes the role from the user; one not held stays revoked. A user
    /// or role that does not exist is [`StoreError::NotFound`].
    async fn revoke_role(&self, user_id: &str, role_id: &str) -> Result<(), StoreError>;
}
