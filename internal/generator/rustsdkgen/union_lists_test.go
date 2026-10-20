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
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestUnionListsSDKCrateBuildsAndRuns generates the Rust types crate and
// the Rust SDK crate of union-lists-api (sdktest.LoadUnionListsService),
// checks the SDK crate with cargo clippy and runs cargo test on it with
// unionListsSDKTest: a list, a list of lists, one value, a map and a map of
// lists of the union Shape, none paged, decode into its members, an
// element the union cannot read or a null element is an error, and a
// response that may be null is an Option, None for null. CARGO_TARGET_DIR
// is honored when set.
func TestUnionListsSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", sdktest.UnionListsService)
	sdkDir := filepath.Join(root, "sdk", "rust", sdktest.UnionListsService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	names := naming.Default()
	sdkOutput, err := Generate(apiOutput, names.RustSDKCrate(sdktest.UnionListsService), names.RustTypesCrate(sdktest.UnionListsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, names))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "union_lists.rs"), strings.ReplaceAll(unionListsSDKTest, "SDK_CRATE", crate))

	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// unionListsSDKTest is tests/union_lists.rs of the generated SDK crate,
// with SDK_CRATE replaced by the crate's module name.
const unionListsSDKTest = `use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::types::{Circle, Shape, Square};
use SDK_CRATE::{ClientConfig, UnionListsApiSdk};

/// Answers each operation with circles and squares mixed. In mode "bad"
/// one element of each list and map is a string, which neither member
/// reads; in mode "null" it is null, and so is the search for shapes.
fn serve(mode: &'static str) -> UnionListsApiSdk {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    thread::spawn(move || {
        let circle = json!({"radius": 1.0});
        let square = json!({"side": 2.0});
        let odd: Value = match mode {
            "bad" => json!("triangle"),
            "null" => Value::Null,
            _ => circle.clone(),
        };
        for stream in listener.incoming() {
            let mut stream = stream.unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                if header.trim_end().is_empty() {
                    break;
                }
            }
            let path = request_line.split_whitespace().nth(1).unwrap_or_default().to_string();
            let data = match path.as_str() {
                "/api/shapes" => json!([circle, odd]),
                "/api/shapes/rows" => json!([[circle], [square, odd]]),
                "/api/shape-index" => json!({"a": circle, "b": odd}),
                "/api/shape-groups" => json!({"round": [circle], "mixed": [square, odd]}),
                "/api/shape-search/c-1" => circle.clone(),
                "/api/shape-search" if mode == "null" => Value::Null,
                "/api/shape-search" => json!([square, odd]),
                "/api/shapes/none" | "/api/shape-search/none" => Value::Null,
                _ => square.clone(),
            };
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
    UnionListsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn circle() -> Shape {
    Shape::Circle(Circle { radius: 1.0 })
}

fn square() -> Shape {
    Shape::Square(Square { side: 2.0 })
}

#[tokio::test]
async fn each_element_of_a_list_decodes_through_the_union() {
    let sdk = serve("good");
    assert_eq!(sdk.shape.list_shapes(None).await.unwrap(), vec![circle(), circle()]);
    assert_eq!(sdk.shape.shape_rows(None).await.unwrap(), vec![vec![circle()], vec![square(), circle()]]);
    assert_eq!(sdk.shape.get_shape("s-1".to_string(), None).await.unwrap(), square());
}

#[tokio::test]
async fn each_value_of_a_map_decodes_through_the_union() {
    let sdk = serve("good");
    assert_eq!(
        sdk.shape.shapes_by_name(None).await.unwrap(),
        HashMap::from([("a".to_string(), circle()), ("b".to_string(), circle())])
    );
    assert_eq!(
        sdk.shape.shape_groups(None).await.unwrap(),
        HashMap::from([("round".to_string(), vec![circle()]), ("mixed".to_string(), vec![square(), circle()])])
    );
}

#[tokio::test]
async fn a_response_that_may_be_null_is_none_for_null() {
    let sdk = serve("null");
    assert_eq!(sdk.shape.find_shape("c-1".to_string(), None).await.unwrap(), Some(circle()));
    assert_eq!(sdk.shape.find_shape("none".to_string(), None).await.unwrap(), None);
    assert_eq!(sdk.shape.find_shapes(None).await.unwrap(), None);
    assert_eq!(serve("good").shape.find_shapes(None).await.unwrap(), Some(vec![square(), circle()]));
}

#[tokio::test]
async fn a_required_shape_that_answers_null_is_an_error() {
    assert!(serve("good").shape.get_shape("none".to_string(), None).await.is_err());
}

#[tokio::test]
async fn an_element_the_union_cannot_read_is_an_error() {
    for mode in ["bad", "null"] {
        let sdk = serve(mode);
        assert!(sdk.shape.list_shapes(None).await.is_err(), "list_shapes in mode {mode}");
        assert!(sdk.shape.shape_rows(None).await.is_err(), "shape_rows in mode {mode}");
        assert!(sdk.shape.shapes_by_name(None).await.is_err(), "shapes_by_name in mode {mode}");
        assert!(sdk.shape.shape_groups(None).await.is_err(), "shape_groups in mode {mode}");
    }
    assert!(serve("bad").shape.find_shapes(None).await.is_err());
}
`
