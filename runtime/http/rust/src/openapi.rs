//! Serving a generated server's OpenAPI document and its documentation page,
//! as the generated Go server serves them.

use axum::http::header::CONTENT_TYPE;
use axum::response::IntoResponse;
use axum::routing::get;
use axum::Router;
use serde_json::{json, Map, Value};

/// The `info.version` a served document states when the service sets none,
/// the generated Go server's default.
pub const DEFAULT_OPENAPI_VERSION: &str = "1.0.0";

/// The server URL a served document states when the service sets none, the
/// generated Go server's default.
pub const DEFAULT_OPENAPI_BASE_URL: &str = "http://localhost:8080";

/// What a generated server's `build_router_with` mounts beside its routes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RouterOptions {
    /// Serve the OpenAPI document at `GET /api/openapi.json` and its RapiDoc
    /// page at `GET /api/docs`. On by default, as in the generated Go server.
    pub serve_openapi: bool,
    /// The `info.version` the served document states.
    pub openapi_version: String,
    /// The URL of the one server the served document lists.
    pub openapi_base_url: String,
}

impl Default for RouterOptions {
    fn default() -> Self {
        Self {
            serve_openapi: true,
            openapi_version: DEFAULT_OPENAPI_VERSION.to_owned(),
            openapi_base_url: DEFAULT_OPENAPI_BASE_URL.to_owned(),
        }
    }
}

/// The document a build wrote, with `info.version` and the server list the
/// running service states in place of the build's placeholders. A document
/// that does not parse is served as it is, as the Go server serves it.
pub fn openapi_document(raw: &str, version: &str, base_url: &str) -> String {
    let Ok(Value::Object(mut document)) = serde_json::from_str::<Value>(raw) else {
        return raw.to_owned();
    };
    let info = document
        .entry("info")
        .or_insert_with(|| Value::Object(Map::new()));
    if !info.is_object() {
        *info = Value::Object(Map::new());
    }
    if let Value::Object(info) = info {
        info.insert("version".to_owned(), Value::String(version.to_owned()));
    }
    document.insert(
        "servers".to_owned(),
        json!([{"url": base_url, "description": "Runtime server"}]),
    );
    serde_json::to_string_pretty(&Value::Object(document)).unwrap_or_else(|_| raw.to_owned())
}

/// The RapiDoc page that renders `/api/openapi.json`, as the generated Go
/// server's `/api/docs` does.
pub fn rapidoc_html(title: &str) -> String {
    let title = escape_html(title);
    format!(
        r##"<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{title} API Documentation</title>
  <script type="module" src="https://unpkg.com/rapidoc/dist/rapidoc-min.js"></script>
</head>
<body>
  <rapi-doc
    spec-url="/api/openapi.json"
    theme="dark"
    bg-color="#1a1a1a"
    text-color="#f0f0f0"
    primary-color="#4a9eff"
    header-color="#1a1a1a"
    nav-bg-color="#2d2d2d"
    nav-text-color="#f0f0f0"
    nav-hover-bg-color="#3d3d3d"
    nav-hover-text-color="#ffffff"
    nav-accent-color="#4a9eff"
    render-style="read"
    show-header="true"
    allow-authentication="true"
    persist-auth="true"
    allow-server-selection="true"
    allow-api-list-style-selection="false"
    fill-request-fields-with-example="true"
    show-method-in-nav-bar="as-colored-block"
    use-path-in-nav-bar="false"
    schema-style="table"
    schema-expand-level="2"
    schema-description-expanded="true"
    default-schema-tab="model"
    response-area-height="400px"
    regular-font="'Open Sans', sans-serif"
    mono-font="'Roboto Mono', monospace"
    font-size="default"
  >
    <div slot="nav-logo" style="padding: 20px; text-align: center;">
      <h2 style="margin: 0; color: #4a9eff;">{title}</h2>
      <p style="margin: 5px 0 0 0; color: #999; font-size: 14px;">API Documentation</p>
    </div>
  </rapi-doc>
</body>
</html>"##
    )
}

/// A router serving `raw` at `GET /api/openapi.json`, with the version and
/// server URL of `options`, and its RapiDoc page titled `title` at
/// `GET /api/docs`. Empty when `options.serve_openapi` is off.
pub fn openapi_router(raw: &str, title: &str, options: &RouterOptions) -> Router {
    if !options.serve_openapi {
        return Router::new();
    }
    let document = openapi_document(raw, &options.openapi_version, &options.openapi_base_url);
    let page = rapidoc_html(title);
    Router::new()
        .route(
            "/api/openapi.json",
            get(move || {
                let document = document.clone();
                async move { ([(CONTENT_TYPE, "application/json")], document).into_response() }
            }),
        )
        .route(
            "/api/docs",
            get(move || {
                let page = page.clone();
                async move { ([(CONTENT_TYPE, "text/html; charset=utf-8")], page).into_response() }
            }),
        )
}

fn escape_html(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
}

#[cfg(test)]
mod tests {
    use axum::body::Body;
    use axum::http::{Request, StatusCode};
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    use super::*;

    const RAW: &str = r#"{"openapi":"3.0.3","info":{"title":"shop","version":"__OPENAPI_VERSION__"},"servers":[{"url":"__OPENAPI_BASE_URL__"}],"paths":{}}"#;

    #[test]
    fn the_document_states_the_running_version_and_server() {
        let document: Value =
            serde_json::from_str(&openapi_document(RAW, "2.3.4", "https://api.example")).unwrap();
        assert_eq!(document["info"]["version"], "2.3.4");
        assert_eq!(document["info"]["title"], "shop");
        assert_eq!(
            document["servers"],
            json!([{"url": "https://api.example", "description": "Runtime server"}])
        );
        assert_eq!(openapi_document("not json", "1", "x"), "not json");
        let without_info: Value =
            serde_json::from_str(&openapi_document(r#"{"info":7}"#, "1.0.0", "x")).unwrap();
        assert_eq!(without_info["info"], json!({"version": "1.0.0"}));
    }

    #[test]
    fn the_page_escapes_its_title() {
        let page = rapidoc_html("a<b>");
        assert!(page.contains("<title>a&lt;b&gt; API Documentation</title>"));
        assert!(page.contains(r#"spec-url="/api/openapi.json""#));
    }

    async fn get(router: Router, path: &str) -> (StatusCode, String, String) {
        let response = router
            .oneshot(Request::get(path).body(Body::empty()).unwrap())
            .await
            .unwrap();
        let status = response.status();
        let content_type = response
            .headers()
            .get(CONTENT_TYPE)
            .map(|v| v.to_str().unwrap().to_owned())
            .unwrap_or_default();
        let body = response.into_body().collect().await.unwrap().to_bytes();
        (
            status,
            content_type,
            String::from_utf8(body.to_vec()).unwrap(),
        )
    }

    #[tokio::test]
    async fn the_router_serves_the_document_and_the_page() {
        let router = openapi_router(RAW, "shop", &RouterOptions::default());
        let (status, content_type, body) = get(router.clone(), "/api/openapi.json").await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(content_type, "application/json");
        let document: Value = serde_json::from_str(&body).unwrap();
        assert_eq!(document["info"]["version"], DEFAULT_OPENAPI_VERSION);
        assert_eq!(document["servers"][0]["url"], DEFAULT_OPENAPI_BASE_URL);
        let (status, content_type, body) = get(router, "/api/docs").await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(content_type, "text/html; charset=utf-8");
        assert!(body.contains("<rapi-doc"));

        let off = RouterOptions {
            serve_openapi: false,
            ..RouterOptions::default()
        };
        let (status, _, _) = get(openapi_router(RAW, "shop", &off), "/api/openapi.json").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }
}
