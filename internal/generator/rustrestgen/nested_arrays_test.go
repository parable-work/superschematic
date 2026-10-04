package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const nestedArraysService = "fixture-nested-arrays-api"

var nestedArraysClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// loadNestedArraysSchema loads fixture-nested-arrays-api: an input type, a
// PUT body argument and a bare response that are arrays of arrays. With
// withPaint it adds grid.paint (sdktest.AddPaintOperation).
func loadNestedArraysSchema(t *testing.T, withPaint bool) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysService, err)
	}
	if withPaint {
		if err := sdktest.AddPaintOperation(schema); err != nil {
			t.Fatal(err)
		}
	}
	return schema
}

func generateNestedArraysAPI(t *testing.T, schema *ir.Schema, typesDir, outputDir string) *APIOutput {
	t.Helper()
	output, err := Generate(schema, Options{
		AuthProvider: sessionauth.Provider{},
		SchemaName:   nestedArraysService,
		TypesCrate:   naming.Default().RustTypesCrate(nestedArraysService),
		TypesDir:     typesDir,
		OutputDir:    outputDir,
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatalf("expected Rust API output for %s", nestedArraysService)
	}
	return output
}

// TestWriteRustAPIGoldenNestedArrays pins the Rust API crate of
// fixture-nested-arrays-api. Handlers take and return serde_json::Value,
// so a list-of-lists body argument or response needs no route of its own.
// Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenNestedArrays -update
func TestWriteRustAPIGoldenNestedArrays(t *testing.T) {
	root := t.TempDir()
	output := generateNestedArraysAPI(t, loadNestedArraysSchema(t, false),
		filepath.Join(root, "types", "rust", nestedArraysService), filepath.Join(root, "api", nestedArraysService))
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, "/repo/schemas/dist/api/"+nestedArraysService); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", nestedArraysService)
	for _, name := range []string{"Cargo.toml", "src/lib.rs", "src/interfaces.rs", "src/router.rs"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v (run with -update)", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// addImportOperation adds grid.importGrid to the loaded
// fixture-nested-arrays-api schema: an encrypted POST to grid-imports
// declared @manualRouteRegistration, which the service mounts itself.
func addImportOperation(t *testing.T, schema *ir.Schema) {
	t.Helper()
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:                    "importGrid",
			Comment:                 "Import a grid sent encrypted; the service mounts the route.",
			TypeRef:                 ir.TypeRef{Name: "boolean"},
			Required:                true,
			Arguments:               []*ir.ArgumentDef{{Name: "source", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
			HTTPMethod:              "POST",
			RestPath:                "grid-imports",
			Encrypted:               true,
			ManualRouteRegistration: true,
		})
		return
	}
	t.Fatalf("schema %s has no GridMutations operation set", schema.Name)
}

// TestNestedArraysAPICrateBuildsAndRoutes runs cargo test on the Rust API
// crate of fixture-nested-arrays-api, with grid.paint and grid.importGrid
// added, with nestedArraysRouterTest: a list-of-lists body reaches the
// implementation as nested JSON arrays, a list-of-lists result comes back in
// the success envelope, a path parameter reaches it decoded exactly once,
// and the manual grid.importGrid has no trait method and no route until the
// service adds one.
func TestNestedArraysAPICrateBuildsAndRoutes(t *testing.T) {
	schema := loadNestedArraysSchema(t, true)
	addImportOperation(t, schema)
	cargoTestAPICrate(t, nestedArraysService, schema, "nested_arrays", nestedArraysRouterTest)
}

// cargoTestAPICrate generates the Rust types crate and the Rust API crate of
// schema into a temp tree laid out as a build writes it, adds test as
// tests/<testName>.rs of the API crate, with API_CRATE and RUNTIME_CRATE
// replaced by the crates' module names, and runs cargo test on the API
// crate. The types crate resolves superscalar from the checkout
// scripts/superscalar-dep.sh stands up. CARGO_TARGET_DIR is honored when
// set.
func cargoTestAPICrate(t *testing.T, service string, schema *ir.Schema, testName, test string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	paths := testpaths.Local(t)

	// The http-runtime dependency is a path relative to the crate, which
	// cargo resolves from the real directory (macOS /var is /private/var).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "rust", service)
	apiDir := filepath.Join(root, "api", service)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: service, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	output, err := Generate(schema, Options{
		AuthProvider: sessionauth.Provider{},
		SchemaName:   service,
		TypesCrate:   naming.Default().RustTypesCrate(service),
		TypesDir:     typesDir,
		OutputDir:    apiDir,
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SetReplacePaths(output, paths, apiDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteAPI(output, apiDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	cargoToml, err := os.ReadFile(filepath.Join(apiDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cargoToml = append(cargoToml, []byte(`
[dev-dependencies]
tower = { version = "0.5", features = ["util"] }

`+testpaths.RustPatch(paths, naming.Default()))...)
	if err := os.WriteFile(filepath.Join(apiDir, "Cargo.toml"), cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	test = strings.NewReplacer("API_CRATE", strings.ReplaceAll(output.CrateName, "-", "_"), "RUNTIME_CRATE", output.RuntimeCrateIdent).Replace(test)
	if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "tests", testName+".rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}

	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = apiDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test on the generated API crate: %v\n%s", err, out)
	}
}

// nestedArraysRouterTest is tests/nested_arrays.rs of the generated API
// crate, with API_CRATE and RUNTIME_CRATE replaced by the crates' module
// names. Its implementation echoes each body argument back.
const nestedArraysRouterTest = `use std::sync::Arc;

use API_CRATE::{build_router, GridImplementation, Implementations};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use axum::routing::post;
use axum::{Json, Router};
use serde_json::{json, Value};
use tower::ServiceExt;

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

struct Echo;

#[async_trait]
impl GridImplementation for Echo {
    async fn save_grid(&self, _ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        Ok(payload)
    }
    async fn replace_labels(&self, ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        Ok(json!({"id": ctx.path_params.get("id"), "labels": payload["labels"]}))
    }
    async fn paint(&self, _ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        Ok(payload["polygons"].clone())
    }
    async fn get_grid(&self, _ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Err(ApiError::not_implemented("get_grid is not implemented"))
    }
    async fn grid_labels(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        let rows: usize = ctx.query_params.get("limit").and_then(|limit| limit.parse().ok()).unwrap_or(3);
        let labels = json!([["a", "b"], [], ["c"]]);
        Ok(Value::Array(labels.as_array().unwrap().iter().take(rows).cloned().collect()))
    }
}

fn router() -> Router {
    build_router(Implementations { grid: Arc::new(Echo) })
}

async fn call(method: &str, uri: String, body: Option<Value>) -> (StatusCode, Value) {
    send(router(), method, uri, body).await
}

// send answers the status and the JSON body, or null for an empty body.
async fn send(router: Router, method: &str, uri: String, body: Option<Value>) -> (StatusCode, Value) {
    let request = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    let request = match body {
        Some(body) => request.body(Body::from(body.to_string())).unwrap(),
        None => request.body(Body::empty()).unwrap(),
    };
    let response = router.oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    if bytes.is_empty() {
        return (status, Value::Null);
    }
    (status, serde_json::from_slice(&bytes).unwrap())
}

// grid.importGrid is encrypted and @manualRouteRegistration, so Echo has no
// method for it and the generated router answers 404 at its path. The
// service adds the route to the router build_router returns, and its handler
// receives the envelope as sent, to decrypt.
#[tokio::test]
async fn a_manual_operation_is_left_to_the_service() {
    let envelope = json!({"algorithm": "RSA-OAEP-256", "payload": "Y2lwaGVydGV4dA==", "keyId": "k1"});
    let (status, _) = send(router(), "POST", "/api/grid-imports".to_string(), Some(envelope.clone())).await;
    assert_eq!(status, StatusCode::NOT_FOUND);

    let mounted = router().route("/api/grid-imports", post(|Json(body): Json<Value>| async move { Json(body) }));
    let (status, received) = send(mounted, "POST", "/api/grid-imports".to_string(), Some(envelope.clone())).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(received, envelope);
}

#[tokio::test]
async fn a_list_of_lists_body_reaches_the_implementation() {
    let (status, envelope) = call(
        "PUT",
        format!("/api/grids/{GRID_ID}/labels"),
        Some(json!({"labels": [["a", "b"], []]})),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], json!({"id": GRID_ID, "labels": [["a", "b"], []]}));
    assert!(envelope["meta"]["requestId"].is_string());
}

#[tokio::test]
async fn a_list_of_lists_result_is_the_envelope_data() {
    let (status, envelope) = call("GET", format!("/api/grids/{GRID_ID}/labels?limit=2"), None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], json!([["a", "b"], []]));

    let polygons = json!([[{"x": 1.0, "y": 2.0}], []]);
    let (status, envelope) = call(
        "PUT",
        format!("/api/grids/{GRID_ID}/paint"),
        Some(json!({"shades": [["light"], []], "polygons": polygons})),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], polygons);
}

// The id is sent encoded once, as encodeURIComponent writes it, and in
// other encodings of the same value: the implementation receives it decoded
// exactly once, as behind the TypeScript and Go routers. A path whose
// escapes do not decode to UTF-8 answers 400 in the error envelope.
#[tokio::test]
async fn a_path_parameter_is_decoded_once() {
    for (segment, want) in [
        ("%25", "%"),
        ("a%2525b", "a%25b"),
        ("100%25", "100%"),
        ("x%2541y", "x%41y"),
        ("a%2Fb", "a/b"),
        ("caf%C3%A9", "caf\u{e9}"),
        ("a%2Bb%20c", "a+b c"),
        ("%41", "A"),
        ("caf%c3%a9", "caf\u{e9}"),
        ("a%2fb", "a/b"),
    ] {
        let (status, envelope) = call("PUT", format!("/api/grids/{segment}/labels"), Some(json!({"labels": []}))).await;
        assert_eq!(status, StatusCode::OK, "{segment}");
        assert_eq!(envelope["data"]["id"], want, "{segment}");
    }
    for segment in ["%", "100%", "%ZZ", "a%2", "%E9", "%C3%28"] {
        let (status, envelope) = call("PUT", format!("/api/grids/{segment}/labels"), Some(json!({"labels": []}))).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{segment}");
        assert_eq!(envelope["error"]["code"], "bad_request", "{segment}");
    }
}

#[tokio::test]
async fn an_input_type_with_lists_of_lists_passes_through() {
    let grid = json!({"labels": [["a"], []], "shades": [["dark"]], "polygons": [[]], "weights": null});
    let (status, envelope) = call("POST", "/api/grids".to_string(), Some(grid.clone())).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], grid);
}
`
