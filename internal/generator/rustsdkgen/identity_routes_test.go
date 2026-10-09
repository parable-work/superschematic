package rustsdkgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// userRoutesService is the API whose @userSessions and @userAdministration
// sets the loader fills from fixture-user-model-db (D50).
const userRoutesService = "fixture-user-routes-api"

// loadUserRoutesAPI loads fixture-user-routes-api: the 18 operations of
// the user model's route sets and the project's greeting.greet.
func loadUserRoutesAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesService))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: userRoutesService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// writeUserRoutesSDK writes the Rust SDK crate of fixture-user-routes-api
// to sdkDir against the types crate in typesDir.
func writeUserRoutesSDK(t *testing.T, apiOutput *apigen.APIOutput, sdkDir, typesDir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(userRoutesService), naming.Default().RustTypesCrate(userRoutesService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// userRoutesPasswordTools are the operations whose input carries a
// password, which the user model's sets hide from an agent.
var userRoutesPasswordTools = []string{"login", "register", "changePassword", "createUser", "setUserPassword"}

// TestUserRoutesAreSDKMethodsAndTools: the Rust SDK treats the user
// model's operations as any other, though the identity runtime serves
// them (D50): each has a method, and each is a tool, those carrying a
// password hidden with the user model's reason.
func TestUserRoutesAreSDKMethodsAndTools(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", userRoutesService)
	sdkOutput := writeUserRoutesSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", userRoutesService))

	var methods []string
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			methods = append(methods, namespace.Name+"."+endpoint.MethodName)
		}
	}
	slices.Sort(methods)
	want := []string{
		"account-admin.create_role", "account-admin.create_user", "account-admin.delete_role",
		"account-admin.disable_user", "account-admin.enable_user", "account-admin.get_user",
		"account-admin.grant_role", "account-admin.list_roles", "account-admin.list_users",
		"account-admin.revoke_role", "account-admin.set_user_password", "account-admin.update_role",
		"account.capabilities", "account.change_password", "account.login", "account.logout",
		"account.me", "account.register",
		"greeting.greet",
	}
	if !slices.Equal(methods, want) {
		t.Errorf("SDK methods = %v, want %v", methods, want)
	}

	data, err := os.ReadFile(filepath.Join(sdkDir, "tools", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Tools []struct {
			Name       string `json:"name"`
			Namespace  string `json:"namespace"`
			MethodName string `json:"methodName"`
			MCP        *struct {
				Hidden       bool   `json:"hidden"`
				HiddenReason string `json:"hiddenReason"`
			} `json:"mcp"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("tools/schema.json: %v", err)
	}
	var tools []string
	for _, tool := range document.Tools {
		tools = append(tools, tool.Namespace+"."+tool.MethodName)
		_, operation, _ := strings.Cut(tool.Name, ".")
		hidden := tool.MCP != nil && tool.MCP.Hidden
		if slices.Contains(userRoutesPasswordTools, operation) {
			if !hidden || tool.MCP.HiddenReason != ir.IdentityPasswordToolReason {
				t.Errorf("%s: mcp = %+v, want hidden with %q", tool.Name, tool.MCP, ir.IdentityPasswordToolReason)
			}
		} else if hidden {
			t.Errorf("%s is hidden: %+v", tool.Name, tool.MCP)
		}
	}
	slices.Sort(tools)
	if !slices.Equal(tools, want) {
		t.Errorf("tools/schema.json tools = %v, want %v", tools, want)
	}
}

// TestWriteSDKGoldenUserRoutes pins every file of the Rust SDK crate for
// fixture-user-routes-api, tool documents included: the user model's
// operations are methods and tools as the project's greet is. Regenerate
// with:
// go test ./internal/generator/rustsdkgen -run TestWriteSDKGoldenUserRoutes -update
func TestWriteSDKGoldenUserRoutes(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", userRoutesService)
	writeUserRoutesSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", userRoutesService))
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", userRoutesService))
}

// TestUserRoutesSDKCrateBuildsAndRuns generates the Rust types crate and
// the Rust SDK crate of fixture-user-routes-api into a temp tree laid out
// as a build writes it, checks the SDK crate with cargo clippy and runs
// cargo test on it with userRoutesSDKTest. The types crate resolves
// superscalar from the checkout scripts/superscalar-dep.sh stands up.
// CARGO_TARGET_DIR is honored when set.
func TestUserRoutesSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadUserRoutesAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", userRoutesService)
	sdkDir := filepath.Join(root, "sdk", "rust", userRoutesService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: userRoutesService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput := writeUserRoutesSDK(t, apiOutput, sdkDir, typesDir)
	if sdkOutput.TypesCrate != typesOutput.CrateName {
		t.Fatalf("SDK types crate %q, types crate %q", sdkOutput.TypesCrate, typesOutput.CrateName)
	}

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "user_routes.rs"), strings.ReplaceAll(userRoutesSDKTest, "SDK_CRATE", crate))

	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// userRoutesSDKTest is tests/user_routes.rs of the generated SDK crate,
// with SDK_CRATE replaced by the crate's module name: login's token signs
// the next calls in, a boolean and a map of booleans decode, a PUT and a
// DELETE with path values alone send no body, and a password too short for
// Auth.Password is refused before the request.
const userRoutesSDKTest = `use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureUserRoutesApiSdk};

const USER_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";
const ROLE_ID: &str = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e";

struct Request {
    method: String,
    path: String,
    authorization: Option<String>,
    body: Value,
}

/// Answers one request per canned value with the success envelope around
/// it, and sends each request back.
fn serve(responses: Vec<Value>) -> (String, mpsc::Receiver<Request>) {
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
            let mut authorization = None;
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
                    } else if name.eq_ignore_ascii_case("authorization") {
                        authorization = Some(value.trim().to_string());
                    }
                }
            }
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            let body = if body.is_empty() { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            sender.send(Request { method, path, authorization, body }).unwrap();
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

#[tokio::test]
async fn the_user_model_operations_cross_the_wire() {
    let session_user = json!({"id": USER_ID, "login": "ada@example.com", "name": "Ada"});
    let user = json!({"id": USER_ID, "login": "ada@example.com", "name": "Ada", "disabled": false, "roles": [{"id": ROLE_ID, "name": "admin"}]});
    let (base_url, requests) = serve(vec![
        json!({"user": session_user, "expiresAt": "2026-01-02T03:04:05Z", "token": "session-token"}),
        json!({"operations": {"AccountMeHandler": true, "AccountAdminCreateUserHandler": false}}),
        json!(true),
        user,
        json!(true),
        json!(true),
    ]);
    let sdk = FixtureUserRoutesApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();

    let session = sdk
        .account
        .login(
            types::LoginInput {
                login: "ada@example.com".to_string(),
                password: "correct horse".to_string(),
                session: Some(types::SessionTransport::Bearer),
            },
            None,
        )
        .await
        .unwrap();
    let request = requests.recv().unwrap();
    assert_eq!((request.method.as_str(), request.path.as_str()), ("POST", "/api/auth/login"));
    assert_eq!(request.authorization, None);
    assert_eq!(request.body, json!({"login": "ada@example.com", "password": "correct horse", "session": "bearer"}));
    assert_eq!(session.user.login, "ada@example.com");
    sdk.set_token(session.token.unwrap());

    let capabilities = sdk.account.capabilities(None).await.unwrap();
    let request = requests.recv().unwrap();
    assert_eq!((request.method.as_str(), request.path.as_str()), ("GET", "/api/auth/capabilities"));
    assert_eq!(request.authorization.as_deref(), Some("Bearer session-token"));
    assert_eq!(
        capabilities.operations,
        HashMap::from([("AccountMeHandler".to_string(), true), ("AccountAdminCreateUserHandler".to_string(), false)])
    );

    let user_id: types::IdentityUUID = USER_ID.parse().unwrap();
    let role_id: types::IdentityUUID = ROLE_ID.parse().unwrap();
    assert!(sdk
        .account_admin
        .set_user_password(user_id, types::SetPasswordInput { password: "battery staple".to_string() }, None)
        .await
        .unwrap());
    let request = requests.recv().unwrap();
    assert_eq!(request.method, "PUT");
    assert_eq!(request.path, format!("/api/auth/admin/users/{user_id}/password"));
    assert_eq!(request.body, json!({"password": "battery staple"}));

    let granted = sdk.account_admin.grant_role(user_id, role_id, None).await.unwrap();
    let request = requests.recv().unwrap();
    assert_eq!(request.method, "PUT");
    assert_eq!(request.path, format!("/api/auth/admin/users/{user_id}/roles/{role_id}"));
    assert_eq!(request.body, Value::Null);
    assert_eq!(granted.roles.len(), 1);
    assert!(!granted.disabled);

    assert!(sdk.account_admin.delete_role(role_id, None).await.unwrap());
    let request = requests.recv().unwrap();
    assert_eq!(request.method, "DELETE");
    assert_eq!(request.path, format!("/api/auth/admin/roles/{role_id}"));
    assert_eq!(request.body, Value::Null);

    assert!(sdk.account.logout(None).await.unwrap());
    let request = requests.recv().unwrap();
    assert_eq!((request.method.as_str(), request.path.as_str()), ("POST", "/api/auth/logout"));
    assert_eq!(request.body, Value::Null);
}

#[tokio::test]
async fn a_short_password_is_refused_before_the_request() {
    let sdk = FixtureUserRoutesApiSdk::new(ClientConfig::with_base_url("http://127.0.0.1:9", None, None)).unwrap();
    let err = sdk
        .account
        .register(
            types::RegisterInput { login: "ada@example.com".to_string(), name: None, password: "short".to_string(), session: None },
            None,
        )
        .await
        .unwrap_err()
        .to_string();
    assert!(err.contains("input.password: must be at least 8 characters"), "{err}");
}
`
