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

// bodyArgsAPI is apigen's body-args-api: nameShades and placePoints, PUTs
// whose body arguments are maps of an enum, of lists of a string scalar and
// of an object type, and removeTags, a DELETE whose arguments, an optional
// map of the enum among them, the route reads from the JSON body.
const bodyArgsAPI = "body-args-api"

// TestMapArgumentsAreHashMaps: a map argument is a HashMap of its value
// type, of a Vec of it for a map of lists, as the route takes it, inside an
// Option when optional.
func TestMapArgumentsAreHashMaps(t *testing.T) {
	_, apiOutput := loadBodyArgsAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := map[string]string{
		"shade_by_name":   "std::collections::HashMap<String, types::Shade>",
		"links_by_locale": "Option<std::collections::HashMap<String, Vec<types::NetworkUrl>>>",
		"point_by_name":   "std::collections::HashMap<String, types::Point>",
		"shade_by_label":  "Option<std::collections::HashMap<String, types::Shade>>",
	}
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			for _, arg := range endpoint.ScalarArgs {
				rustType, ok := want[arg.RustName]
				if !ok {
					continue
				}
				delete(want, arg.RustName)
				if !arg.IsMap || arg.RustType != rustType {
					t.Errorf("%s %s: IsMap %t, type %s; want true, %s", endpoint.MethodName, arg.RustName, arg.IsMap, arg.RustType, rustType)
				}
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("no map arguments %v", want)
	}
}

// TestMapArgumentsSDKCrateSendsJSONObjects generates the Rust types crate
// and the Rust SDK crate of body-args-api and runs mapArgsSDKTest on them:
// each map argument is sent as a JSON object in the body, a DELETE's
// included, and a value the route would refuse is refused before the
// request, every failure at the path the route reports it.
func TestMapArgumentsSDKCrateSendsJSONObjects(t *testing.T) {
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
	typesDir := filepath.Join(root, "types", "rust", bodyArgsAPI)
	sdkDir := filepath.Join(root, "sdk", "rust", bodyArgsAPI)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(bodyArgsAPI), naming.Default().RustTypesCrate(bodyArgsAPI), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "map_args.rs"), strings.ReplaceAll(mapArgsSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// loadBodyArgsAPI loads apigen's body-args-api and generates its API.
func loadBodyArgsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsAPI, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
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
use SDK_CRATE::{BodyArgsApiSdk, ClientConfig, SDKError};

/// Answers each request with the success envelope around the body's labels,
/// or around true for a body without them, and sends each request's method
/// and path, and its JSON body, back.
fn serve(requests: usize) -> (String, mpsc::Receiver<(String, Value)>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for _ in 0..requests {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let target = request_line.rsplit_once(' ').unwrap().0.to_string();
            let mut length = 0usize;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let line = line.trim_end();
                if line.is_empty() {
                    break;
                }
                if let Some((name, value)) = line.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            let body: Value = if length == 0 { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            let data = body.get("labels").cloned().unwrap_or(json!(true));
            sender.send((target, body)).unwrap();
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

fn sdk(base_url: String) -> BodyArgsApiSdk {
    BodyArgsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn map<V>(entries: Vec<(&str, V)>) -> HashMap<String, V> {
    entries.into_iter().map(|(key, value)| (key.to_string(), value)).collect()
}

#[tokio::test]
async fn a_map_is_sent_as_a_json_object_of_its_values() {
    let (base_url, requests) = serve(3);
    let sdk = sdk(base_url);

    let input = NameShadesInput {
        shade_by_name: map(vec![("a", types::Shade::Light), ("b", types::Shade::Dark)]),
        links_by_locale: Some(map(vec![("en", vec!["https://a.test".to_string()]), ("fr", Vec::new())])),
    };
    assert!(sdk.tag.name_shades("p1".to_string(), input, None).await.unwrap());
    assert_eq!(
        requests.recv().unwrap(),
        (
            "PUT /api/posts/p1/shade-names".to_string(),
            json!({"shadeByName": {"a": "light", "b": "dark"}, "linksByLocale": {"en": ["https://a.test"], "fr": []}})
        )
    );

    // {} is a value; an absent optional map is left out.
    let input = NameShadesInput { shade_by_name: HashMap::new(), links_by_locale: None };
    assert!(sdk.tag.name_shades("p2".to_string(), input, None).await.unwrap());
    assert_eq!(requests.recv().unwrap(), ("PUT /api/posts/p2/shade-names".to_string(), json!({"shadeByName": {}})));

    let input = PlacePointsInput {
        point_by_name: map(vec![("a", types::Point { x: 1.0, y: 2.0, pin_label: None }), ("b", types::Point { x: 0.5, y: -1.0, pin_label: None })]),
    };
    assert!(sdk.tag.place_points("p3".to_string(), input, None).await.unwrap());
    assert_eq!(
        requests.recv().unwrap(),
        ("PUT /api/posts/p3/points".to_string(), json!({"pointByName": {"a": {"x": 1.0, "y": 2.0}, "b": {"x": 0.5, "y": -1.0}}}))
    );
}

#[tokio::test]
async fn a_delete_sends_its_arguments_a_map_among_them_as_the_json_body() {
    let (base_url, requests) = serve(2);
    let sdk = sdk(base_url);

    let input = RemoveTagsInput {
        labels: vec!["a".to_string(), "b".to_string()],
        reason: Some("merged".to_string()),
        requester: None,
        shade_by_label: Some(map(vec![("a", types::Shade::Dark)])),
    };
    assert_eq!(sdk.tag.remove_tags("p1".to_string(), input, None).await.unwrap(), vec!["a", "b"]);
    assert_eq!(
        requests.recv().unwrap(),
        ("DELETE /api/posts/p1/tags".to_string(), json!({"labels": ["a", "b"], "reason": "merged", "shadeByLabel": {"a": "dark"}}))
    );

    let input = RemoveTagsInput { labels: vec!["c".to_string()], reason: None, requester: None, shade_by_label: None };
    assert_eq!(sdk.tag.remove_tags("p1".to_string(), input, None).await.unwrap(), vec!["c"]);
    assert_eq!(requests.recv().unwrap(), ("DELETE /api/posts/p1/tags".to_string(), json!({"labels": ["c"]})));
}

/// A value the route would refuse is refused before the request, with
/// every failure at the path the route reports it: name[key][i] for an
/// element of a list value, name[key].field inside an object. Nothing
/// listens at the base URL, so a request that was sent fails as a network
/// error instead.
#[tokio::test]
async fn a_map_value_the_route_would_refuse_is_refused_before_the_request() {
    let sdk = sdk("http://127.0.0.1:9".to_string());

    let input = NameShadesInput {
        shade_by_name: HashMap::new(),
        links_by_locale: Some(map(vec![
            ("en", vec!["https://a.test".to_string(), "http://b.test".to_string()]),
            ("fr", vec!["ftp://c.test".to_string()]),
        ])),
    };
    let err = sdk.tag.name_shades("p0".to_string(), input, None).await.unwrap_err();
    assert!(matches!(err, SDKError::Config(_)), "{err}");
    assert_eq!(
        err.to_string(),
        "invalid sdk configuration: argument validation failed: linksByLocale[en][1]: invalid format; linksByLocale[fr][0]: invalid format"
    );

    let input = PlacePointsInput {
        point_by_name: map(vec![
            ("c", types::Point { x: -2.5, y: 0.0, pin_label: None }),
            ("b", types::Point { x: 1.0, y: 0.0, pin_label: None }),
            ("a", types::Point { x: -1.0, y: 0.0, pin_label: None }),
        ]),
    };
    let err = sdk.tag.place_points("p0".to_string(), input, None).await.unwrap_err();
    assert!(matches!(err, SDKError::Config(_)), "{err}");
    assert_eq!(
        err.to_string(),
        "invalid sdk configuration: argument validation failed: pointByName[a].x: must be at least 0; pointByName[c].x: must be at least 0"
    );
}
`
