#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct AccountAdminNamespace {
    client: HttpClient,
}

impl AccountAdminNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// Creates a role. The caller's own permissions must cover each permission it grants.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn create_role(
        &self,
        input: types::RoleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityRole, SDKError> {
        let path = "/api/auth/admin/roles";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RoleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Creates a user with the login, name and password given.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn create_user(
        &self,
        input: types::CreateUserInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = "/api/auth/admin/users";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("CreateUserInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Deletes a role and every grant of it. It answers true.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn delete_role(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<bool, SDKError> {
        let path = &format!("/api/auth/admin/roles/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("DELETE", path, &query_params, None, options)
            .await
    }
    /// Disables a user, who can no longer sign in, and ends their sessions.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn disable_user(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = &format!("/api/auth/admin/users/{}/disable", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
    /// Enables a disabled user.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn enable_user(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = &format!("/api/auth/admin/users/{}/enable", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
    /// One user and the roles they hold.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_user(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = &format!("/api/auth/admin/users/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Grants a user a role. The caller's own permissions must cover the role's.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn grant_role(
        &self,
        id: types::IdentityUUID,
        role_id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = &format!("/api/auth/admin/users/{}/roles/{}", runtime::path_segment(&id), runtime::path_segment(&role_id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("PUT", path, &query_params, None, options)
            .await
    }
    /// Lists the roles and the permissions each grants.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_roles(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<Vec<types::IdentityRole>, SDKError> {
        let path = "/api/auth/admin/roles";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Lists the users and the roles each holds.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_users(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<Vec<types::IdentityUser>, SDKError> {
        let path = "/api/auth/admin/users";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Revokes a role from a user.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn revoke_role(
        &self,
        id: types::IdentityUUID,
        role_id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityUser, SDKError> {
        let path = &format!("/api/auth/admin/users/{}/roles/{}", runtime::path_segment(&id), runtime::path_segment(&role_id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("DELETE", path, &query_params, None, options)
            .await
    }
    /// Sets a user's password and ends their sessions. It answers true.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn set_user_password(
        &self,
        id: types::IdentityUUID,
        input: types::SetPasswordInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<bool, SDKError> {
        let path = &format!("/api/auth/admin/users/{}/password", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("SetPasswordInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("PUT", path, &query_params, body, options)
            .await
    }
    /// Renames a role or replaces its permissions. The caller's own permissions must cover each permission given.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn update_role(
        &self,
        id: types::IdentityUUID,
        input: types::RoleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::IdentityRole, SDKError> {
        let path = &format!("/api/auth/admin/roles/{}", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RoleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("PUT", path, &query_params, body, options)
            .await
    }
}
