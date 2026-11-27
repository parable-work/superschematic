package rustsdkgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// graphService is the API whose @graphOperations set the loader fills
// with the operations of fixture-version-graph-db's Recipe graph (D138),
// and graphDB its database.
const (
	graphService = "fixture-graph-api"
	graphDB      = "fixture-version-graph-db"
)

// loadGraphAPI loads fixture-graph-api and its database with one registry,
// and generates the API's output.
func loadGraphAPI(t *testing.T) (*ir.Schema, *ir.Schema, *registry.Registry, *apigen.APIOutput) {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, graphDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, graphService), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   graphService,
		ModulePath:   "example.com/schemas/api/" + graphService,
		TypesModule:  "example.com/schemas/types/go/" + graphService,
		Dependencies: map[string]*ir.Schema{graphDB: db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, reg, apiOutput
}

// TestGraphOperationsSDKCrateBuildsAndCalls builds the Rust SDK crate of
// fixture-graph-api over the types crates of the API and its database,
// which holds the Recipe graph's facade over the version graph's Rust
// engine, with clippy's warnings denied, and runs a test in it against a
// local server: the types the loader adds for a @graphOperations set
// (D138) compile, a save sends each kind's edits as the route reads them,
// and a branch, a save, a tree and a diff answer typed.
func TestGraphOperationsSDKCrateBuildsAndCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, db, reg, apiOutput := loadGraphAPI(t)
	root := testpaths.TempDir(t)
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{graphDB, db, nil},
		{graphService, schema, map[string]*ir.Schema{graphDB: db}},
	} {
		crates := map[string]string{}
		for dep := range tc.deps {
			crates[dep] = naming.Default().RustTypesCrate(dep)
		}
		output, err := rustgen.Generate(tc.schema, rustgen.Options{
			SchemaName: tc.name, Dependencies: tc.deps, DependencyCrates: crates, Registry: reg, Clock: nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("rustgen.Generate %s: %v", tc.name, err)
		}
		dir := filepath.Join(root, "types", "rust", tc.name)
		// The database's crate holds the graph's facade, over the version
		// graph's Rust engine at its place in the checkout.
		if err := rustgen.SetVersionGraphPath(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := rustgen.WriteTypes(output, dir); err != nil {
			t.Fatalf("rustgen.WriteTypes %s: %v", tc.name, err)
		}
	}
	sdkDir := filepath.Join(root, "sdk", "rust", graphService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(graphService), naming.Default().RustTypesCrate(graphService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", graphService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "graph_operations.rs"), strings.ReplaceAll(graphOperationsSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// graphOperationsSDKTest is tests/graph_operations.rs of the generated SDK
// crate, with SDK_CRATE replaced by the crate's module name.
const graphOperationsSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::namespaces::recipe::DiffQueryParams;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureGraphApiSdk};

const RECIPE: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";
const REF: &str = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f";
const PART: &str = "2d3e4f5a-6b7c-4d8e-9f0a-1b2c3d4e5f60";
const COMMIT: &str = "3e4f5a6b-7c8d-4e9f-8a1b-2c3d4e5f6071";

/// Answers one request per canned value with the success envelope around
/// it, and sends each request's method, path and JSON body back.
fn serve(responses: Vec<Value>) -> (String, mpsc::Receiver<(String, String, Value)>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for data in responses {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let mut parts = request_line.split_whitespace();
            let method = parts.next().unwrap_or_default().to_string();
            let path = parts.next().unwrap_or_default().to_string();
            let mut length = 0usize;
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                let header = header.trim_end();
                if header.is_empty() {
                    break;
                }
                if let Some((name, value)) = header.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            let body = if body.is_empty() { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            sender.send((method, path, body)).unwrap();
            let payload = json!({"data": data, "meta": {"requestId": "req-1"}}).to_string();
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                payload.len(),
                payload
            )
            .unwrap();
        }
    });
    (base_url, receiver)
}

/// A ref as the server answers one.
fn ref_value(version: i64) -> Value {
    json!({"id": REF, "root": RECIPE, "parent": COMMIT, "name": "draft", "sealed": false, "discarded": false, "version": version})
}

/// Rows of no kind but parts, as a tree and a save's rows hold them.
fn rows(parts: Value) -> Value {
    json!({"cover": [], "ingredient": [], "note": [], "part": parts, "step": [], "tasting": [], "utensil": []})
}

#[tokio::test]
async fn the_graph_operations_send_and_answer_their_types() {
    let part = json!({"entityKey": PART, "rank": "V", "heading": "Intro"});
    let mut tree = rows(json!([part.clone()]));
    tree["contentHash"] = json!("abc");
    tree["findings"] = json!([{"code": "missing_parent", "kind": "part", "entityKey": PART, "message": "no parent"}]);
    let row = r#"{"heading":"Intro"}"#;
    let (base_url, requests) = serve(vec![
        ref_value(1),
        json!({"ref": ref_value(2), "saved": rows(json!([part]))}),
        tree,
        json!([{"kind": "part", "entityKey": PART, "operation": "ADD", "row": row}]),
    ]);
    let sdk = FixtureGraphApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let recipe: types::IdentityUUID = RECIPE.parse().unwrap();
    let draft: types::IdentityUUID = REF.parse().unwrap();
    let key: types::IdentityUUID = PART.parse().unwrap();

    let branched = sdk
        .recipe
        .branch(recipe, types::RecipeGraphBranchInput { from: None, name: "draft".to_string() }, None)
        .await
        .unwrap();
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path), ("POST", format!("/api/recipes/{}/refs", recipe)));
    // An unset from is left out of the body.
    assert_eq!(body, json!({"name": "draft"}));
    assert_eq!(branched.id, draft);
    assert_eq!(branched.version, 1);
    assert!(branched.head.is_none() && !branched.sealed);

    let edits = types::RecipeGraphEdits {
        cover: None,
        ingredient: None,
        note: None,
        part: Some(types::RecipeGraphPartRowEdits {
            upsert: Some(vec![types::RecipeGraphPartRowInput {
                entity_key: None,
                parent_key: None,
                rank: "V".to_string(),
                heading: "Intro".to_string(),
            }]),
            delete: None,
            unset: None,
        }),
        step: Some(types::RecipeGraphStepRowEdits {
            upsert: None,
            delete: Some(vec![key]),
            unset: None,
        }),
        tasting: None,
        utensil: None,
    };
    let saved = sdk
        .recipe
        .save(recipe, draft, types::RecipeGraphSaveInput { version: 1, edits }, None)
        .await
        .unwrap();
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path), ("PUT", format!("/api/recipes/{}/refs/{}/edits", recipe, draft)));
    assert_eq!(body["version"], json!(1));
    assert_eq!(body["edits"]["part"]["upsert"], json!([{"rank": "V", "heading": "Intro"}]));
    assert_eq!(body["edits"]["step"]["delete"].as_array().map(Vec::len), Some(1));
    assert!(body["edits"].get("cover").is_none(), "a kind with no edits is left out: {body}");
    assert_eq!(saved.r#ref.version, 2);
    assert_eq!(saved.saved.part.len(), 1);
    assert_eq!(saved.saved.part[0].entity_key, key);

    let composed = sdk.recipe.compose(recipe, draft, None).await.unwrap();
    let (method, path, _) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path), ("GET", format!("/api/recipes/{}/refs/{}/tree", recipe, draft)));
    assert_eq!(composed.part[0].heading, "Intro");
    assert_eq!(composed.content_hash, "abc");
    assert_eq!(composed.findings[0].code, "missing_parent");

    let commit: types::IdentityUUID = COMMIT.parse().unwrap();
    let changes = sdk
        .recipe
        .diff(recipe, Some(&DiffQueryParams { from: commit, to: commit }), None)
        .await
        .unwrap();
    let (method, path, _) = requests.recv().unwrap();
    assert_eq!(method, "GET");
    assert!(path.starts_with(&format!("/api/recipes/{}/diff?", recipe)), "{path}");
    assert_eq!(changes[0].kind, types::RecipeGraphKind::Part);
    assert_eq!(changes[0].operation, types::RecipeGraphChangeOperation::Add);
    assert_eq!(changes[0].row.as_deref(), Some(row));
}
`
