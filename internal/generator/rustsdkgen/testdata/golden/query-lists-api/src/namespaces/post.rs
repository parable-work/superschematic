#[allow(unused_imports)]
use serde::Serialize;

use regex::Regex;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct PostNamespace {
    client: HttpClient,
}
#[derive(Debug, Clone)]
pub struct CountPostsQueryParams {
    pub ids: Vec<types::IdentityUUID>,
    pub shades: Option<Vec<types::Shade>>,
    pub ranks: Option<Vec<types::OrderingRank>>,
    pub codes: Option<Vec<String>>,
    pub tags: Option<Vec<String>>,
    pub flags: Option<Vec<bool>>,
    pub limit: Option<f64>,
}



impl PostNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// Count the posts that match every filter.
    ///
    /// - `query`: optional query parameters for this request.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn count_posts(
        &self,
        query: Option<&CountPostsQueryParams>,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<f64, SDKError> {
        let path = "/api/posts/count";
        let mut query_params: Vec<(String, String)> = Vec::new();
        if let Some(query) = query {
            let query_param_value = &query.ids;
            if query_param_value.is_empty() {
                return Err(SDKError::config("ids is required"));
            }
            // The server validates each item, not the joined value.
            for (query_param_index, query_param_item) in query_param_value.iter().enumerate() {
                let query_param_item_text = query_param_item.to_string();
                runtime::check_query_list_item("ids", query_param_index, &query_param_item_text)?;
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("ids".to_string(), query_param_text));
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.shades.as_ref().filter(|list| !list.is_empty()) {
            // The server validates each item, not the joined value.
            for (query_param_index, query_param_item) in query_param_value.iter().enumerate() {
                let query_param_item_text = query_param_item.to_string();
                runtime::check_query_list_item("shades", query_param_index, &query_param_item_text)?;
            }
            if query_param_value.len() < 2 {
                return Err(SDKError::config("shades must contain at least 2 items"));
            }
            if query_param_value.len() > 3 {
                return Err(SDKError::config("shades must contain at most 3 items"));
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("shades".to_string(), query_param_text));
            }
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.ranks.as_ref().filter(|list| !list.is_empty()) {
            // The server validates each item, not the joined value.
            for query_param_item in query_param_value.iter() {
                let query_param_item_text = query_param_item.to_string();
                let query_param_item_number = query_param_item_text
                    .parse::<f64>()
                    .map_err(|_| SDKError::config("each ranks item must be numeric"))?;
                if query_param_item_number < 1.0 {
                    return Err(SDKError::config("each ranks item must be at least 1"));
                }
                if query_param_item_number > 100.0 {
                    return Err(SDKError::config("each ranks item must be at most 100"));
                }
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("ranks".to_string(), query_param_text));
            }
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.codes.as_ref().filter(|list| !list.is_empty()) {
            let query_param_pattern = Regex::new("^[a-z]+$")
                .map_err(|err| SDKError::config(format!("invalid validation pattern for codes: {}", err)))?;
            // The server validates each item, not the joined value.
            for (query_param_index, query_param_item) in query_param_value.iter().enumerate() {
                let query_param_item_text = query_param_item.to_string();
                runtime::check_query_list_item("codes", query_param_index, &query_param_item_text)?;
                if query_param_item_text.chars().count() < 2 {
                    return Err(SDKError::config("each codes item must be at least 2 characters"));
                }
                if query_param_item_text.chars().count() > 4 {
                    return Err(SDKError::config("each codes item must be at most 4 characters"));
                }
                if !query_param_pattern.is_match(&query_param_item_text) {
                    return Err(SDKError::config("each codes item has an invalid format"));
                }
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("codes".to_string(), query_param_text));
            }
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.tags.as_ref().filter(|list| !list.is_empty()) {
            // The server validates each item, not the joined value.
            for (query_param_index, query_param_item) in query_param_value.iter().enumerate() {
                let query_param_item_text = query_param_item.to_string();
                runtime::check_query_list_item("tags", query_param_index, &query_param_item_text)?;
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("tags".to_string(), query_param_text));
            }
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.flags.as_ref().filter(|list| !list.is_empty()) {
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("flags".to_string(), query_param_text));
            }
            if let Some(query_param_value) = &query.limit {
            let query_param_text = query_param_value.to_string();
            let query_param_number = query_param_text
                .parse::<f64>()
                .map_err(|_| SDKError::config("limit must be numeric"))?;
            if query_param_number > 50.0 {
                return Err(SDKError::config("limit must be at most 50"));
            }
            query_params.push(("limit".to_string(), query_param_value.to_string()));
            }
        } else {
            return Err(SDKError::config("query parameters are required"));
        }
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
}
