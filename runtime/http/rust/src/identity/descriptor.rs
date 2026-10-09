//! The identity descriptor a store is built from: the tables that hold a
//! schema's users, sessions, credentials, roles and grants, and which
//! column plays which part. `internal/generator/identitydesc/README.md` is
//! its contract; the generated Rust types hold it as `IDENTITY_DESCRIPTOR`.

use serde::Deserialize;

use super::store::StoreError;

/// The descriptor format a store reads.
pub const DESCRIPTOR_VERSION: u32 = 1;

/// A schema's identity descriptor.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Descriptor {
    pub version: u32,
    pub user: DescriptorUser,
    pub session: DescriptorSession,
    pub credential: DescriptorCredential,
    /// Absent when the schema has no `UserRole` table, and `role_grant`
    /// with it.
    #[serde(default)]
    pub role: Option<DescriptorRole>,
    #[serde(default, rename = "roleGrant")]
    pub role_grant: Option<DescriptorRoleGrant>,
}

/// The table with the `User` trait.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct DescriptorUser {
    #[serde(rename = "type")]
    pub type_name: String,
    pub table: String,
    pub columns: UserColumns,
    /// The key field's type: a scalar's canonical name, such as
    /// `Identity.UUID`, or a builtin type.
    pub key_scalar: String,
    /// The login field's scalar, such as `Contact.Email`.
    pub login_scalar: String,
    /// The name field's type, such as `Identity.Name` or `string`.
    pub name_scalar: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UserColumns {
    pub key: String,
    pub login: String,
    pub name: String,
}

/// The `Session` table the loader adds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DescriptorSession {
    pub table: String,
    pub columns: SessionColumns,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionColumns {
    pub id: String,
    pub user: String,
    pub token_hash: String,
    pub created_at: String,
    pub expires_at: String,
    pub last_seen_at: String,
    pub revoked_at: String,
}

/// The `UserCredential` table the loader adds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DescriptorCredential {
    pub table: String,
    pub columns: CredentialColumns,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct CredentialColumns {
    pub id: String,
    pub user: String,
    pub password_hash: String,
    pub password_changed_at: String,
    pub disabled_at: String,
}

/// The table with the `UserRole` trait.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct DescriptorRole {
    #[serde(rename = "type")]
    pub type_name: String,
    pub table: String,
    pub columns: RoleColumns,
    /// The role table's key field's type, as `user.keyScalar` is the user
    /// table's.
    pub key_scalar: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RoleColumns {
    pub key: String,
    pub name: String,
    pub permissions: String,
}

/// The `UserRoleGrant` table the loader adds beside a `UserRole` table.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct DescriptorRoleGrant {
    pub table: String,
    pub columns: RoleGrantColumns,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct RoleGrantColumns {
    pub id: String,
    pub user: String,
    pub role: String,
    pub granted_at: String,
}

impl Descriptor {
    /// Reads an identity descriptor. It refuses any version but 1, unknown
    /// members, an empty name, a role table without its grant table or the
    /// other way round, and a name holding a NUL, which no quoting carries.
    pub fn parse(json: &str) -> Result<Descriptor, StoreError> {
        let descriptor: Descriptor =
            serde_json::from_str(json).map_err(|err| StoreError::Descriptor(err.to_string()))?;
        if descriptor.version != DESCRIPTOR_VERSION {
            return Err(StoreError::Descriptor(format!(
                "version {}, want {DESCRIPTOR_VERSION}",
                descriptor.version
            )));
        }
        if descriptor.role.is_some() != descriptor.role_grant.is_some() {
            return Err(StoreError::Descriptor(
                "role and roleGrant are present together or not at all".to_owned(),
            ));
        }
        let mut names = descriptor.names();
        names.sort_by_key(|(member, _)| *member);
        for (member, name) in names {
            if name.is_empty() {
                return Err(StoreError::Descriptor(format!("{member} is empty")));
            }
            if name.contains('\0') {
                return Err(StoreError::Descriptor(format!("{member} holds a NUL")));
            }
        }
        Ok(descriptor)
    }

    fn names(&self) -> Vec<(&'static str, &str)> {
        let (u, s, c) = (&self.user, &self.session, &self.credential);
        let mut names = vec![
            ("user.type", u.type_name.as_str()),
            ("user.table", &u.table),
            ("user.columns.key", &u.columns.key),
            ("user.columns.login", &u.columns.login),
            ("user.columns.name", &u.columns.name),
            ("user.keyScalar", &u.key_scalar),
            ("user.loginScalar", &u.login_scalar),
            ("user.nameScalar", &u.name_scalar),
            ("session.table", &s.table),
            ("session.columns.id", &s.columns.id),
            ("session.columns.user", &s.columns.user),
            ("session.columns.tokenHash", &s.columns.token_hash),
            ("session.columns.createdAt", &s.columns.created_at),
            ("session.columns.expiresAt", &s.columns.expires_at),
            ("session.columns.lastSeenAt", &s.columns.last_seen_at),
            ("session.columns.revokedAt", &s.columns.revoked_at),
            ("credential.table", &c.table),
            ("credential.columns.id", &c.columns.id),
            ("credential.columns.user", &c.columns.user),
            ("credential.columns.passwordHash", &c.columns.password_hash),
            (
                "credential.columns.passwordChangedAt",
                &c.columns.password_changed_at,
            ),
            ("credential.columns.disabledAt", &c.columns.disabled_at),
        ];
        if let (Some(r), Some(g)) = (&self.role, &self.role_grant) {
            names.extend([
                ("role.type", r.type_name.as_str()),
                ("role.table", &r.table),
                ("role.columns.key", &r.columns.key),
                ("role.columns.name", &r.columns.name),
                ("role.columns.permissions", &r.columns.permissions),
                ("role.keyScalar", &r.key_scalar),
                ("roleGrant.table", &g.table),
                ("roleGrant.columns.id", &g.columns.id),
                ("roleGrant.columns.user", &g.columns.user),
                ("roleGrant.columns.role", &g.columns.role),
                ("roleGrant.columns.grantedAt", &g.columns.granted_at),
            ]);
        }
        names
    }
}

/// A name quoted as every dialect the store speaks reads it: a double
/// quote, the name with each double quote doubled, and a double quote.
pub(crate) fn quote(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}
