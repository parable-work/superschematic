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
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// bodyArgsService is apigen's body-args-api: nameShades, placePoints and
// removeTags (a DELETE) take maps of an enum, of lists of a string scalar
// and of an object type.
const bodyArgsService = "body-args-api"

func loadBodyArgsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestMapArgumentsAreHashMaps: a map body argument is typed as the route
// reads it, HashMap<String, T> or HashMap<String, Vec<T>>, as the types
// crate types a map field, and not as its value type; an optional one is
// an Option of that. Its field is renamed to the argument's name, which
// the route reads, as is every argument whose Rust name differs.
func TestMapArgumentsAreHashMaps(t *testing.T) {
	_, apiOutput := loadBodyArgsAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	args := map[string]ScalarArg{}
	for _, ns := range sdkOutput.Namespaces {
		for _, endpoint := range ns.Endpoints {
			for _, arg := range endpoint.ScalarArgs {
				args[endpoint.MethodName+"."+arg.Name] = arg
			}
		}
	}
	for name, want := range map[string]struct{ rustType, rename string }{
		"name_shades.shadeByName":   {"std::collections::HashMap<String, types::Shade>", "shadeByName"},
		"name_shades.linksByLocale": {"Option<std::collections::HashMap<String, Vec<types::NetworkUrl>>>", "linksByLocale"},
		"place_points.pointByName":  {"std::collections::HashMap<String, types::Point>", "pointByName"},
		"remove_tags.shadeByLabel":  {"Option<std::collections::HashMap<String, types::Shade>>", "shadeByLabel"},
		"remove_tags.linksByLocale": {"Option<std::collections::HashMap<String, Vec<types::NetworkUrl>>>", "linksByLocale"},
		"remove_tags.labels":        {"Vec<String>", ""},
	} {
		if got := args[name]; got.RustType != want.rustType || got.SerdeRename != want.rename {
			t.Errorf("%s is %q renamed %q, want %q renamed %q", name, got.RustType, got.SerdeRename, want.rustType, want.rename)
		}
	}
}

// TestMapArgumentRulesCheckNoValue: the namespace template checks a single
// value's rules on its text and a list's bounds on its length, neither of
// which a map has. A map's rules apply to each value, as to each list
// element, which the SDK leaves to the route, and list bounds do not bound
// a map, so a map argument emits no check.
func TestMapArgumentRulesCheckNoValue(t *testing.T) {
	minimum := 2
	endpoint := convertEndpoint(apigen.EndpointInfo{
		Name:   "nameThings",
		Method: "PUT",
		Path:   "/api/things",
		ScalarArgs: []apigen.ScalarArg{
			{Name: "codeByName", Type: "string", IsMap: true, Required: true, ValidatePattern: "^[a-z]+$", ValidateMinLength: &minimum},
			{Name: "tagsByName", Type: "string", IsMap: true, IsArray: true, ValidateListMin: &minimum},
		},
	}, false, "")
	for _, arg := range endpoint.ScalarArgs {
		if arg.NeedsGeneratedValidation() || arg.NeedsRegex() {
			t.Errorf("%s emits checks (validation %t, regex %t), want none", arg.Name, arg.NeedsGeneratedValidation(), arg.NeedsRegex())
		}
	}
	if endpoint.NeedsRegex() {
		t.Error("endpoint.NeedsRegex() = true, want false")
	}
}

// TestMapArgsSDKCrateBuildsAndRuns generates the Rust types crate and the
// Rust SDK crate of body-args-api, checks the SDK crate with cargo clippy
// and runs mapArgsSDKTest with cargo test (cargoClippyAndTest): each map
// argument of nameShades, placePoints and removeTags crosses a local HTTP
// server as the JSON object the route reads, {} included, and an optional
// one that is None is left out.
func TestMapArgsSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadBodyArgsAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", bodyArgsService)
	sdkDir := filepath.Join(root, "sdk", "rust", bodyArgsService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: bodyArgsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(bodyArgsService), naming.Default().RustTypesCrate(bodyArgsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

[patch.crates-io]
superscalar = { path = "`+filepath.ToSlash(paths.ScalarRust)+`" }
`)
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "map_args.rs"), strings.ReplaceAll(mapArgsSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// mapArgsSDKTest is tests/map_args.rs of the generated SDK crate, with
// SDK_CRATE replaced by the crate's module name.
const mapArgsSDKTest = `use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::namespaces::tag::{NameShadesInput, PlacePointsInput, RemoveTagsInput};
use SDK_CRATE::types;
use SDK_CRATE::{BodyArgsApiSdk, ClientConfig};

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

fn links(entries: &[(&str, &[&str])]) -> HashMap<String, Vec<types::NetworkUrl>> {
    entries
        .iter()
        .map(|(locale, urls)| (locale.to_string(), urls.iter().map(|url| url.to_string()).collect()))
        .collect()
}

#[tokio::test]
async fn maps_cross_the_wire_as_json_objects() {
    let (base_url, requests) = serve(vec![json!(true), json!(true), json!(true), json!(["a"])]);
    let sdk = BodyArgsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();

    let named = sdk
        .tag
        .name_shades(
            "p1".to_string(),
            NameShadesInput {
                shade_by_name: HashMap::from([
                    ("a".to_string(), types::Shade::Light),
                    ("b".to_string(), types::Shade::Dark),
                ]),
                links_by_locale: Some(links(&[("en", &["https://a.test"]), ("fr", &[])])),
            },
            None,
        )
        .await
        .unwrap();
    assert!(named);
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path.as_str()), ("PUT", "/api/posts/p1/shade-names"));
    assert_eq!(
        body,
        json!({"shadeByName": {"a": "light", "b": "dark"}, "linksByLocale": {"en": ["https://a.test"], "fr": []}})
    );

    // {} is a value of a required map; an optional map that is None is
    // left out.
    sdk.tag
        .name_shades("p1".to_string(), NameShadesInput { shade_by_name: HashMap::new(), links_by_locale: None }, None)
        .await
        .unwrap();
    let (_, _, body) = requests.recv().unwrap();
    assert_eq!(body, json!({"shadeByName": {}}));

    sdk.tag
        .place_points(
            "p1".to_string(),
            PlacePointsInput {
                point_by_name: HashMap::from([("origin".to_string(), types::Point { x: 0.0, y: 0.0 })]),
            },
            None,
        )
        .await
        .unwrap();
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path.as_str()), ("PUT", "/api/posts/p1/points"));
    assert_eq!(body, json!({"pointByName": {"origin": {"x": 0.0, "y": 0.0}}}));

    let removed = sdk
        .tag
        .remove_tags(
            "p1".to_string(),
            RemoveTagsInput {
                reason: "spam".to_string(),
                labels: vec!["a".to_string()],
                shade_by_label: Some(HashMap::from([("a".to_string(), types::Shade::Dark)])),
                links_by_locale: Some(links(&[("en", &["https://a.test"])])),
                audit: None,
                notes: None,
                vector: None,
            },
            None,
        )
        .await
        .unwrap();
    assert_eq!(removed, vec!["a".to_string()]);
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!((method.as_str(), path.as_str()), ("DELETE", "/api/posts/p1/tags"));
    assert_eq!(
        body,
        json!({"reason": "spam", "labels": ["a"], "shadeByLabel": {"a": "dark"}, "linksByLocale": {"en": ["https://a.test"]}})
    );
}

#[test]
fn a_null_map_value_does_not_decode() {
    assert!(serde_json::from_value::<HashMap<String, types::Shade>>(json!({"a": null})).is_err());
    assert!(serde_json::from_value::<HashMap<String, Vec<String>>>(json!({"en": null})).is_err());
}
`
