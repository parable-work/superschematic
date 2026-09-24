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

// TestNestedArraysAPICrateBuildsAndRoutes generates the Rust types crate
// and the Rust API crate of fixture-nested-arrays-api, with grid.paint
// added, into a temp tree laid out as a build writes it, and runs cargo
// test on the API crate with nestedArraysRouterTest: a list-of-lists body
// reaches the implementation as nested JSON arrays and a list-of-lists
// result comes back in the success envelope. The types crate resolves
// superscalar from the checkout scripts/superscalar-dep.sh stands up.
// CARGO_TARGET_DIR is honored when set.
func TestNestedArraysAPICrateBuildsAndRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	paths := testpaths.Local(t)
	schema := loadNestedArraysSchema(t, true)

	// The http-runtime dependency is a path relative to the crate, which
	// cargo resolves from the real directory (macOS /var is /private/var).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "rust", nestedArraysService)
	apiDir := filepath.Join(root, "api", nestedArraysService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: nestedArraysService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	output := generateNestedArraysAPI(t, schema, typesDir, apiDir)
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

[patch.crates-io]
superscalar = { path = "`+filepath.ToSlash(paths.ScalarRust)+`" }
`)...)
	if err := os.WriteFile(filepath.Join(apiDir, "Cargo.toml"), cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	test := strings.NewReplacer("API_CRATE", strings.ReplaceAll(output.CrateName, "-", "_"), "RUNTIME_CRATE", output.RuntimeCrateIdent).Replace(nestedArraysRouterTest)
	if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "tests", "nested_arrays.rs"), []byte(test), 0o644); err != nil {
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

async fn call(method: &str, uri: String, body: Option<Value>) -> (StatusCode, Value) {
    let router = build_router(Implementations { grid: Arc::new(Echo) });
    let request = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    let request = match body {
        Some(body) => request.body(Body::from(body.to_string())).unwrap(),
        None => request.body(Body::empty()).unwrap(),
    };
    let response = router.oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, serde_json::from_slice(&bytes).unwrap())
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

#[tokio::test]
async fn an_input_type_with_lists_of_lists_passes_through() {
    let grid = json!({"labels": [["a"], []], "shades": [["dark"]], "polygons": [[]], "weights": null});
    let (status, envelope) = call("POST", "/api/grids".to_string(), Some(grid.clone())).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], grid);
}
`
