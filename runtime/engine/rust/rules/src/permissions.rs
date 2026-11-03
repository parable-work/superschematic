//! Whether a caller holds a permission, as the engine's behaviors ask it
//! today (D58's `can()`), ported to the core (D103). A behavior's context
//! asks the core's matcher whether the caller's permissions satisfy one; a
//! service standing in for an end user holds none (D37), so it is never
//! asked. The default matcher is the HTTP runtime's `hasAnyPermission`:
//! dotted paths, where a granted permission covers itself and every
//! permission nested under it, and no root permission. A deployment with a
//! vocabulary of its own compiles its matcher into its core
//! ([`crate::Core::with_matcher`]).

/// Decides whether held permissions satisfy required ones.
pub trait PermissionMatcher: Send + Sync {
    /// Whether `held` satisfies `required`; an empty `required` is
    /// satisfied.
    fn matches(&self, held: &[String], required: &[String]) -> bool;
}

/// The default matcher: dotted paths ([`has_any_permission`]).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DottedPermissions;

impl PermissionMatcher for DottedPermissions {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        has_any_permission(held, required)
    }
}

/// Whether a granted permission covers a required one: equal, or the
/// required one nested under it (`orders` covers `orders.ship`, not
/// `ordersx`).
pub fn covers(granted: &str, required: &str) -> bool {
    granted == required
        || required
            .strip_prefix(granted)
            .is_some_and(|rest| rest.starts_with('.'))
}

/// True when `required` is empty or any held permission covers any
/// required one: the HTTP runtime's `hasAnyPermission`.
pub fn has_any_permission(held: &[String], required: &[String]) -> bool {
    required.is_empty()
        || required
            .iter()
            .any(|need| held.iter().any(|have| covers(have, need)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn held(permissions: &[&str]) -> Vec<String> {
        permissions.iter().map(|p| (*p).to_owned()).collect()
    }

    #[test]
    fn a_permission_covers_itself_and_what_nests_under_it() {
        assert!(covers("orders", "orders"));
        assert!(covers("orders", "orders.ship"));
        assert!(!covers("orders", "ordersx.ship"));
        assert!(!covers("orders.ship", "orders"));
        assert!(!covers("root", "orders.ship"));
    }

    #[test]
    fn any_held_permission_may_satisfy_any_required_one() {
        let need = held(&["orders.ship"]);
        assert!(has_any_permission(&held(&["orders"]), &need));
        assert!(has_any_permission(&held(&["x", "orders.ship"]), &need));
        assert!(!has_any_permission(&held(&[]), &need));
        assert!(has_any_permission(&held(&[]), &[]));
        assert!(DottedPermissions.matches(&held(&["orders.ship"]), &need));
    }
}
