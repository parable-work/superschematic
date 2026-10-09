#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct AccountNamespace {
    client: HttpClient,
}

impl AccountNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// For each operation of the API an end user may call, keyed by its OpenAPI operation id, whether its route admits the caller.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn capabilities(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::Capabilities, SDKError> {
        let path = "/api/auth/capabilities";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Changes the caller's password, given their current one, and ends their other sessions. It answers true.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn change_password(
        &self,
        input: types::ChangePasswordInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<bool, SDKError> {
        let path = "/api/auth/password";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ChangePasswordInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Signs a user in with their login and password and starts a session. A bearer session answers its token; a cookie session sets the session cookie and answers none.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn login(
        &self,
        input: types::LoginInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::LoginResult, SDKError> {
        let path = "/api/auth/login";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("LoginInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Ends the caller's session and clears the session cookie. It answers true.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn logout(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<bool, SDKError> {
        let path = "/api/auth/logout";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
    /// The caller's user, the roles they hold and the permissions those roles grant.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn me(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::CurrentUser, SDKError> {
        let path = "/api/auth/me";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Creates a user with the login, name and password given and signs them in, as login does.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn register(
        &self,
        input: types::RegisterInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::LoginResult, SDKError> {
        let path = "/api/auth/register";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RegisterInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
