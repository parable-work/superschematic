//! The identity store over SQL, built from a schema's identity descriptor,
//! for Postgres and SQLite: the Rust port of the Go runtime's `SQLStore`,
//! statement for statement, so a database one writes the other reads.
//!
//! It quotes every name the descriptor gives, finds a login by equality
//! after the login scalar parses it, and leaves the case rule to the column
//! (`CITEXT`, `TEXT COLLATE NOCASE`). Keys are in their scalar's wire form
//! outside the store: an `Identity.UUID` key is its base62 form, as the
//! generated types write it, and hyphenated in the database. The store
//! deletes a role's grants itself, so it does not rely on the cascade.

mod client;

use std::sync::Arc;
use std::time::SystemTime;

use async_trait::async_trait;

#[cfg(feature = "identity-sqlite")]
pub use client::Rusqlite;
#[cfg(feature = "identity-postgres")]
pub use client::TokioPostgres;
pub use client::{Client, ClientError, Conn, Dialect, SqlValue};

use super::descriptor::{quote, Descriptor};
use super::scalars::{KeyCodec, Scalars};
use super::store::{
    LoginRecord, NewSession, NewUser, Role, Session, SessionRecord, Store, StoreError, User,
};
use super::time::{format_sqlite, parse_timestamp};

/// The key of every table the loader adds, `AutoGenerate<Identity.UUID>`.
const SESSION_KEY_SCALAR: &str = "Identity.UUID";

/// The [`Store`] over a SQL [`Client`].
pub struct SqlStore {
    client: Box<dyn Client>,
    dialect: Dialect,
    descriptor: Descriptor,
    scalars: Arc<dyn Scalars>,
    user_key: KeyCodec,
    role_key: Option<KeyCodec>,
    session_key: KeyCodec,
    name_is_login: bool,
    t: Names,
}

/// The quoted table and column names the statements use.
#[derive(Default)]
struct Names {
    user: String,
    user_key: String,
    user_login: String,
    user_name: String,
    session: String,
    session_id: String,
    session_user: String,
    token_hash: String,
    created_at: String,
    expires_at: String,
    last_seen_at: String,
    revoked_at: String,
    credential: String,
    credential_user: String,
    password_hash: String,
    password_changed_at: String,
    disabled: String,
    role: String,
    role_key: String,
    role_name: String,
    role_permissions: String,
    grant: String,
    grant_user: String,
    grant_role: String,
    granted_at: String,
}

fn db(err: ClientError) -> StoreError {
    StoreError::Database(Box::new(err))
}

/// A statement runner: the client on its own, or a transaction.
#[async_trait]
trait Run: Send {
    async fn rows(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, StoreError>;
    async fn run(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, StoreError>;
}

struct Direct<'a>(&'a dyn Client);

#[async_trait]
impl Run for Direct<'_> {
    async fn rows(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, StoreError> {
        self.0.query(sql, args).await.map_err(db)
    }

    async fn run(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, StoreError> {
        self.0.execute(sql, args).await.map_err(db)
    }
}

#[async_trait]
impl Run for Box<dyn Conn + '_> {
    async fn rows(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, StoreError> {
        self.query(sql, args).await.map_err(db)
    }

    async fn run(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, StoreError> {
        self.execute(sql, args).await.map_err(db)
    }
}

/// Ends a transaction: commits it when `result` is a success, and rolls it
/// back otherwise.
async fn finish<T>(tx: Box<dyn Conn + '_>, result: Result<T, StoreError>) -> Result<T, StoreError> {
    match result {
        Ok(value) => {
            tx.commit().await.map_err(db)?;
            Ok(value)
        }
        Err(err) => {
            if let Err(rollback) = tx.rollback().await {
                tracing::warn!(error = %rollback, "identity: roll back a transaction");
            }
            Err(err)
        }
    }
}

/// A text column.
fn text(row: &[SqlValue], i: usize) -> Result<String, StoreError> {
    match row.get(i) {
        Some(SqlValue::Text(s)) => Ok(s.clone()),
        Some(SqlValue::Int(n)) => Ok(n.to_string()),
        other => Err(StoreError::database(format!(
            "column {i} is not text: {other:?}"
        ))),
    }
}

/// A nullable text column.
fn optional_text(row: &[SqlValue], i: usize) -> Result<Option<String>, StoreError> {
    match row.get(i) {
        Some(SqlValue::Null) => Ok(None),
        _ => text(row, i).map(Some),
    }
}

/// A nullable timestamp column: a time, or text in RFC 3339 or SQLite's
/// layout.
fn optional_time(row: &[SqlValue], i: usize) -> Result<Option<SystemTime>, StoreError> {
    match row.get(i) {
        Some(SqlValue::Null) => Ok(None),
        Some(SqlValue::Time(t)) => Ok(Some(*t)),
        Some(SqlValue::Text(s)) => parse_timestamp(s)
            .map(Some)
            .ok_or_else(|| StoreError::database(format!("cannot read {s:?} as a timestamp"))),
        other => Err(StoreError::database(format!(
            "column {i} is not a timestamp: {other:?}"
        ))),
    }
}

fn time(row: &[SqlValue], i: usize) -> Result<SystemTime, StoreError> {
    optional_time(row, i)?
        .ok_or_else(|| StoreError::database(format!("column {i} is null, not a timestamp")))
}

/// A role's permissions: a Postgres `TEXT[]`, or a JSON array in text.
fn permissions(row: &[SqlValue], i: usize, role: &str) -> Result<Vec<String>, StoreError> {
    match row.get(i) {
        Some(SqlValue::Null) => Ok(Vec::new()),
        Some(SqlValue::TextList(list)) => Ok(list.clone()),
        Some(SqlValue::Text(s)) if s.is_empty() => Ok(Vec::new()),
        Some(SqlValue::Text(s)) => serde_json::from_str::<Option<Vec<String>>>(s)
            .map(Option::unwrap_or_default)
            .map_err(|err| StoreError::database(format!("read role {role}'s permissions: {err}"))),
        other => Err(StoreError::database(format!(
            "role {role}'s permissions are not a list: {other:?}"
        ))),
    }
}

/// Orders roles by name, in byte order, the same in every dialect and
/// collation.
fn sort_roles(roles: &mut [Role]) {
    roles.sort_by(|a, b| a.name.as_bytes().cmp(b.name.as_bytes()));
}

impl SqlStore {
    /// Builds a store over `client` from the identity descriptor's JSON
    /// (the generated types' `IDENTITY_DESCRIPTOR`). `scalars` parses logins,
    /// display names and keys; the login scalar must be one it has.
    pub fn new(
        client: impl Client,
        descriptor: &str,
        scalars: Arc<dyn Scalars>,
    ) -> Result<SqlStore, StoreError> {
        let descriptor = Descriptor::parse(descriptor)?;
        let login_scalar = &descriptor.user.login_scalar;
        if scalars.parse(login_scalar, "").is_none() {
            return Err(StoreError::Descriptor(format!(
                "the login scalar {login_scalar:?} is not one the scalar catalog has"
            )));
        }
        let (u, s, c) = (
            &descriptor.user,
            &descriptor.session,
            &descriptor.credential,
        );
        let mut t = Names {
            user: quote(&u.table),
            user_key: quote(&u.columns.key),
            user_login: quote(&u.columns.login),
            user_name: quote(&u.columns.name),
            session: quote(&s.table),
            session_id: quote(&s.columns.id),
            session_user: quote(&s.columns.user),
            token_hash: quote(&s.columns.token_hash),
            created_at: quote(&s.columns.created_at),
            expires_at: quote(&s.columns.expires_at),
            last_seen_at: quote(&s.columns.last_seen_at),
            revoked_at: quote(&s.columns.revoked_at),
            credential: quote(&c.table),
            credential_user: quote(&c.columns.user),
            password_hash: quote(&c.columns.password_hash),
            password_changed_at: quote(&c.columns.password_changed_at),
            disabled: quote(&c.columns.disabled_at),
            ..Names::default()
        };
        if let (Some(r), Some(g)) = (&descriptor.role, &descriptor.role_grant) {
            t.role = quote(&r.table);
            t.role_key = quote(&r.columns.key);
            t.role_name = quote(&r.columns.name);
            t.role_permissions = quote(&r.columns.permissions);
            t.grant = quote(&g.table);
            t.grant_user = quote(&g.columns.user);
            t.grant_role = quote(&g.columns.role);
            t.granted_at = quote(&g.columns.granted_at);
        }
        Ok(SqlStore {
            dialect: client.dialect(),
            client: Box::new(client),
            user_key: KeyCodec::new(&u.key_scalar, scalars.as_ref()),
            role_key: descriptor
                .role
                .as_ref()
                .map(|r| KeyCodec::new(&r.key_scalar, scalars.as_ref())),
            session_key: KeyCodec::new(SESSION_KEY_SCALAR, scalars.as_ref()),
            name_is_login: u.columns.name == u.columns.login,
            scalars,
            t,
            descriptor,
        })
    }

    /// The descriptor the store was built from.
    pub fn descriptor(&self) -> &Descriptor {
        &self.descriptor
    }

    /// The client the store runs its statements through.
    pub fn client(&self) -> &dyn Client {
        self.client.as_ref()
    }

    fn direct(&self) -> Direct<'_> {
        Direct(self.client.as_ref())
    }

    async fn begin(&self) -> Result<Box<dyn Conn + '_>, StoreError> {
        self.client.begin().await.map_err(db)
    }

    /// A statement written with `?` placeholders in the dialect's own:
    /// `$1`, `$2`, ... on Postgres, leaving quoted names and literals alone.
    fn sql(&self, query: &str) -> String {
        if self.dialect != Dialect::Postgres {
            return query.to_owned();
        }
        let mut out = String::with_capacity(query.len() + 8);
        let mut n = 0;
        let mut quoted: Option<char> = None;
        for c in query.chars() {
            match quoted {
                Some(q) if c == q => quoted = None,
                Some(_) => {}
                None if c == '"' || c == '\'' => quoted = Some(c),
                None if c == '?' => {
                    n += 1;
                    out.push_str(&format!("${n}"));
                    continue;
                }
                None => {}
            }
            out.push(c);
        }
        out
    }

    /// A timestamp as the dialect stores it.
    fn time_arg(&self, t: SystemTime) -> SqlValue {
        match self.dialect {
            Dialect::Postgres => SqlValue::Time(t),
            Dialect::Sqlite => SqlValue::Text(format_sqlite(t)),
        }
    }

    /// A role's permissions as the dialect stores them.
    fn permissions_arg(&self, permissions: &[String]) -> SqlValue {
        match self.dialect {
            Dialect::Postgres => SqlValue::TextList(permissions.to_vec()),
            Dialect::Sqlite => SqlValue::Text(
                serde_json::to_string(permissions).unwrap_or_else(|_| "[]".to_owned()),
            ),
        }
    }

    fn user_key(&self, id: &str) -> Result<String, StoreError> {
        self.user_key
            .to_db(id, self.scalars.as_ref())
            .ok_or(StoreError::NotFound)
    }

    fn session_key(&self, id: &str) -> Result<String, StoreError> {
        self.session_key
            .to_db(id, self.scalars.as_ref())
            .ok_or(StoreError::NotFound)
    }

    fn role_codec(&self) -> Result<&KeyCodec, StoreError> {
        self.role_key.as_ref().ok_or(StoreError::NoRoles)
    }

    fn role_key(&self, id: &str) -> Result<String, StoreError> {
        self.role_codec()?
            .to_db(id, self.scalars.as_ref())
            .ok_or(StoreError::NotFound)
    }

    /// The login scalar's parse of `login`.
    fn parse_login(&self, login: &str) -> Result<String, StoreError> {
        let scalar = &self.descriptor.user.login_scalar;
        match self.scalars.parse(scalar, login) {
            Some(Ok(parsed)) => Ok(parsed),
            Some(Err(reason)) => Err(StoreError::InvalidLogin {
                scalar: scalar.clone(),
                reason,
            }),
            None => Err(StoreError::Descriptor(format!(
                "the login scalar {scalar:?} is not one the scalar catalog has"
            ))),
        }
    }

    /// Whether a row of `table` has `key` in `column`.
    async fn exists(
        &self,
        q: &mut dyn Run,
        table: &str,
        column: &str,
        key: &str,
    ) -> Result<bool, StoreError> {
        let rows = q
            .rows(
                &self.sql(&format!("SELECT 1 FROM {table} WHERE {column} = ?")),
                &[key.into()],
            )
            .await?;
        Ok(!rows.is_empty())
    }

    async fn require(
        &self,
        q: &mut dyn Run,
        table: &str,
        column: &str,
        key: &str,
    ) -> Result<(), StoreError> {
        if self.exists(q, table, column, key).await? {
            Ok(())
        } else {
            Err(StoreError::NotFound)
        }
    }

    /// Selects a user's key, login, name, `disabledAt` and password hash,
    /// the credential joined when there is one.
    fn user_select(&self) -> String {
        let t = &self.t;
        format!(
            "SELECT u.{}, u.{}, u.{}, c.{}, c.{} FROM {} u LEFT JOIN {} c ON c.{} = u.{}",
            t.user_key,
            t.user_login,
            t.user_name,
            t.disabled,
            t.password_hash,
            t.user,
            t.credential,
            t.credential_user,
            t.user_key
        )
    }

    fn scan_user(&self, row: &[SqlValue]) -> Result<LoginRecord, StoreError> {
        Ok(LoginRecord {
            user: User {
                id: self.user_key.to_wire(&text(row, 0)?),
                login: text(row, 1)?,
                name: text(row, 2)?,
                disabled: optional_time(row, 3)?.is_some(),
                roles: Vec::new(),
            },
            password_hash: optional_text(row, 4)?.unwrap_or_default(),
        })
    }

    async fn find_user(&self, column: &str, arg: String) -> Result<LoginRecord, StoreError> {
        let query = self.sql(&format!("{} WHERE u.{column} = ?", self.user_select()));
        let rows = self.direct().rows(&query, &[arg.into()]).await?;
        let row = rows.first().ok_or(StoreError::NotFound)?;
        self.scan_user(row)
    }

    fn role_select(&self) -> String {
        let t = &self.t;
        format!(
            "SELECT r.{}, r.{}, r.{} FROM {} r",
            t.role_key, t.role_name, t.role_permissions, t.role
        )
    }

    /// Reads roles from rows of key, name and permissions, after the
    /// holder's key when `with_user`.
    fn scan_roles(
        &self,
        rows: &[Vec<SqlValue>],
        with_user: bool,
    ) -> Result<Vec<(String, Role)>, StoreError> {
        let codec = self.role_codec()?;
        let offset = usize::from(with_user);
        rows.iter()
            .map(|row| {
                let user = if with_user {
                    self.user_key.to_wire(&text(row, 0)?)
                } else {
                    String::new()
                };
                let name = text(row, offset + 1)?;
                let role = Role {
                    id: codec.to_wire(&text(row, offset)?),
                    permissions: permissions(row, offset + 2, &name)?,
                    name,
                };
                Ok((user, role))
            })
            .collect()
    }

    async fn get_role_in(&self, q: &mut dyn Run, key: &str) -> Result<Role, StoreError> {
        let query = self.sql(&format!(
            "{} WHERE r.{} = ?",
            self.role_select(),
            self.t.role_key
        ));
        let rows = q.rows(&query, &[key.into()]).await?;
        self.scan_roles(&rows, false)?
            .into_iter()
            .next()
            .map(|(_, role)| role)
            .ok_or(StoreError::NotFound)
    }

    /// Revokes the live sessions of the user with the database key `key`,
    /// but the session with the database key `keep`.
    async fn revoke_user_sessions_in(
        &self,
        q: &mut dyn Run,
        key: &str,
        keep: Option<&str>,
        at: SystemTime,
    ) -> Result<(), StoreError> {
        let t = &self.t;
        let mut query = format!(
            "UPDATE {} SET {} = ? WHERE {} = ? AND {} IS NULL",
            t.session, t.revoked_at, t.session_user, t.revoked_at
        );
        let mut args = vec![self.time_arg(at), key.into()];
        if let Some(keep) = keep {
            query.push_str(&format!(" AND {} <> ?", t.session_id));
            args.push(keep.into());
        }
        q.run(&self.sql(&query), &args).await.map(drop)
    }

    async fn create_user_in(
        &self,
        q: &mut dyn Run,
        login: &str,
        name: &str,
        password_hash: &str,
        at: SystemTime,
    ) -> Result<String, StoreError> {
        let t = &self.t;
        let (columns, values, mut args) = if self.name_is_login {
            (t.user_login.clone(), "?", vec![SqlValue::from(login)])
        } else {
            (
                format!("{}, {}", t.user_login, t.user_name),
                "?, ?",
                vec![login.into(), name.into()],
            )
        };
        let query = format!(
            "INSERT INTO {} ({columns}) VALUES ({values}) ON CONFLICT ({}) DO NOTHING RETURNING {}",
            t.user, t.user_login, t.user_key
        );
        let rows = q.rows(&self.sql(&query), &args).await?;
        let key = match rows.first() {
            Some(row) => text(row, 0)?,
            None => return Err(StoreError::LoginTaken),
        };
        args = vec![key.as_str().into(), password_hash.into(), self.time_arg(at)];
        let query = format!(
            "INSERT INTO {} ({}, {}, {}) VALUES (?, ?, ?)",
            t.credential, t.credential_user, t.password_hash, t.password_changed_at
        );
        q.run(&self.sql(&query), &args).await?;
        Ok(key)
    }

    async fn set_password_in(
        &self,
        q: &mut dyn Run,
        key: &str,
        password_hash: &str,
        at: SystemTime,
        keep: Option<&str>,
    ) -> Result<(), StoreError> {
        let t = &self.t;
        self.require(q, &t.user, &t.user_key, key).await?;
        let query = format!(
            "INSERT INTO {} ({}, {}, {}) VALUES (?, ?, ?) ON CONFLICT ({}) DO UPDATE SET {} = excluded.{}, {} = excluded.{}",
            t.credential,
            t.credential_user,
            t.password_hash,
            t.password_changed_at,
            t.credential_user,
            t.password_hash,
            t.password_hash,
            t.password_changed_at,
            t.password_changed_at
        );
        q.run(
            &self.sql(&query),
            &[key.into(), password_hash.into(), self.time_arg(at)],
        )
        .await?;
        self.revoke_user_sessions_in(q, key, keep, at).await
    }

    async fn set_disabled_in(
        &self,
        q: &mut dyn Run,
        key: &str,
        disabled: bool,
        at: SystemTime,
    ) -> Result<(), StoreError> {
        let t = &self.t;
        self.require(q, &t.user, &t.user_key, key).await?;
        if !disabled {
            let query = format!(
                "UPDATE {} SET {} = NULL WHERE {} = ?",
                t.credential, t.disabled, t.credential_user
            );
            return q.run(&self.sql(&query), &[key.into()]).await.map(drop);
        }
        // A user without a credential gets one with no password, which
        // matches none, so the user still reads as disabled.
        let query = format!(
            "INSERT INTO {} ({}, {}, {}, {}) VALUES (?, '', ?, ?) ON CONFLICT ({}) DO UPDATE SET {} = excluded.{}",
            t.credential,
            t.credential_user,
            t.password_hash,
            t.password_changed_at,
            t.disabled,
            t.credential_user,
            t.disabled,
            t.disabled
        );
        q.run(
            &self.sql(&query),
            &[key.into(), self.time_arg(at), self.time_arg(at)],
        )
        .await?;
        self.revoke_user_sessions_in(q, key, None, at).await
    }

    async fn update_role_in(
        &self,
        q: &mut dyn Run,
        key: &str,
        name: &str,
        permissions: &[String],
    ) -> Result<Role, StoreError> {
        let t = &self.t;
        self.require(q, &t.role, &t.role_key, key).await?;
        let taken = format!(
            "SELECT 1 FROM {} WHERE {} = ? AND {} <> ?",
            t.role, t.role_name, t.role_key
        );
        if !q
            .rows(&self.sql(&taken), &[name.into(), key.into()])
            .await?
            .is_empty()
        {
            return Err(StoreError::RoleNameTaken);
        }
        let query = format!(
            "UPDATE {} SET {} = ?, {} = ? WHERE {} = ?",
            t.role, t.role_name, t.role_permissions, t.role_key
        );
        match q
            .run(
                &self.sql(&query),
                &[name.into(), self.permissions_arg(permissions), key.into()],
            )
            .await
        {
            Err(StoreError::Database(err)) if is_unique_violation(err.as_ref()) => {
                return Err(StoreError::RoleNameTaken)
            }
            other => other?,
        };
        self.get_role_in(q, key).await
    }

    async fn delete_role_in(&self, q: &mut dyn Run, key: &str) -> Result<(), StoreError> {
        let t = &self.t;
        let grants = format!("DELETE FROM {} WHERE {} = ?", t.grant, t.grant_role);
        q.run(&self.sql(&grants), &[key.into()]).await?;
        let role = format!("DELETE FROM {} WHERE {} = ?", t.role, t.role_key);
        if q.run(&self.sql(&role), &[key.into()]).await? == 0 {
            return Err(StoreError::NotFound);
        }
        Ok(())
    }

    /// Finds the user and the role, then grants or revokes, in one
    /// transaction.
    async fn change_grant(
        &self,
        user_id: &str,
        role_id: &str,
        grant: Option<SystemTime>,
    ) -> Result<(), StoreError> {
        let user = self.user_key(user_id)?;
        let role = self.role_key(role_id)?;
        let mut tx = self.begin().await?;
        let result = self.change_grant_in(&mut tx, &user, &role, grant).await;
        finish(tx, result).await
    }

    async fn change_grant_in(
        &self,
        q: &mut dyn Run,
        user: &str,
        role: &str,
        grant: Option<SystemTime>,
    ) -> Result<(), StoreError> {
        let t = &self.t;
        self.require(q, &t.user, &t.user_key, user).await?;
        self.require(q, &t.role, &t.role_key, role).await?;
        match grant {
            Some(at) => {
                let query = format!(
                    "INSERT INTO {} ({}, {}, {}) VALUES (?, ?, ?) ON CONFLICT ({}, {}) DO NOTHING",
                    t.grant, t.grant_user, t.grant_role, t.granted_at, t.grant_user, t.grant_role
                );
                q.run(
                    &self.sql(&query),
                    &[user.into(), role.into(), self.time_arg(at)],
                )
                .await?;
            }
            None => {
                let query = format!(
                    "DELETE FROM {} WHERE {} = ? AND {} = ?",
                    t.grant, t.grant_user, t.grant_role
                );
                q.run(&self.sql(&query), &[user.into(), role.into()])
                    .await?;
            }
        }
        Ok(())
    }
}

fn is_unique_violation(err: &(dyn std::error::Error + Send + Sync + 'static)) -> bool {
    err.downcast_ref::<ClientError>()
        .is_some_and(ClientError::is_unique_violation)
}

#[async_trait]
impl Store for SqlStore {
    async fn find_login(&self, login: &str) -> Result<LoginRecord, StoreError> {
        let parsed = self.parse_login(login)?;
        self.find_user(&self.t.user_login, parsed).await
    }

    async fn find_credential(&self, user_id: &str) -> Result<LoginRecord, StoreError> {
        let key = self.user_key(user_id)?;
        self.find_user(&self.t.user_key, key).await
    }

    async fn get_user(&self, id: &str) -> Result<User, StoreError> {
        let key = self.user_key(id)?;
        let mut user = self.find_user(&self.t.user_key, key).await?.user;
        user.roles = self.user_roles(&user.id).await?;
        Ok(user)
    }

    async fn list_users(&self) -> Result<Vec<User>, StoreError> {
        let rows = self.direct().rows(&self.user_select(), &[]).await?;
        let mut users = rows
            .iter()
            .map(|row| self.scan_user(row).map(|rec| rec.user))
            .collect::<Result<Vec<_>, _>>()?;
        if self.has_roles() {
            let t = &self.t;
            let query = format!(
                "SELECT g.{}, r.{}, r.{}, r.{} FROM {} g JOIN {} r ON r.{} = g.{}",
                t.grant_user,
                t.role_key,
                t.role_name,
                t.role_permissions,
                t.grant,
                t.role,
                t.role_key,
                t.grant_role
            );
            let rows = self.direct().rows(&query, &[]).await?;
            let grants = self.scan_roles(&rows, true)?;
            for user in &mut users {
                user.roles = grants
                    .iter()
                    .filter(|(holder, _)| *holder == user.id)
                    .map(|(_, role)| role.clone())
                    .collect();
                sort_roles(&mut user.roles);
            }
        }
        users.sort_by(|a, b| a.login.as_bytes().cmp(b.login.as_bytes()));
        Ok(users)
    }

    async fn create_user(&self, new: NewUser) -> Result<User, StoreError> {
        let login = self.parse_login(&new.login)?;
        let name = if new.name.is_empty() || self.name_is_login {
            login.clone()
        } else {
            let scalar = &self.descriptor.user.name_scalar;
            match self.scalars.parse(scalar, &new.name) {
                // The name column has its scalar's bounds; a name outside
                // them is the caller's, not a failed write.
                Some(Err(reason)) => {
                    return Err(StoreError::InvalidName {
                        scalar: scalar.clone(),
                        reason,
                    })
                }
                Some(Ok(parsed)) => parsed,
                None => new.name.clone(),
            }
        };
        let mut tx = self.begin().await?;
        let result = self
            .create_user_in(&mut tx, &login, &name, &new.password_hash, new.at)
            .await;
        let key = finish(tx, result).await?;
        Ok(User {
            id: self.user_key.to_wire(&key),
            login,
            name,
            disabled: false,
            roles: Vec::new(),
        })
    }

    async fn set_password(
        &self,
        user_id: &str,
        password_hash: &str,
        at: SystemTime,
        keep_session: Option<&str>,
    ) -> Result<(), StoreError> {
        let key = self.user_key(user_id)?;
        let keep = keep_session.map(|id| self.session_key(id)).transpose()?;
        let mut tx = self.begin().await?;
        let result = self
            .set_password_in(&mut tx, &key, password_hash, at, keep.as_deref())
            .await;
        finish(tx, result).await
    }

    async fn rehash_password(
        &self,
        user_id: &str,
        old_hash: &str,
        new_hash: &str,
    ) -> Result<(), StoreError> {
        let key = self.user_key(user_id)?;
        let t = &self.t;
        let query = format!(
            "UPDATE {} SET {} = ? WHERE {} = ? AND {} = ?",
            t.credential, t.password_hash, t.credential_user, t.password_hash
        );
        self.direct()
            .run(
                &self.sql(&query),
                &[new_hash.into(), key.into(), old_hash.into()],
            )
            .await
            .map(drop)
    }

    async fn set_disabled(
        &self,
        user_id: &str,
        disabled: bool,
        at: SystemTime,
    ) -> Result<(), StoreError> {
        let key = self.user_key(user_id)?;
        let mut tx = self.begin().await?;
        let result = self.set_disabled_in(&mut tx, &key, disabled, at).await;
        finish(tx, result).await
    }

    async fn create_session(&self, new: NewSession) -> Result<Session, StoreError> {
        let key = self.user_key(&new.user_id)?;
        let t = &self.t;
        let query = format!(
            "INSERT INTO {} ({}, {}, {}, {}) VALUES (?, ?, ?, ?) RETURNING {}",
            t.session, t.session_user, t.token_hash, t.created_at, t.expires_at, t.session_id
        );
        let rows = self
            .direct()
            .rows(
                &self.sql(&query),
                &[
                    key.into(),
                    new.token_hash.as_str().into(),
                    self.time_arg(new.created_at),
                    self.time_arg(new.expires_at),
                ],
            )
            .await?;
        let id = rows
            .first()
            .map(|row| text(row, 0))
            .transpose()?
            .ok_or_else(|| StoreError::database("creating a session returned no key"))?;
        Ok(Session {
            id: self.session_key.to_wire(&id),
            user_id: new.user_id,
            created_at: new.created_at,
            expires_at: new.expires_at,
            last_seen_at: None,
            revoked_at: None,
        })
    }

    async fn find_session(&self, token_hash: &str) -> Result<SessionRecord, StoreError> {
        let t = &self.t;
        let query = format!(
            "SELECT s.{}, s.{}, s.{}, s.{}, s.{}, s.{}, u.{}, u.{}, c.{} FROM {} s JOIN {} u ON u.{} = s.{} LEFT JOIN {} c ON c.{} = s.{} WHERE s.{} = ?",
            t.session_id,
            t.session_user,
            t.created_at,
            t.expires_at,
            t.last_seen_at,
            t.revoked_at,
            t.user_login,
            t.user_name,
            t.disabled,
            t.session,
            t.user,
            t.user_key,
            t.session_user,
            t.credential,
            t.credential_user,
            t.session_user,
            t.token_hash
        );
        let rows = self
            .direct()
            .rows(&self.sql(&query), &[token_hash.into()])
            .await?;
        let row = rows.first().ok_or(StoreError::NotFound)?;
        let user_id = self.user_key.to_wire(&text(row, 1)?);
        Ok(SessionRecord {
            session: Session {
                id: self.session_key.to_wire(&text(row, 0)?),
                user_id: user_id.clone(),
                created_at: time(row, 2)?,
                expires_at: time(row, 3)?,
                last_seen_at: optional_time(row, 4)?,
                revoked_at: optional_time(row, 5)?,
            },
            user: User {
                id: user_id,
                login: text(row, 6)?,
                name: text(row, 7)?,
                disabled: optional_time(row, 8)?.is_some(),
                roles: Vec::new(),
            },
        })
    }

    async fn touch_session(
        &self,
        session_id: &str,
        at: SystemTime,
        stale_before: SystemTime,
    ) -> Result<(), StoreError> {
        let id = self.session_key(session_id)?;
        let t = &self.t;
        let query = format!(
            "UPDATE {} SET {} = ? WHERE {} = ? AND ({} IS NULL OR {} < ?)",
            t.session, t.last_seen_at, t.session_id, t.last_seen_at, t.last_seen_at
        );
        self.direct()
            .run(
                &self.sql(&query),
                &[self.time_arg(at), id.into(), self.time_arg(stale_before)],
            )
            .await
            .map(drop)
    }

    async fn revoke_session(&self, session_id: &str, at: SystemTime) -> Result<(), StoreError> {
        let id = self.session_key(session_id)?;
        let t = &self.t;
        let query = format!(
            "UPDATE {} SET {} = ? WHERE {} = ? AND {} IS NULL",
            t.session, t.revoked_at, t.session_id, t.revoked_at
        );
        self.direct()
            .run(&self.sql(&query), &[self.time_arg(at), id.into()])
            .await
            .map(drop)
    }

    async fn revoke_user_sessions(
        &self,
        user_id: &str,
        except_session: Option<&str>,
        at: SystemTime,
    ) -> Result<(), StoreError> {
        let key = self.user_key(user_id)?;
        let keep = except_session.map(|id| self.session_key(id)).transpose()?;
        self.revoke_user_sessions_in(&mut self.direct(), &key, keep.as_deref(), at)
            .await
    }

    fn has_roles(&self) -> bool {
        self.descriptor.role.is_some()
    }

    async fn user_roles(&self, user_id: &str) -> Result<Vec<Role>, StoreError> {
        if !self.has_roles() {
            return Ok(Vec::new());
        }
        let key = self.user_key(user_id)?;
        let t = &self.t;
        let query = format!(
            "SELECT r.{}, r.{}, r.{} FROM {} g JOIN {} r ON r.{} = g.{} WHERE g.{} = ?",
            t.role_key,
            t.role_name,
            t.role_permissions,
            t.grant,
            t.role,
            t.role_key,
            t.grant_role,
            t.grant_user
        );
        let rows = self.direct().rows(&self.sql(&query), &[key.into()]).await?;
        let mut roles: Vec<Role> = self
            .scan_roles(&rows, false)?
            .into_iter()
            .map(|(_, role)| role)
            .collect();
        sort_roles(&mut roles);
        Ok(roles)
    }

    async fn list_roles(&self) -> Result<Vec<Role>, StoreError> {
        self.role_codec()?;
        let rows = self.direct().rows(&self.role_select(), &[]).await?;
        let mut roles: Vec<Role> = self
            .scan_roles(&rows, false)?
            .into_iter()
            .map(|(_, role)| role)
            .collect();
        sort_roles(&mut roles);
        Ok(roles)
    }

    async fn get_role(&self, id: &str) -> Result<Role, StoreError> {
        let key = self.role_key(id)?;
        self.get_role_in(&mut self.direct(), &key).await
    }

    async fn create_role(&self, name: &str, permissions: &[String]) -> Result<Role, StoreError> {
        let codec = self.role_codec()?;
        let t = &self.t;
        let query = format!(
            "INSERT INTO {} ({}, {}) VALUES (?, ?) ON CONFLICT ({}) DO NOTHING RETURNING {}",
            t.role, t.role_name, t.role_permissions, t.role_name, t.role_key
        );
        let rows = self
            .direct()
            .rows(
                &self.sql(&query),
                &[name.into(), self.permissions_arg(permissions)],
            )
            .await?;
        let key = match rows.first() {
            Some(row) => text(row, 0)?,
            None => return Err(StoreError::RoleNameTaken),
        };
        Ok(Role {
            id: codec.to_wire(&key),
            name: name.to_owned(),
            permissions: permissions.to_vec(),
        })
    }

    async fn update_role(
        &self,
        id: &str,
        name: &str,
        permissions: &[String],
    ) -> Result<Role, StoreError> {
        let key = self.role_key(id)?;
        let mut tx = self.begin().await?;
        let result = self.update_role_in(&mut tx, &key, name, permissions).await;
        finish(tx, result).await
    }

    async fn delete_role(&self, id: &str) -> Result<(), StoreError> {
        let key = self.role_key(id)?;
        let mut tx = self.begin().await?;
        let result = self.delete_role_in(&mut tx, &key).await;
        finish(tx, result).await
    }

    async fn grant_role(
        &self,
        user_id: &str,
        role_id: &str,
        at: SystemTime,
    ) -> Result<(), StoreError> {
        self.change_grant(user_id, role_id, Some(at)).await
    }

    async fn revoke_role(&self, user_id: &str, role_id: &str) -> Result<(), StoreError> {
        self.change_grant(user_id, role_id, None).await
    }
}
