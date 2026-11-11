#[allow(unused_imports)]
use serde::Serialize;

use regex::Regex;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct CatalogNamespace {
    client: HttpClient,
}
#[derive(Debug, Clone, Default)]
pub struct ListItemsQueryParams {
    pub ids: Option<Vec<types::IdentityUUID>>,
    pub in_stock: Option<bool>,
    pub sort: Option<String>,
    pub cursor: Option<String>,
    pub limit: Option<i64>,
}


/// The filter of `list_parts` (D106): a member per field a caller may
/// filter by, `None` or the field's operators, and all of them hold at
/// once.
#[derive(Debug, Clone, Default)]
pub struct ListPartsFilter {
    pub id: Option<runtime::Equals<types::IdentityUUID>>,
    pub name: Option<runtime::Text<String>>,
    pub kind: Option<runtime::Equals<types::PartKind>>,
    pub quantity: Option<runtime::Ordered<types::GenericInt64>>,
    pub weight: Option<runtime::Ordered<f64>>,
    pub price: Option<runtime::Ordered<types::GenericDecimal>>,
    pub discontinued: Option<runtime::Equals<bool>>,
    pub made_at: Option<runtime::Ordered<types::TemporalDateTime>>,
    pub retired_on: Option<runtime::Ordered<types::TemporalDate>>,
    pub supplier_id: Option<runtime::Equals<types::IdentityUUID>>,
}


#[derive(Debug, Clone, Default)]
pub struct ListPartsQueryParams {
    pub sort: Option<String>,
    pub filter: Option<ListPartsFilter>,
    pub cursor: Option<String>,
    pub limit: Option<i64>,
}


#[derive(Debug, Clone, Default)]
pub struct ListTagsQueryParams {
    pub cursor: Option<String>,
    pub limit: Option<i64>,
}



impl CatalogNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// One item.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_item(
        &self,
        item_id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ItemView, SDKError> {
        let path = &format!("/api/items/{}", runtime::path_segment(&item_id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// The items, two to a page unless the caller asks for up to five, by
    /// their ids or their stock, in order of their names unless the caller
    /// sorts them by name or sku, either way.
    ///
    /// - `query`: optional query parameters for this request.
    ///
    /// Answers one page (D60): `query.cursor` is the `next` of the page
    /// before, `None` for the first, and `query.limit` the most items the
    /// page holds, 1 to 5, 2 when `None`. `list_items_all`
    /// yields every item, page after page.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_items(
        &self,
        query: Option<&ListItemsQueryParams>,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<runtime::Page<types::ItemView>, SDKError> {
        let path = "/api/items";
        let mut query_params: Vec<(String, String)> = Vec::new();
        if let Some(query) = query {
            // An empty list is left out: the route refuses a present empty value.
            if let Some(query_param_value) = query.ids.as_ref().filter(|list| !list.is_empty()) {
            // The server validates each item, not the joined value.
            for (query_param_index, query_param_item) in query_param_value.iter().enumerate() {
                let query_param_item_text = query_param_item.to_string();
                runtime::check_query_list_item("ids", query_param_index, &query_param_item_text)?;
            }
            if query_param_value.len() > 5 {
                return Err(SDKError::config("ids must contain at most 5 items"));
            }
            // An array is one comma-separated query value (?name=a,b).
            let query_param_text = query_param_value.iter().map(|value| value.to_string()).collect::<Vec<_>>().join(",");
            query_params.push(("ids".to_string(), query_param_text));
            }
            if let Some(query_param_value) = &query.in_stock {
            query_params.push(("inStock".to_string(), query_param_value.to_string()));
            }
            if let Some(query_param_value) = &query.sort {
            query_params.push(("sort".to_string(), query_param_value.to_string()));
            }
            // An empty value is left out, as the route reads it: none.
            if let Some(query_param_value) = query.cursor.as_ref().filter(|text| !text.is_empty()) {
            let query_param_text = query_param_value.to_string();
            if query_param_text.chars().count() > 1024 {
                return Err(SDKError::config("cursor must be at most 1024 characters"));
            }
            let query_param_pattern = Regex::new("^[A-Za-z0-9_-]+$")
                .map_err(|err| SDKError::config(format!("invalid validation pattern for cursor: {}", err)))?;
            if !query_param_pattern.is_match(&query_param_text) {
                return Err(SDKError::config("cursor has an invalid format"));
            }
            query_params.push(("cursor".to_string(), query_param_value.to_string()));
            }
            if let Some(query_param_value) = &query.limit {
            let query_param_text = query_param_value.to_string();
            let query_param_number = query_param_text
                .parse::<f64>()
                .map_err(|_| SDKError::config("limit must be numeric"))?;
            if query_param_number < 1.0 {
                return Err(SDKError::config("limit must be at least 1"));
            }
            if query_param_number > 5.0 {
                return Err(SDKError::config("limit must be at most 5"));
            }
            query_params.push(("limit".to_string(), query_param_value.to_string()));
            }
        }
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Yields every item of `list_items`, page after page (D60). The
    /// first page is the one `query.cursor` names, the first of the list
    /// when it is `None`, and each page holds up to `query.limit` items. A
    /// page is asked for, with the last one's `next`, only when the items
    /// before it are taken; an error ends the iteration, and so does a page
    /// whose `next` is the cursor it was asked with, after its items.
    pub fn list_items_all<'a>(
        &'a self,
        query: Option<&ListItemsQueryParams>,
        options: Option<&'a runtime::RequestOptions>,
    ) -> runtime::Pager<'a, types::ItemView> {
        let mut query = query.cloned().unwrap_or_default();
        let cursor = query.cursor.take();
        runtime::Pager::new("list_items", cursor, move |cursor| {
            query.cursor = cursor;
            let page_query = query.clone();
            Box::pin(async move {
                self.list_items(
                    Some(&page_query),
                    options,
                )
                .await
            })
        })
    }
    /// The parts, in order of their names unless the caller sorts them by
    /// quantity, kept to the ones the caller's filters find (D106): a filter
    /// on a field of each kind, beside a page and a sort.
    ///
    /// - `query`: optional query parameters for this request.
    ///
    /// Answers one page (D60): `query.cursor` is the `next` of the page
    /// before, `None` for the first, and `query.limit` the most items the
    /// page holds, 1 to 5, 2 when `None`. `list_parts_all`
    /// yields every item, page after page.
    ///
    /// `query.filter` keeps the items its members find (D106): each
    /// member is a field's operators (`eq`, `one_of`, and the ones its type
    /// adds), and all of them hold at once. A cursor carries no filter:
    /// the next page is asked with the same one, as `list_parts_all` does.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_parts(
        &self,
        query: Option<&ListPartsQueryParams>,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<runtime::Page<types::PartView>, SDKError> {
        let path = "/api/parts";
        let mut query_params: Vec<(String, String)> = Vec::new();
        if let Some(query) = query {
            if let Some(query_param_value) = &query.sort {
            query_params.push(("sort".to_string(), query_param_value.to_string()));
            }
            // The filter is written in the bracket form (D106):
            // filter[field]=value, or filter[field][op]=value.
            if let Some(query_filter) = &query.filter {
                let query_filter_start = query_params.len();
                if let Some(query_filter_field) = &query_filter.id {
                    query_filter_field.append_query("id", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.name {
                    query_filter_field.append_query("name", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.kind {
                    query_filter_field.append_query("kind", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.quantity {
                    query_filter_field.append_query("quantity", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.weight {
                    query_filter_field.append_query("weight", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.price {
                    query_filter_field.append_query("price", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.discontinued {
                    query_filter_field.append_query("discontinued", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.made_at {
                    query_filter_field.append_query("madeAt", false, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.retired_on {
                    query_filter_field.append_query("retiredOn", true, &mut query_params)?;
                }
                if let Some(query_filter_field) = &query_filter.supplier_id {
                    query_filter_field.append_query("supplierId", true, &mut query_params)?;
                }
                runtime::check_filter_count(query_params.len() - query_filter_start)?;
            }
            // An empty value is left out, as the route reads it: none.
            if let Some(query_param_value) = query.cursor.as_ref().filter(|text| !text.is_empty()) {
            let query_param_text = query_param_value.to_string();
            if query_param_text.chars().count() > 1024 {
                return Err(SDKError::config("cursor must be at most 1024 characters"));
            }
            let query_param_pattern = Regex::new("^[A-Za-z0-9_-]+$")
                .map_err(|err| SDKError::config(format!("invalid validation pattern for cursor: {}", err)))?;
            if !query_param_pattern.is_match(&query_param_text) {
                return Err(SDKError::config("cursor has an invalid format"));
            }
            query_params.push(("cursor".to_string(), query_param_value.to_string()));
            }
            if let Some(query_param_value) = &query.limit {
            let query_param_text = query_param_value.to_string();
            let query_param_number = query_param_text
                .parse::<f64>()
                .map_err(|_| SDKError::config("limit must be numeric"))?;
            if query_param_number < 1.0 {
                return Err(SDKError::config("limit must be at least 1"));
            }
            if query_param_number > 5.0 {
                return Err(SDKError::config("limit must be at most 5"));
            }
            query_params.push(("limit".to_string(), query_param_value.to_string()));
            }
        }
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Yields every item of `list_parts`, page after page (D60). The
    /// first page is the one `query.cursor` names, the first of the list
    /// when it is `None`, and each page holds up to `query.limit` items. A
    /// page is asked for, with the last one's `next`, only when the items
    /// before it are taken; an error ends the iteration, and so does a page
    /// whose `next` is the cursor it was asked with, after its items.
    /// Every page is asked with `query.filter` (D106).
    pub fn list_parts_all<'a>(
        &'a self,
        query: Option<&ListPartsQueryParams>,
        options: Option<&'a runtime::RequestOptions>,
    ) -> runtime::Pager<'a, types::PartView> {
        let mut query = query.cloned().unwrap_or_default();
        let cursor = query.cursor.take();
        runtime::Pager::new("list_parts", cursor, move |cursor| {
            query.cursor = cursor;
            let page_query = query.clone();
            Box::pin(async move {
                self.list_parts(
                    Some(&page_query),
                    options,
                )
                .await
            })
        })
    }
    /// The tags items carry: a page of a primitive, with the default limits.
    ///
    /// - `query`: optional query parameters for this request.
    ///
    /// Answers one page (D60): `query.cursor` is the `next` of the page
    /// before, `None` for the first, and `query.limit` the most items the
    /// page holds, 1 to 100, 20 when `None`. `list_tags_all`
    /// yields every item, page after page.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_tags(
        &self,
        query: Option<&ListTagsQueryParams>,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<runtime::Page<String>, SDKError> {
        let path = "/api/tags";
        let mut query_params: Vec<(String, String)> = Vec::new();
        if let Some(query) = query {
            // An empty value is left out, as the route reads it: none.
            if let Some(query_param_value) = query.cursor.as_ref().filter(|text| !text.is_empty()) {
            let query_param_text = query_param_value.to_string();
            if query_param_text.chars().count() > 1024 {
                return Err(SDKError::config("cursor must be at most 1024 characters"));
            }
            let query_param_pattern = Regex::new("^[A-Za-z0-9_-]+$")
                .map_err(|err| SDKError::config(format!("invalid validation pattern for cursor: {}", err)))?;
            if !query_param_pattern.is_match(&query_param_text) {
                return Err(SDKError::config("cursor has an invalid format"));
            }
            query_params.push(("cursor".to_string(), query_param_value.to_string()));
            }
            if let Some(query_param_value) = &query.limit {
            let query_param_text = query_param_value.to_string();
            let query_param_number = query_param_text
                .parse::<f64>()
                .map_err(|_| SDKError::config("limit must be numeric"))?;
            if query_param_number < 1.0 {
                return Err(SDKError::config("limit must be at least 1"));
            }
            if query_param_number > 100.0 {
                return Err(SDKError::config("limit must be at most 100"));
            }
            query_params.push(("limit".to_string(), query_param_value.to_string()));
            }
        }
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Yields every item of `list_tags`, page after page (D60). The
    /// first page is the one `query.cursor` names, the first of the list
    /// when it is `None`, and each page holds up to `query.limit` items. A
    /// page is asked for, with the last one's `next`, only when the items
    /// before it are taken; an error ends the iteration, and so does a page
    /// whose `next` is the cursor it was asked with, after its items.
    pub fn list_tags_all<'a>(
        &'a self,
        query: Option<&ListTagsQueryParams>,
        options: Option<&'a runtime::RequestOptions>,
    ) -> runtime::Pager<'a, String> {
        let mut query = query.cloned().unwrap_or_default();
        let cursor = query.cursor.take();
        runtime::Pager::new("list_tags", cursor, move |cursor| {
            query.cursor = cursor;
            let page_query = query.clone();
            Box::pin(async move {
                self.list_tags(
                    Some(&page_query),
                    options,
                )
                .await
            })
        })
    }
    /// Place a basket.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn place_basket(
        &self,
        input: types::BasketInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::BasketView, SDKError> {
        let path = "/api/baskets";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("BasketInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
