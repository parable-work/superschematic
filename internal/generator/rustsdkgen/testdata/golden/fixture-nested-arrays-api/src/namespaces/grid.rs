#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct GridNamespace {
    client: HttpClient,
}
#[derive(Debug, Clone)]
pub struct GridLabelsQueryParams {
    pub limit: Option<f64>,
}


#[derive(Debug, Clone, Serialize)]
pub struct ReplaceLabelsInput {
    pub labels: Vec<Vec<String>>,
}



impl GridNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// get_grid endpoint.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_grid(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::GridView, SDKError> {
        let path = format!("/api/grids/{}", id);
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", &path, &query_params, None, options)
            .await
    }
    /// One grid's labels as a bare list of lists, at most `limit` rows.
    ///
    /// - `query`: optional query parameters for this request.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn grid_labels(
        &self,
        id: types::IdentityUUID,
        query: Option<&GridLabelsQueryParams>,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<Vec<Vec<String>>, SDKError> {
        let path = format!("/api/grids/{}/labels", id);
        let mut query_params: Vec<(String, String)> = Vec::new();
        if let Some(query) = query {
            if let Some(query_param_value) = &query.limit {
            query_params.push(("limit".to_string(), query_param_value.to_string()));
            }
        }
        self.client
            .request_json("GET", &path, &query_params, None, options)
            .await
    }
    /// Replace a grid's labels; the body argument is a list of lists.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn replace_labels(
        &self,
        id: types::IdentityUUID,
        input: ReplaceLabelsInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::GridView, SDKError> {
        let path = format!("/api/grids/{}/labels", id);
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(serde_json::to_value(input)?);
        self.client
            .request_json("PUT", &path, &query_params, body, options)
            .await
    }
    /// Store a grid from a request body.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn save_grid(
        &self,
        input: types::SaveGridInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::GridView, SDKError> {
        let path = "/api/grids";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("SaveGridInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", &path, &query_params, body, options)
            .await
    }
}
