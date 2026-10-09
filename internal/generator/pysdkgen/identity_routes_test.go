package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesService is the API whose @userSessions and @userAdministration
// sets the loader fills from fixture-user-model-db (D50).
const userRoutesService = "fixture-user-routes-api"

// loadUserRoutesAPI loads fixture-user-routes-api: the user model's
// account and account-admin operations, which the identity runtime serves,
// and greeting.greet, which the project implements.
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

func writeUserRoutesSDK(t *testing.T, apiOutput *apigen.APIOutput, dir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, dir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	return sdkOutput
}

// TestTheSDKHasAMethodForEachUserRoute: the identity runtime serves the
// user model's operations, not the project, but a client calls them as any
// other, so the Python SDK has a method for each beside greeting.greet.
func TestTheSDKHasAMethodForEachUserRoute(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var methods []string
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			methods = append(methods, namespace.AccessorName+"."+endpoint.MethodName)
		}
	}
	want := []string{
		"account.capabilities", "account.change_password", "account.login",
		"account.logout", "account.me", "account.register",
		"account_admin.create_role", "account_admin.create_user", "account_admin.delete_role",
		"account_admin.disable_user", "account_admin.enable_user", "account_admin.get_user",
		"account_admin.grant_role", "account_admin.list_roles", "account_admin.list_users",
		"account_admin.revoke_role", "account_admin.set_user_password", "account_admin.update_role",
		"greeting.greet",
	}
	if !slices.Equal(methods, want) {
		t.Errorf("SDK methods = %v, want %v", methods, want)
	}
}

// TestWriteSDKGoldenUserRoutes pins every file of the Python SDK for
// fixture-user-routes-api: the user model's operations, which take an input
// with an enum and passwords, answer a boolean, a map of booleans or a
// session with an optional token, and route PUT and DELETE requests without
// a body. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenUserRoutes -update
func TestWriteSDKGoldenUserRoutes(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	outDir := t.TempDir()
	writeUserRoutesSDK(t, apiOutput, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", userRoutesService))
}

// TestUserRoutesSDKImportsAndRuns writes the Python types package and the
// Python SDK of fixture-user-routes-api, byte-compiles both, and runs
// userRoutesProbe against a local HTTP server: each of the user model's
// operations sends its method, path and body, the bearer token where the
// route needs it, and comes back as its model, list or boolean; a short
// password or an unknown session transport fails validation before any
// request. The probe needs pydantic and skips without it.
func TestUserRoutesSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadUserRoutesAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: userRoutesService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput := writeUserRoutesSDK(t, apiOutput, sdkDir)
	if sdkOutput.TypesPackage != typesOutput.PythonModuleName {
		t.Fatalf("SDK types package %q, types module %q", sdkOutput.TypesPackage, typesOutput.PythonModuleName)
	}

	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(userRoutesProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// userRoutesProbe is formatted with the types and SDK directories, the SDK
// package and class, and the types package. The server answers each route
// with the data in answers, keyed by method and path.
const userRoutesProbe = `
import datetime
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
types_package = __import__(%[5]q)
SDK = getattr(sdk_package, %[4]q)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError

user_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
role_id = "5d1f7c2a-3b4e-4f6a-8c9d-0e1f2a3b4c5d"
session_user = {"id": user_id, "login": "ada@example.com", "name": "Ada"}
role = {"id": role_id, "name": "editor", "permissions": ["identity.users.read"]}
user = {**session_user, "disabled": False, "roles": [{"id": role_id, "name": "editor"}]}
users = "/api/auth/admin/users/" + user_id
roles = "/api/auth/admin/roles"
answers = {
    ("POST", "/api/auth/login"): {"user": session_user, "expiresAt": "2026-10-08T12:00:00Z", "token": "session-token"},
    ("POST", "/api/auth/register"): {"user": session_user, "expiresAt": "2026-10-08T12:00:00Z"},
    ("GET", "/api/auth/me"): {"user": session_user, "roles": [{"id": role_id, "name": "editor"}], "permissions": ["identity.users.read"]},
    ("GET", "/api/auth/capabilities"): {"operations": {"greet": True, "listUsers": False}},
    ("POST", "/api/auth/logout"): True,
    ("POST", "/api/auth/password"): True,
    ("POST", "/api/auth/admin/users"): user,
    ("GET", "/api/auth/admin/users"): [user],
    ("GET", users): user,
    ("POST", users + "/disable"): {**user, "disabled": True},
    ("POST", users + "/enable"): user,
    ("PUT", users + "/password"): True,
    ("GET", roles): [role],
    ("POST", roles): role,
    ("PUT", roles + "/" + role_id): role,
    ("DELETE", roles + "/" + role_id): True,
    ("PUT", users + "/roles/" + role_id): user,
    ("DELETE", users + "/roles/" + role_id): {**user, "roles": []},
    ("GET", "/api/greeting"): {"message": "Hello, Ada"},
}
calls = []


class Handler(BaseHTTPRequestHandler):
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        calls.append((self.command, self.path, json.loads(raw) if raw else None, self.headers.get("Authorization")))
        body = json.dumps({"data": answers[(self.command, self.path)], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PUT = do_DELETE = _answer

    def log_message(self, *args):
        pass


def refused(call, fields):
    try:
        call()
    except ValidationError as err:
        assert sorted(err.errors) == fields, err.errors
    else:
        raise AssertionError(f"expected a ValidationError for {fields}")


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}"))

# A short password and an unknown session transport fail before a request.
refused(lambda: sdk.account.login({"login": "ada@example.com", "password": "short"}), ["password"])
refused(lambda: sdk.account.login({"login": "ada@example.com", "password": "correct horse", "session": "pigeon"}), ["session"])
refused(lambda: sdk.account_admin.set_user_password(user_id, {"password": "short"}), ["password"])
assert calls == [], calls

# login is a public route: it sends no token, and answers the session's.
session = sdk.account.login({"login": "ada@example.com", "password": "correct horse", "session": "bearer"})
assert calls[-1] == ("POST", "/api/auth/login", {"login": "ada@example.com", "password": "correct horse", "session": "bearer"}, None), calls[-1]
assert type(session) is types_package.LoginResult, session
assert session.token == "session-token" and session.user.id == user_id, session
assert session.expires_at == datetime.datetime(2026, 10, 8, 12, tzinfo=datetime.timezone.utc), session.expires_at
sdk.set_token(session.token)
bearer = "Bearer session-token"

# A cookie session answers no token; the enum is sent as its value.
registered = sdk.account.register(types_package.RegisterInput(login="ada@example.com", password="correct horse", session=types_package.SessionTransport.Cookie))
assert calls[-1][:3] == ("POST", "/api/auth/register", {"login": "ada@example.com", "password": "correct horse", "session": "cookie"}), calls[-1]
assert registered.token is None, registered

current = sdk.account.me()
assert calls[-1] == ("GET", "/api/auth/me", None, bearer), calls[-1]
assert type(current) is types_package.CurrentUser and current.roles[0].name == "editor", current

capabilities = sdk.account.capabilities()
assert type(capabilities) is types_package.Capabilities, capabilities
assert capabilities.operations == {"greet": True, "listUsers": False}, capabilities.operations

assert sdk.account.change_password({"current": "correct horse", "password": "battery staple"}) is True
assert calls[-1] == ("POST", "/api/auth/password", {"current": "correct horse", "password": "battery staple"}, bearer), calls[-1]

admin = sdk.account_admin
created = admin.create_user({"login": "ada@example.com", "password": "correct horse"})
assert calls[-1][:3] == ("POST", "/api/auth/admin/users", {"login": "ada@example.com", "password": "correct horse"}), calls[-1]
assert type(created) is types_package.IdentityUser and created.roles[0].id == role_id, created
listed = admin.list_users()
assert [type(u) for u in listed] == [types_package.IdentityUser], listed
assert admin.get_user(user_id).login == "ada@example.com"
assert admin.disable_user(user_id).disabled is True
assert calls[-1][:3] == ("POST", users + "/disable", None), calls[-1]
assert admin.enable_user(user_id).disabled is False
assert admin.set_user_password(user_id, {"password": "battery staple"}) is True
assert calls[-1][:3] == ("PUT", users + "/password", {"password": "battery staple"}), calls[-1]

assert [r.name for r in admin.list_roles()] == ["editor"]
assert admin.create_role({"name": "editor", "permissions": ["identity.users.read"]}).id == role_id
assert calls[-1][:3] == ("POST", roles, {"name": "editor", "permissions": ["identity.users.read"]}), calls[-1]
assert type(admin.update_role(role_id, {"name": "editor", "permissions": []})) is types_package.IdentityRole
assert calls[-1][:3] == ("PUT", roles + "/" + role_id, {"name": "editor", "permissions": []}), calls[-1]
assert admin.delete_role(role_id) is True
assert calls[-1] == ("DELETE", roles + "/" + role_id, None, bearer), calls[-1]

# A grant and its revocation carry their ids in the path and no body.
assert admin.grant_role(user_id, role_id).roles[0].id == role_id
assert calls[-1] == ("PUT", users + "/roles/" + role_id, None, bearer), calls[-1]
assert admin.revoke_role(user_id, role_id).roles == []
assert calls[-1] == ("DELETE", users + "/roles/" + role_id, None, bearer), calls[-1]

assert sdk.account.logout() is True
assert calls[-1] == ("POST", "/api/auth/logout", None, bearer), calls[-1]
assert sdk.greeting.greet().message == "Hello, Ada"
assert len({(c[0], c[1]) for c in calls}) == len(answers), sorted(set(answers) - {(c[0], c[1]) for c in calls})
server.shutdown()
`
