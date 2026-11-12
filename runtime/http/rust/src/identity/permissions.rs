//! Roles' permissions: their form, a principal's permissions, the rule that
//! no one grants what they do not hold, what an API key made with
//! permissions holds of its user's roles, and capabilities over the routes
//! (`runtime/http/testdata/README.md`, sections `permissionNames`,
//! `effectivePermissions`, `grants`, `keyPermissions` and `capabilities`).

use std::collections::{BTreeMap, HashSet};

use serde::{Deserialize, Serialize};

use crate::{covers, OperationInfo};

use super::store::Role;

/// Whether `p` has a permission's form: dotted segments of ASCII letters,
/// digits, `_` and `-`, none empty (`orders.read`,
/// `identity.users.write`).
pub fn valid_permission(p: &str) -> bool {
    !p.is_empty()
        && p.split('.').all(|segment| {
            !segment.is_empty()
                && segment
                    .bytes()
                    .all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'-')
        })
}

/// The permissions roles carry, without repeats: each role's in its order,
/// the roles in the order given, and a permission kept where it first
/// appears. A permission another covers is still listed (`a` and `a.b`
/// both are).
pub fn effective_permissions<'a, I>(roles: I) -> Vec<String>
where
    I: IntoIterator<Item = &'a [String]>,
{
    let mut seen = HashSet::new();
    let mut out = Vec::new();
    for permissions in roles {
        for p in permissions {
            if seen.insert(p.as_str()) {
                out.push(p.clone());
            }
        }
    }
    out
}

/// The permissions of `given` that no permission of `held` covers, in
/// `given`'s order and without repeats. A caller may write a role with
/// `given`, or grant a role carrying it, only when it is empty: no one
/// grants what they do not hold.
pub fn uncovered(held: &[String], given: &[String]) -> Vec<String> {
    let mut seen = HashSet::new();
    given
        .iter()
        .filter(|p| seen.insert(p.as_str()))
        .filter(|p| !held.iter().any(|h| covers(h, p)))
        .cloned()
        .collect()
}

/// The permissions a user holds through `roles` that no permission of
/// `held` covers: [`uncovered`] of their [`effective_permissions`]. A
/// caller may set the user's password, disable them or enable them only
/// when it is empty: no one takes over, or locks out, a user who holds
/// what they do not.
pub fn uncovered_user<'a, I>(held: &[String], roles: I) -> Vec<String>
where
    I: IntoIterator<Item = &'a [String]>,
{
    uncovered(held, &effective_permissions(roles))
}

/// The permissions of `named` that some permission of `held` covers, in
/// `named`'s order and without repeats. They are what an API key made with
/// `named` holds of a role carrying `held` (D114): each permission it
/// names while the role holds it, and never a part of one it names more
/// broadly than the role does.
pub fn narrow_permissions(held: &[String], named: &[String]) -> Vec<String> {
    let mut seen = HashSet::new();
    named
        .iter()
        .filter(|p| seen.insert(p.as_str()))
        .filter(|p| held.iter().any(|h| covers(h, p)))
        .cloned()
        .collect()
}

/// Roles as an API key made with `named` holds them (D114): each role with
/// its permissions narrowed ([`narrow_permissions`]), in the order given,
/// and a role left with no permission dropped. A key whose permissions are
/// null names none and holds its user's roles as they are, so its roles
/// are not narrowed.
pub fn narrow_roles(roles: &[Role], named: &[String]) -> Vec<Role> {
    roles
        .iter()
        .filter_map(|role| {
            let permissions = narrow_permissions(&role.permissions, named);
            (!permissions.is_empty()).then(|| Role {
                id: role.id.clone(),
                name: role.name.clone(),
                permissions,
            })
        })
        .collect()
}

/// What capabilities needs to know of one operation of the API: the route
/// requirements of the router's operation table, keyed by the operation's
/// OpenAPI operation id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Route {
    /// The operation's OpenAPI operation id, its key in the answer.
    pub operation_id: String,
    /// The route needs a caller (`@auth`, an `Authenticated` set).
    pub requires_auth: bool,
    /// Its `@requirePermission` list, of which the caller must hold one. A
    /// route with any needs a caller.
    pub permissions: Vec<String>,
    /// `@requireOwnership`: the route needs a caller, and whether they own
    /// the resource is the implementation's, so capabilities answers only
    /// whether the caller is admitted.
    pub require_ownership: bool,
    /// `@requireService`: only a service calls it, so capabilities leaves
    /// it out.
    pub service_only: bool,
}

impl Route {
    /// The route of an operation of the generated crate's `operations`
    /// table, under its OpenAPI operation id. It is not service-only;
    /// [`Route::service_only`] makes it so.
    pub fn of(operation_id: impl Into<String>, info: &OperationInfo) -> Route {
        Route {
            operation_id: operation_id.into(),
            requires_auth: info.requires_auth,
            permissions: info.permissions.iter().map(|p| (*p).to_owned()).collect(),
            require_ownership: info.require_ownership,
            service_only: false,
        }
    }

    /// The route, marked `@requireService`.
    #[must_use]
    pub fn service_only(mut self) -> Route {
        self.service_only = true;
        self
    }

    fn needs_caller(&self) -> bool {
        self.requires_auth || !self.permissions.is_empty() || self.require_ownership
    }

    /// Whether the route admits a caller holding `held`, by the router's
    /// rule: a route that needs a caller admits none when `authenticated` is
    /// false, and one with permissions admits the caller when `permits`
    /// accepts the caller's (the router's `Authenticator::permits`).
    pub fn admits(
        &self,
        authenticated: bool,
        held: &[String],
        permits: &dyn Fn(&[String], &[String]) -> bool,
    ) -> bool {
        if self.needs_caller() && !authenticated {
            return false;
        }
        self.permissions.is_empty() || permits(held, &self.permissions)
    }
}

/// For each route an end user may call (every route but a service-only
/// one), whether it admits an authenticated caller holding `held`, keyed by
/// operation id.
pub fn capabilities_of(
    routes: &[Route],
    held: &[String],
    permits: &dyn Fn(&[String], &[String]) -> bool,
) -> BTreeMap<String, bool> {
    routes
        .iter()
        .filter(|route| !route.service_only)
        .map(|route| {
            (
                route.operation_id.clone(),
                route.admits(true, held, permits),
            )
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(ToString::to_string).collect()
    }

    fn role(id: &str, name: &str, permissions: &[&str]) -> Role {
        Role {
            id: id.to_owned(),
            name: name.to_owned(),
            permissions: strings(permissions),
        }
    }

    /// A key made with permissions holds each role narrowed to them, in
    /// the roles' order, and no role it holds nothing of.
    #[test]
    fn narrow_roles_keeps_what_each_role_covers() {
        let roles = [
            role("1", "clerk", &["orders", "stock.read"]),
            role("2", "auditor", &["audit.read"]),
            role("3", "reader", &["orders.read"]),
        ];
        let got = narrow_roles(&roles, &strings(&["orders.read", "stock", "orders.read"]));
        assert_eq!(
            got,
            [
                role("1", "clerk", &["orders.read"]),
                role("3", "reader", &["orders.read"]),
            ]
        );
        assert_eq!(
            effective_permissions(got.iter().map(|r| r.permissions.as_slice())),
            ["orders.read"]
        );
        for (name, named) in [
            ("nothing held", strings(&["billing"])),
            ("nothing named", Vec::new()),
        ] {
            assert!(narrow_roles(&roles, &named).is_empty(), "{name}");
        }
        assert_eq!(
            roles[0].permissions[0], "orders",
            "the roles given are kept"
        );
    }
}
