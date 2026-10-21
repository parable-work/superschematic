package rustrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestRustRouterAnswersUnionRequests serves union-lists-api's Rust router
// and sends it union_requests.json's requests (sdktest.UnionRequestsPath)
// through unionRequestsRouterTest, which records what the router answers;
// sdktest.CheckUnionResponses holds the answers to the requests'
// expectations, as it holds the Go and TypeScript routers'. A union body
// argument is prepared by prepare_<union>, which validates the member the
// union picks, and an input's union fields are validated as their members.
func TestRustRouterAnswersUnionRequests(t *testing.T) {
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatal(err)
	}
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	responses := filepath.Join(t.TempDir(), "responses.json")
	t.Setenv("UNION_REQUESTS", requestsPath)
	t.Setenv("UNION_RESPONSES", responses)
	cargoTestAPICrate(t, sdktest.UnionListsService, schema, "union_requests", unionRequestsRouterTest)
	problems, err := sdktest.CheckUnionResponses(requests, responses)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("the Rust router answered union_requests.json otherwise:\n%s", strings.Join(problems, "\n"))
	}
}

// unionRequestsRouterTest is tests/union_requests.rs of the generated API
// crate. Its implementation answers drawShape with the shape it received,
// pay with the payment's kind, checkout with the payment's kind, a colon
// and the number of shapes, the list, grid and map operations with the
// number of shapes, and probe with ok. It sends each request of
// $UNION_REQUESTS and writes what the router answered to
// $UNION_RESPONSES.
const unionRequestsRouterTest = `use std::sync::Arc;

use API_CRATE::{
    build_router, types, Implementations, ShapeCheckoutArgs, ShapeDrawShapeArgs, ShapeDrawShapesArgs,
    ShapeFindShapeArgs, ShapeGetShapeArgs, ShapeImplementation, ShapePayArgs, ShapeProbeArgs,
    ShapeShapeGridArgs, ShapeShapesByKeyArgs,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::Request;
use serde_json::{json, Value};
use tower::ServiceExt;

struct Shapes;

fn kind_of(payment: &types::Payment) -> String {
    match payment {
        types::Payment::Card(card) => card.kind.clone(),
        types::Payment::Invoice(invoice) => invoice.kind.clone(),
    }
}

#[async_trait]
impl ShapeImplementation for Shapes {
    async fn checkout(&self, _: RequestContext, args: ShapeCheckoutArgs) -> Result<String, ApiError> {
        let shapes = args.input.shapes.map_or(0, |shapes| shapes.len());
        Ok(format!("{}:{shapes}", kind_of(&args.input.payment)))
    }
    async fn pay(&self, _: RequestContext, args: ShapePayArgs) -> Result<String, ApiError> {
        Ok(kind_of(&args.payment))
    }
    async fn draw_shape(&self, _: RequestContext, args: ShapeDrawShapeArgs) -> Result<types::Shape, ApiError> {
        Ok(args.shape)
    }
    async fn draw_shapes(&self, _: RequestContext, args: ShapeDrawShapesArgs) -> Result<String, ApiError> {
        Ok(args.shapes.len().to_string())
    }
    async fn shape_grid(&self, _: RequestContext, args: ShapeShapeGridArgs) -> Result<String, ApiError> {
        Ok(args.rows.iter().map(Vec::len).sum::<usize>().to_string())
    }
    async fn shapes_by_key(&self, _: RequestContext, args: ShapeShapesByKeyArgs) -> Result<String, ApiError> {
        Ok(args.by_key.len().to_string())
    }
    async fn probe(&self, _: RequestContext, _: ShapeProbeArgs) -> Result<String, ApiError> {
        Ok("ok".to_string())
    }
    async fn shape_groups(&self, _: RequestContext) -> Result<Vec<types::Shape>, ApiError> {
        Ok(vec![])
    }
    async fn shapes_by_name(&self, _: RequestContext) -> Result<types::Shape, ApiError> {
        Err(ApiError::not_found("no shapes"))
    }
    async fn find_shapes(&self, _: RequestContext) -> Result<Vec<types::Shape>, ApiError> {
        Ok(vec![])
    }
    async fn find_shape(&self, _: RequestContext, _: ShapeFindShapeArgs) -> Result<types::Shape, ApiError> {
        Err(ApiError::not_found("no shape"))
    }
    async fn list_shapes(&self, _: RequestContext) -> Result<Vec<types::Shape>, ApiError> {
        Ok(vec![])
    }
    async fn shape_rows(&self, _: RequestContext) -> Result<Vec<Vec<types::Shape>>, ApiError> {
        Ok(vec![])
    }
    async fn get_shape(&self, _: RequestContext, _: ShapeGetShapeArgs) -> Result<types::Shape, ApiError> {
        Err(ApiError::not_found("no shape"))
    }
}

#[tokio::test]
async fn union_requests() {
    let file: Value = serde_json::from_str(
        &std::fs::read_to_string(std::env::var("UNION_REQUESTS").unwrap()).unwrap(),
    )
    .unwrap();
    let mut responses = Vec::new();
    for request in file["requests"].as_array().unwrap() {
        let router = build_router(Implementations { shape: Arc::new(Shapes) });
        let http_request = Request::builder()
            .method("POST")
            .uri(request["path"].as_str().unwrap())
            .header("content-type", "application/json")
            .body(Body::from(request["body"].to_string()))
            .unwrap();
        let response = router.oneshot(http_request).await.unwrap();
        let status = response.status().as_u16();
        let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let body: Value = serde_json::from_slice(&bytes).unwrap();
        responses.push(json!({ "name": request["name"], "status": status, "body": body }));
    }
    std::fs::write(
        std::env::var("UNION_RESPONSES").unwrap(),
        serde_json::to_vec(&responses).unwrap(),
    )
    .unwrap();
}
`
