package rustsdkgen

import (
	"os"
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
	ir "github.com/parable-work/superschematic/ir"
)

// loadQueryListsAPI loads query-lists-api (sdktest.LoadQueryListsService):
// list query parameters of an enum, a UUID scalar, an integer scalar,
// strings and booleans.
func loadQueryListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := sdktest.LoadQueryListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.QueryListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.QueryListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// writeQueryListsSDK writes the Rust SDK crate of query-lists-api to sdkDir
// against the types crate in typesDir.
func writeQueryListsSDK(t *testing.T, apiOutput *apigen.APIOutput, sdkDir, typesDir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(sdktest.QueryListsService), naming.Default().RustTypesCrate(sdktest.QueryListsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// TestListQueryParamItemChecks: a list whose items are text (an enum, a
// UUID, a string) has each item checked before the request, as the
// comma-separated value cannot carry an empty item or one with a comma or
// surrounding space; a list of numbers or booleans is checked item by item
// only for its rules. listMin bounds only a minimum above one, as an empty
// list is left out.
func TestListQueryParamItemChecks(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.ChecksQueryListItems {
		t.Error("ChecksQueryListItems is false, want true")
	}
	params := sdkOutput.Namespaces[0].Endpoints[0].QueryParams
	want := []struct {
		name                                  string
		isArray, itemIsText, items, checksMin bool
	}{
		{"ids", true, true, true, false},
		{"shades", true, true, true, true},
		{"ranks", true, false, true, false},
		{"codes", true, true, true, false},
		{"tags", true, true, true, false},
		{"flags", true, false, false, false},
		{"limit", false, false, false, false},
	}
	if len(params) != len(want) {
		t.Fatalf("query params = %+v", params)
	}
	for i, w := range want {
		got := params[i]
		if got.Name != w.name || got.IsArray != w.isArray || got.ItemIsText != w.itemIsText ||
			got.ChecksItems() != w.items || got.ChecksListMin() != w.checksMin {
			t.Errorf("parameter %d = %s (list %t, text %t, items %t, listMin %t), want %s (list %t, text %t, items %t, listMin %t)",
				i, got.Name, got.IsArray, got.ItemIsText, got.ChecksItems(), got.ChecksListMin(),
				w.name, w.isArray, w.itemIsText, w.items, w.checksMin)
		}
	}

	// An SDK without a list of text items has no check_query_list_item.
	endpoint := paginatedListEndpoint()
	endpoint.QueryParams = append(endpoint.QueryParams, apigen.Param{Name: "counts", Type: "number", IsArray: true})
	plain, err := Generate(&apigen.APIOutput{SchemaName: "plain-api", Endpoints: []apigen.EndpointInfo{endpoint}}, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if plain.ChecksQueryListItems {
		t.Error("ChecksQueryListItems is true for a list of numbers, want false")
	}
}

// TestWriteSDKGoldenQueryLists pins the namespace of the Rust SDK for
// query-lists-api, and checks that its runtime carries
// check_query_list_item. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestWriteSDKGoldenQueryLists -update
func TestWriteSDKGoldenQueryLists(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", sdktest.QueryListsService)
	writeQueryListsSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", sdktest.QueryListsService))

	name := "src/namespaces/post.rs"
	got, err := os.ReadFile(filepath.Join(sdkDir, name))
	if err != nil {
		t.Fatalf("read generated %s: %v", name, err)
	}
	goldenPath := filepath.Join("testdata", "golden", sdktest.QueryListsService, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(goldenPath); err != nil {
		t.Fatalf("read golden %s: %v (run with -update)", name, err)
	} else if string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept)", name)
	}

	runtimeSrc, err := os.ReadFile(filepath.Join(sdkDir, "src", "runtime.rs"))
	if err != nil {
		t.Fatalf("read runtime.rs: %v", err)
	}
	if !strings.Contains(string(runtimeSrc), "pub fn check_query_list_item(name: &str, index: usize, text: &str)") {
		t.Error("runtime.rs must carry check_query_list_item")
	}
}

// TestQueryListsSDKCrateBuildsAndRuns generates the Rust types crate and
// the Rust SDK crate of query-lists-api into a temp tree laid out as a
// build writes it, and runs cargo test on the SDK crate with
// queryListsSDKTest: a list is sent as one comma-separated value, an empty
// one is left out, and a required empty list or an item the value cannot
// carry fails before any request. CARGO_TARGET_DIR is honored when set.
func TestQueryListsSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadQueryListsAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", sdktest.QueryListsService)
	sdkDir := filepath.Join(root, "sdk", "rust", sdktest.QueryListsService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: sdktest.QueryListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput := writeQueryListsSDK(t, apiOutput, sdkDir, typesDir)

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

[patch.crates-io]
superscalar = { path = "`+filepath.ToSlash(paths.ScalarRust)+`" }
`)
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "query_lists.rs"), strings.ReplaceAll(queryListsSDKTest, "SDK_CRATE", crate))

	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = sdkDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+cargoTargetDir(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test on the generated SDK crate: %v\n%s", err, out)
	}
}

// queryListsSDKTest is tests/query_lists.rs of the generated SDK crate,
// with SDK_CRATE replaced by the crate's module name.
const queryListsSDKTest = `use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::json;
use SDK_CRATE::errors::SDKError;
use SDK_CRATE::namespaces::post::CountPostsQueryParams;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, QueryListsApiSdk};

const FIRST_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";
const SECOND_ID: &str = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e";

/// Answers requests with 7 in the success envelope, and sends each
/// request's query pairs back, decoded.
fn serve() -> (String, mpsc::Receiver<Vec<(String, String)>>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
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
            let target = request_line.split_whitespace().nth(1).unwrap_or_default().to_string();
            let url = reqwest::Url::parse(&format!("http://sdk.test{}", target)).unwrap();
            assert_eq!(url.path(), "/api/posts/count");
            sender.send(url.query_pairs().into_owned().collect()).unwrap();
            let payload = json!({"data": 7, "meta": {"requestId": "req-1"}}).to_string();
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

fn ids() -> Vec<types::IdentityUUID> {
    vec![FIRST_ID.parse().unwrap(), SECOND_ID.parse().unwrap()]
}

fn query() -> CountPostsQueryParams {
    CountPostsQueryParams {
        ids: ids(),
        shades: None,
        ranks: None,
        codes: None,
        tags: None,
        flags: None,
        limit: None,
    }
}

fn strings(items: &[&str]) -> Option<Vec<String>> {
    Some(items.iter().map(|item| item.to_string()).collect())
}

fn pairs(items: &[(&str, &str)]) -> Vec<(String, String)> {
    items.iter().map(|(key, value)| (key.to_string(), value.to_string())).collect()
}

fn config_message(result: Result<f64, SDKError>) -> String {
    match result {
        Err(SDKError::Config(message)) => message,
        other => panic!("expected a config error, got {:?}", other),
    }
}

#[tokio::test]
async fn a_list_is_one_comma_separated_value() {
    let (base_url, requests) = serve();
    let sdk = QueryListsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    // An Identity.UUID is sent in its canonical form, which the route parses.
    let ids_text = ids().iter().map(|id| id.to_string()).collect::<Vec<_>>().join(",");

    let mut full = query();
    full.shades = Some(vec![types::Shade::Light, types::Shade::Dark]);
    full.ranks = Some(vec![1, 100]);
    full.codes = strings(&["ab", "wxyz"]);
    full.tags = strings(&["a tag", "x"]);
    full.flags = Some(vec![true, false]);
    full.limit = Some(10.0);
    assert_eq!(sdk.post.count_posts(Some(&full), None).await.unwrap(), 7.0);
    assert_eq!(
        requests.recv().unwrap(),
        pairs(&[
            ("ids", &ids_text),
            ("shades", "light,dark"),
            ("ranks", "1,100"),
            ("codes", "ab,wxyz"),
            ("tags", "a tag,x"),
            ("flags", "true,false"),
            ("limit", "10"),
        ])
    );

    // An empty list is left out, as an absent one is: the route refuses a
    // present empty value. listMin bounds only a list that is sent.
    let mut empty = query();
    empty.shades = Some(vec![]);
    empty.ranks = Some(vec![]);
    empty.codes = Some(vec![]);
    empty.tags = Some(vec![]);
    empty.flags = Some(vec![]);
    sdk.post.count_posts(Some(&empty), None).await.unwrap();
    assert_eq!(requests.recv().unwrap(), pairs(&[("ids", &ids_text)]));
    sdk.post.count_posts(Some(&query()), None).await.unwrap();
    assert_eq!(requests.recv().unwrap(), pairs(&[("ids", &ids_text)]));
}

#[tokio::test]
async fn an_item_the_value_cannot_carry_fails_before_the_request() {
    let (base_url, requests) = serve();
    let sdk = QueryListsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();

    let mut no_ids = query();
    no_ids.ids = vec![];
    assert_eq!(config_message(sdk.post.count_posts(Some(&no_ids), None).await), "ids is required");

    for (tags, message) in [
        (&["a,b"][..], "tags[0] must not contain a comma or surrounding space"),
        (&["ok", " x"][..], "tags[1] must not contain a comma or surrounding space"),
        (&["x\t"][..], "tags[0] must not contain a comma or surrounding space"),
        (&[""][..], "tags[0] is required"),
        (&["ok", "  "][..], "tags[1] is required"),
    ] {
        let mut bad = query();
        bad.tags = strings(tags);
        assert_eq!(config_message(sdk.post.count_posts(Some(&bad), None).await), message, "tags {:?}", tags);
    }

    // The comma is refused before the item's own rules.
    let mut codes = query();
    codes.codes = strings(&["ab,cd"]);
    assert_eq!(
        config_message(sdk.post.count_posts(Some(&codes), None).await),
        "codes[0] must not contain a comma or surrounding space"
    );
    codes.codes = strings(&["a"]);
    assert_eq!(
        config_message(sdk.post.count_posts(Some(&codes), None).await),
        "each codes item must be at least 2 characters"
    );

    let mut shades = query();
    shades.shades = Some(vec![types::Shade::Dark]);
    assert_eq!(
        config_message(sdk.post.count_posts(Some(&shades), None).await),
        "shades must contain at least 2 items"
    );

    let mut ranks = query();
    ranks.ranks = Some(vec![0]);
    assert_eq!(config_message(sdk.post.count_posts(Some(&ranks), None).await), "each ranks item must be at least 1");

    assert!(requests.try_recv().is_err(), "a refused query must send no request");
}
`
