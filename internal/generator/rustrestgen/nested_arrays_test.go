package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	output, err := generateFrom(schema, apiSource{}, Options{
		SchemaName: nestedArraysService,
		TypesCrate: naming.Default().RustTypesCrate(nestedArraysService),
		TypesDir:   typesDir,
		OutputDir:  outputDir,
		Clock:      nestedArraysClock,
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
// fixture-nested-arrays-api: an input with lists of lists, a list-of-lists
// body argument decoded into Vec<Vec<String>>, and a list-of-lists result.
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
// crate of fixture-nested-arrays-api, with grid.paint, grid.cell,
// grid.placeOrder and grid.importGrid added, with nestedArraysRouterTest:
// a list-of-lists body argument reaches the implementation typed, each
// element checked at its path; a list-of-lists result comes back in the
// success envelope; a path parameter reaches it decoded exactly once; a
// UUID, number or enum that does not parse, and an object that breaks its
// type, are 400 problems naming the parameter; an input is validated, its
// undeclared keys refused and its field errors returned in `errors`; and the
// manual grid.importGrid has no trait method and no route until the service
// adds one. problemsRouterTest checks the wire contract on the same crate.
func TestNestedArraysAPICrateBuildsAndRoutes(t *testing.T) {
	schema := loadNestedArraysSchema(t, true)
	for _, add := range []func(*ir.Schema) error{sdktest.AddCellOperation, sdktest.AddPlaceOrderOperation} {
		if err := add(schema); err != nil {
			t.Fatal(err)
		}
	}
	addImportOperation(t, schema)
	cargoTestAPICrate(t, nestedArraysService, schema, "nested_arrays", nestedArraysRouterTest, func(apiDir string, output *APIOutput) error {
		test := strings.NewReplacer("API_CRATE", strings.ReplaceAll(output.CrateName, "-", "_"), "RUNTIME_CRATE", output.RuntimeCrateIdent).Replace(problemsRouterTest)
		if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(apiDir, "tests", "problems.rs"), []byte(test), 0o644)
	})
}

// cargoTestAPICrate generates the Rust types crate and the Rust API crate of
// schema into a temp tree laid out as a build writes it, adds test as
// tests/<testName>.rs of the API crate, with API_CRATE and RUNTIME_CRATE
// replaced by the crates' module names, and runs cargo test on the API
// crate, after cargo clippy with warnings denied. The types crate resolves
// superscalar from the checkout scripts/superscalar-dep.sh stands up. A
// test may pause tokio's clock
// (#[tokio::test(start_paused = true)]). Each extra runs on the written
// crate before the build, to add files of its own. CARGO_TARGET_DIR is
// honored when set.
func cargoTestAPICrate(t *testing.T, service string, schema *ir.Schema, testName, test string, extras ...func(apiDir string, output *APIOutput) error) {
	t.Helper()
	cargoTestAPICrateWith(t, service, schema, testName, test, cargoOptions{}, extras...)
}

// cargoOptions are what a test of the API crate adds to its build: the
// crate's features, which clippy and cargo test turn on, and lines of the
// manifest's [dev-dependencies].
type cargoOptions struct {
	features        []string
	devDependencies string
}

// cargoTestAPICrateWith is cargoTestAPICrate with options.
func cargoTestAPICrateWith(t *testing.T, service string, schema *ir.Schema, testName, test string, options cargoOptions, extras ...func(apiDir string, output *APIOutput) error) {
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
	output, err := generateFrom(schema, apiSource{authDB: authDBOf(t, schema)}, Options{
		SchemaName: service,
		TypesCrate: naming.Default().RustTypesCrate(service),
		TypesDir:   typesDir,
		OutputDir:  apiDir,
		Clock:      nestedArraysClock,
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
	for _, extra := range extras {
		if err := extra(apiDir, output); err != nil {
			t.Fatal(err)
		}
	}

	cargoToml, err := os.ReadFile(filepath.Join(apiDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cargoToml = append(cargoToml, []byte(`
[dev-dependencies]
tokio = { version = "1.52.2", features = ["test-util"] }
tower = { version = "0.5", features = ["util"] }
`+options.devDependencies+`
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
	// The crate is linted as a service's CI would lint it, with warnings
	// denied, before its tests run.
	var features []string
	if len(options.features) > 0 {
		features = []string{"--features", strings.Join(options.features, ",")}
	}
	for _, args := range [][]string{
		append(append([]string{"clippy", "--quiet", "--all-targets"}, features...), "--", "-D", "warnings"),
		append([]string{"test", "--quiet"}, features...),
	} {
		cmd := exec.Command(cargoPath, args...)
		cmd.Dir = apiDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s on the generated API crate: %v\n%s", args[0], err, out)
		}
	}
}

// nestedArraysRouterTest is tests/nested_arrays.rs of the generated API
// crate, with API_CRATE and RUNTIME_CRATE replaced by the crates' module
// names. Its implementation echoes each argument back.
const nestedArraysRouterTest = `use std::sync::Arc;

use API_CRATE::{
    build_router, types, GridCellArgs, GridGetGridArgs, GridGridLabelsArgs, GridImplementation, GridPaintArgs,
    GridPlaceOrderArgs, GridReplaceLabelsArgs, GridSaveGridArgs, Implementations,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use axum::routing::post;
use axum::{Json, Router};
use serde_json::{json, Value};
use tower::ServiceExt;

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(json!(text)).unwrap()
}

fn grid(id: types::IdentityUUID, labels: Vec<Vec<String>>) -> types::GridView {
    types::GridView { id, labels, shades: vec![], polygons: vec![], weights: None }
}

struct Echo;

#[async_trait]
impl GridImplementation for Echo {
    async fn save_grid(&self, _ctx: RequestContext, args: GridSaveGridArgs) -> Result<types::GridView, ApiError> {
        let input = args.input;
        Ok(types::GridView {
            id: uuid(GRID_ID),
            labels: input.labels,
            shades: input.shades,
            polygons: input.polygons,
            weights: input.weights,
        })
    }
    async fn replace_labels(&self, _ctx: RequestContext, args: GridReplaceLabelsArgs) -> Result<types::GridView, ApiError> {
        Ok(grid(args.id, args.labels))
    }
    async fn paint(&self, _ctx: RequestContext, args: GridPaintArgs) -> Result<Vec<Vec<types::Point>>, ApiError> {
        assert!(args.shades.iter().flatten().all(|shade| matches!(shade, types::Shade::Light | types::Shade::Dark)));
        Ok(args.polygons.unwrap_or_default())
    }
    async fn get_grid(&self, _ctx: RequestContext, _args: GridGetGridArgs) -> Result<types::GridView, ApiError> {
        Err(ApiError::not_implemented("get_grid is not implemented"))
    }
    async fn grid_labels(&self, _ctx: RequestContext, args: GridGridLabelsArgs) -> Result<Vec<Vec<String>>, ApiError> {
        let rows = args.limit.map_or(3, |limit| limit as usize);
        let labels = vec![vec!["a".to_string(), "b".to_string()], vec![], vec!["c".to_string()]];
        Ok(labels.into_iter().take(rows).collect())
    }
    async fn cell(&self, _ctx: RequestContext, args: GridCellArgs) -> Result<String, ApiError> {
        Ok(args.label)
    }
    async fn place_order(&self, _ctx: RequestContext, args: GridPlaceOrderArgs) -> Result<types::GridView, ApiError> {
        let products = args.input.lines.into_iter().map(|line| line.product_id).collect();
        Ok(grid(uuid(GRID_ID), vec![products, args.input.gift_codes.unwrap_or_default()]))
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

/// The refusal of a parameter: 400, its details, and the details' errors.
fn assert_refusal(status: StatusCode, body: &Value, details: Value) {
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["code"], "bad_request", "{body}");
    assert_eq!(body["details"], details, "{body}");
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
    assert_eq!(status, StatusCode::OK, "{envelope}");
    assert_eq!(envelope["data"]["labels"], json!([["a", "b"], []]));
    // The UUID comes back in its canonical (base62) form.
    assert_eq!(envelope["data"]["id"], uuid(GRID_ID).to_string());
    assert!(envelope["meta"]["requestId"].is_string());
}

#[tokio::test]
async fn a_list_of_lists_element_is_checked_at_its_path() {
    let uri = format!("/api/grids/{GRID_ID}/labels");
    let (status, body) = call("PUT", uri.clone(), Some(json!({"labels": [["a", 1]]}))).await;
    assert_refusal(status, &body, json!({"location": "body", "parameter": "labels", "path": "labels[0][1]",
        "reason": "expected a string", "errors": [{"validator": "type", "message": "expected a string"}]}));
    let (status, body) = call("PUT", uri.clone(), Some(json!({"labels": [["a"], null]}))).await;
    assert_refusal(status, &body, json!({"location": "body", "parameter": "labels", "path": "labels[1]",
        "reason": "required field", "errors": [{"validator": "required", "message": "required field"}]}));
    let (status, body) = call("PUT", uri, Some(json!({}))).await;
    assert_refusal(status, &body, json!({"location": "body", "parameter": "labels", "reason": "required"}));
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
    assert_eq!(status, StatusCode::OK, "{envelope}");
    assert_eq!(envelope["data"], polygons);
}

#[tokio::test]
async fn an_enum_or_object_element_that_breaks_its_type_is_refused() {
    let uri = format!("/api/grids/{GRID_ID}/paint");
    let (status, body) = call("PUT", uri.clone(), Some(json!({"shades": [["light", "pink"]]}))).await;
    assert_refusal(status, &body, json!({"location": "body", "parameter": "shades", "path": "shades[0][1]",
        "reason": "expected one of light, dark"}));
    let (status, body) = call("PUT", uri.clone(), Some(json!({"shades": [], "polygons": [[{"x": 1.0}]]}))).await;
    assert_refusal(status, &body, json!({"location": "body", "parameter": "polygons", "path": "polygons[0][0]",
        "reason": "does not match the declared type"}));
    // A key Point does not declare is refused, as the TypeScript router's
    // strict parser refuses it.
    let (status, body) = call("PUT", uri, Some(json!({"shades": [], "polygons": [[{"x": 1.0, "y": 2.0, "z": 3.0}]]}))).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["details"]["path"], "polygons[0][0]");
}

#[tokio::test]
async fn a_path_or_query_value_that_does_not_parse_is_refused() {
    let (status, body) = call("GET", "/api/grids/not-a-uuid/labels".to_string(), None).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["details"]["location"], "path");
    assert_eq!(body["details"]["parameter"], "id");
    assert_eq!(body["details"]["reason"], "expected a UUID");
    assert!(body["details"]["errors"].as_array().is_some_and(|errors| !errors.is_empty()), "{body}");

    let (status, body) = call("GET", format!("/api/grids/{GRID_ID}/labels?limit=many"), None).await;
    assert_refusal(status, &body, json!({"location": "query", "parameter": "limit", "reason": "expected a number"}));
}

// The label is sent encoded once, as encodeURIComponent writes it, and in
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
        ("a%3Fb%23c", "a?b#c"),
    ] {
        let (status, envelope) = call("GET", format!("/api/grids/{GRID_ID}/cells/{segment}"), None).await;
        assert_eq!(status, StatusCode::OK, "{segment}: {envelope}");
        assert_eq!(envelope["data"], want, "{segment}");
    }
    for segment in ["%", "100%", "%ZZ", "a%2", "%E9", "%C3%28"] {
        let (status, envelope) = call("GET", format!("/api/grids/{GRID_ID}/cells/{segment}"), None).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{segment}");
        assert_eq!(envelope["code"], "bad_request", "{segment}");
    }
}

#[tokio::test]
async fn an_input_type_with_lists_of_lists_passes_through() {
    let grid = json!({"labels": [["a"], []], "shades": [["dark"]], "polygons": [[]], "weights": null});
    let (status, envelope) = call("POST", "/api/grids".to_string(), Some(grid)).await;
    assert_eq!(status, StatusCode::OK, "{envelope}");
    assert_eq!(envelope["data"]["labels"], json!([["a"], []]));
    assert_eq!(envelope["data"]["shades"], json!([["dark"]]));
    assert_eq!(envelope["data"]["polygons"], json!([[]]));
    assert_eq!(envelope["data"].get("weights"), None);
}

// PlaceOrderInput bounds its lines and holds objects with rules of their
// own: the router validates the input as the generated validators do, and
// refuses a top-level key the type does not declare.
#[tokio::test]
async fn an_input_is_validated_and_its_unknown_keys_refused() {
    let order = json!({"lines": [{"productId": "abc", "quantity": 2}], "shipTo": {"postalCode": "12345"}, "giftCodes": ["G1"]});
    let (status, envelope) = call("POST", "/api/orders".to_string(), Some(order)).await;
    assert_eq!(status, StatusCode::OK, "{envelope}");
    assert_eq!(envelope["data"]["labels"], json!([["abc"], ["G1"]]));

    let invalid = json!({"lines": [{"productId": "a", "quantity": 0}], "shipTo": {"postalCode": "x"}});
    let (status, body) = call("POST", "/api/orders".to_string(), Some(invalid)).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["detail"], "Request body does not match the declared input");
    assert_eq!(body["details"], json!({"location": "body", "reason": "validation failed"}));
    assert_eq!(body["errors"]["lines[0]"]["productId"][0]["validator"], "minLength", "{body}");
    assert_eq!(body["errors"]["lines[0]"]["quantity"][0]["validator"], "min", "{body}");
    assert_eq!(body["errors"]["shipTo"]["postalCode"][0]["validator"], "pattern", "{body}");

    let extra = json!({"lines": [{"productId": "abc", "quantity": 1}], "shipTo": {"postalCode": "12345"}, "coupon": "x"});
    let (status, body) = call("POST", "/api/orders".to_string(), Some(extra)).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["details"], json!({"location": "body", "reason": "unknown fields: coupon"}));
    assert_eq!(body["errors"], json!({"coupon": [{"validator": "unknown", "message": "unknown field"}]}));

    let (status, body) = call("POST", "/api/orders".to_string(), None).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["detail"], "Request body is required");
    let (status, body) = call("POST", "/api/orders".to_string(), Some(json!([]))).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(body["details"]["reason"], "expected an object");
}
`
