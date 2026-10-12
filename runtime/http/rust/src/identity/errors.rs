//! The problems the identity runtime answers with, the codes the Go runtime
//! uses: each an [`ApiError`], so the router answers it as an RFC 9457
//! problem. A refused input is 400 `bad_request` with its field errors.

use http::StatusCode;
use serde_json::json;

use crate::schema::ValidationErrors;
use crate::ApiError;

use super::store::StoreError;

/// A login, or `changePassword`'s current password, that does not verify,
/// for whatever reason. 401.
pub const CODE_INVALID_CREDENTIALS: &str = "invalid_credentials";
/// A route that needs a caller, without a usable session: none, an
/// unusable `Authorization` header, or a session that is unknown, expired,
/// revoked or idle, or whose user is disabled. 401.
pub const CODE_UNAUTHORIZED: &str = "unauthorized";
/// The caller lacks the route's permission, or grants what they do not
/// hold. 403.
pub const CODE_FORBIDDEN: &str = "forbidden";
/// A cookie request, or a cookie login, the cross-origin check refuses.
/// 403.
pub const CODE_CROSS_ORIGIN: &str = "cross_origin";
/// No user or role has the id. 404.
pub const CODE_NOT_FOUND: &str = "not_found";
/// The login or the role name is taken. 409.
pub const CODE_CONFLICT: &str = "conflict";
/// A role's permission is not dotted segments of letters, digits, `_` and
/// `-`. 422, with the permissions in `details`.
pub const CODE_INVALID_PERMISSION: &str = "invalid_permission";

pub(crate) fn invalid_credentials() -> ApiError {
    ApiError::new(
        StatusCode::UNAUTHORIZED,
        CODE_INVALID_CREDENTIALS,
        "Invalid login or password",
    )
}

pub(crate) fn unauthenticated() -> ApiError {
    ApiError::unauthorized("Authentication required")
}

pub(crate) fn forbidden(message: &str) -> ApiError {
    ApiError::forbidden(message)
}

/// A grant of permissions the caller does not hold, listing them.
pub(crate) fn not_held(permissions: Vec<String>) -> ApiError {
    ApiError::forbidden("No one grants a permission they do not hold")
        .with_details(json!({ "permissions": permissions }))
}

pub(crate) fn cross_origin() -> ApiError {
    ApiError::new(
        StatusCode::FORBIDDEN,
        CODE_CROSS_ORIGIN,
        "Cross-origin request refused",
    )
}

pub(crate) fn not_found(what: &str) -> ApiError {
    ApiError::not_found(format!("{what} not found"))
}

pub(crate) fn invalid_permissions(permissions: Vec<String>) -> ApiError {
    ApiError::new(
        StatusCode::UNPROCESSABLE_ENTITY,
        CODE_INVALID_PERMISSION,
        "A permission is dotted segments of letters, digits, '_' and '-'",
    )
    .with_details(json!({ "permissions": permissions }))
}

/// A failure that is not the caller's: logged, and answered as a 500 that
/// does not carry its cause.
pub(crate) fn internal(cause: &dyn std::fmt::Display) -> ApiError {
    tracing::error!(cause = %cause, "identity request failed");
    ApiError::internal("An unexpected error occurred")
}

/// A store's error as the problem the routes answer: a missing row is 404
/// naming `what`, a schema without roles 404, a taken role name 409, and
/// anything else a 500.
pub(crate) fn store_error(err: StoreError, what: &str) -> ApiError {
    match err {
        StoreError::NotFound => not_found(what),
        StoreError::NoRoles => ApiError::not_found("The schema has no roles"),
        StoreError::RoleNameTaken => ApiError::conflict("The role name is taken"),
        StoreError::LoginTaken => ApiError::conflict("The login is taken"),
        other => internal(&other),
    }
}

/// The 400 of one field's refusal.
pub(crate) fn field_refusal(field: &str, validator: &str, message: impl Into<String>) -> ApiError {
    let mut errors = FieldErrors::default();
    errors.add(field, validator, message);
    errors
        .check()
        .err()
        .unwrap_or_else(|| ApiError::bad_request("Validation failed"))
}

/// The refusals of an input's fields.
#[derive(Default)]
pub(crate) struct FieldErrors {
    errors: Option<ValidationErrors>,
}

impl FieldErrors {
    pub(crate) fn add(&mut self, field: &str, validator: &str, message: impl Into<String>) {
        self.errors
            .get_or_insert_with(ValidationErrors::new)
            .add(field, validator, message);
    }

    /// The 400 the refusals are, or `Ok` without any.
    pub(crate) fn check(self) -> Result<(), ApiError> {
        match self.errors {
            Some(errors) if !errors.is_empty() => {
                let refusal = ApiError::bad_request("Validation failed");
                Err(match serde_json::to_value(&errors) {
                    Ok(errors) => refusal.with_errors(errors),
                    Err(_) => refusal,
                })
            }
            _ => Ok(()),
        }
    }
}
