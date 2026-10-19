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
// unionListsSDKTest: a list, a list of lists and one value of the union
// Shape, none paged, decode into its members, and an element the union
// cannot read is an error. CARGO_TARGET_DIR is honored when set.
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
const unionListsSDKTest = `use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::types::{Circle, Shape, Square};
use SDK_CRATE::{ClientConfig, UnionListsApiSdk};

/// Answers each operation with circles and squares mixed, or, when bad,
/// each list with a string among them, which neither member reads.
fn serve(bad: bool) -> UnionListsApiSdk {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    thread::spawn(move || {
        let circle = json!({"radius": 1.0});
        let square = json!({"side": 2.0});
        let odd: Value = if bad { json!("triangle") } else { circle.clone() };
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
    let sdk = serve(false);
    assert_eq!(sdk.shape.list_shapes(None).await.unwrap(), vec![circle(), circle()]);
    assert_eq!(sdk.shape.shape_rows(None).await.unwrap(), vec![vec![circle()], vec![square(), circle()]]);
    assert_eq!(sdk.shape.get_shape("s-1".to_string(), None).await.unwrap(), square());
}

#[tokio::test]
async fn an_element_the_union_cannot_read_is_an_error() {
    let sdk = serve(true);
    assert!(sdk.shape.list_shapes(None).await.is_err());
    assert!(sdk.shape.shape_rows(None).await.is_err());
}
`
