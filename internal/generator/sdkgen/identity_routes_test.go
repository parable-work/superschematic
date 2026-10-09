package sdkgen

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesAPI is the API whose @userSessions and @userAdministration sets
// the loader fills from fixture-user-model-db (D50). The servers leave
// those operations to the identity runtime; the SDK calls them as any
// other.
const userRoutesAPI = "fixture-user-routes-api"

// loadUserRoutesAPI loads fixture-user-routes-api, with its authDb, and
// generates its API output.
func loadUserRoutesAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  userRoutesAPI,
		ModulePath:  "example.com/schemas/api/" + userRoutesAPI,
		TypesModule: "example.com/schemas/types/go/" + userRoutesAPI,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: userRoutesAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writeUserRoutesSDK writes the TypeScript SDK of fixture-user-routes-api
// into dir.
func writeUserRoutesSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// userRouteOperation is one operation of fixture-user-routes-api as the SDK
// and its tools carry it.
type userRouteOperation struct {
	namespace, method, path, permission string
	// carriesPassword marks the operations whose @mcp record is hidden
	// because an input field is a password.
	carriesPassword bool
}

// userRouteOperations is each operation of fixture-user-routes-api, keyed
// by its SDK method name.
var userRouteOperations = map[string]userRouteOperation{
	"login":           {"account", "POST", "/api/auth/login", "", true},
	"logout":          {"account", "POST", "/api/auth/logout", "", false},
	"me":              {"account", "GET", "/api/auth/me", "", false},
	"capabilities":    {"account", "GET", "/api/auth/capabilities", "", false},
	"changePassword":  {"account", "POST", "/api/auth/password", "", true},
	"register":        {"account", "POST", "/api/auth/register", "", true},
	"createUser":      {"account-admin", "POST", "/api/auth/admin/users", "identity.users.write", true},
	"listUsers":       {"account-admin", "GET", "/api/auth/admin/users", "identity.users.read", false},
	"getUser":         {"account-admin", "GET", "/api/auth/admin/users/{id}", "identity.users.read", false},
	"disableUser":     {"account-admin", "POST", "/api/auth/admin/users/{id}/disable", "identity.users.write", false},
	"enableUser":      {"account-admin", "POST", "/api/auth/admin/users/{id}/enable", "identity.users.write", false},
	"setUserPassword": {"account-admin", "PUT", "/api/auth/admin/users/{id}/password", "identity.users.write", true},
	"listRoles":       {"account-admin", "GET", "/api/auth/admin/roles", "identity.roles.read", false},
	"createRole":      {"account-admin", "POST", "/api/auth/admin/roles", "identity.roles.write", false},
	"updateRole":      {"account-admin", "PUT", "/api/auth/admin/roles/{id}", "identity.roles.write", false},
	"deleteRole":      {"account-admin", "DELETE", "/api/auth/admin/roles/{id}", "identity.roles.write", false},
	"grantRole":       {"account-admin", "PUT", "/api/auth/admin/users/{id}/roles/{roleId}", "identity.roles.write", false},
	"revokeRole":      {"account-admin", "DELETE", "/api/auth/admin/users/{id}/roles/{roleId}", "identity.roles.write", false},
	"greet":           {"greeting", "GET", "/api/greeting", "", false},
}

// TestWriteSDKGoldenUserRoutes pins every file of the TypeScript SDK for
// fixture-user-routes-api, its tool documents included. Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenUserRoutes -update
func TestWriteSDKGoldenUserRoutes(t *testing.T) {
	_, apiOutput, parseable := loadUserRoutesAPI(t)
	outDir := t.TempDir()
	writeUserRoutesSDK(t, apiOutput, parseable, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", userRoutesAPI))
}

// TestUserRoutesSDKMethods: the SDK has a method for each operation of the
// user model, in its set's namespace, at the route the identity runtime
// serves, beside the project's greet.
func TestUserRoutesSDKMethods(t *testing.T) {
	_, apiOutput, parseable := loadUserRoutesAPI(t)
	outDir := t.TempDir()
	sdkOutput := writeUserRoutesSDK(t, apiOutput, parseable, outDir)

	found := map[string]bool{}
	for _, namespace := range sdkOutput.Namespaces {
		source, err := os.ReadFile(filepath.Join(outDir, "namespaces", namespace.Name+".ts"))
		if err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range namespace.Endpoints {
			want, ok := userRouteOperations[endpoint.Name]
			if !ok {
				t.Errorf("unexpected SDK method %s.%s", namespace.Name, endpoint.Name)
				continue
			}
			found[endpoint.Name] = true
			if namespace.Name != want.namespace || endpoint.Method != want.method || endpoint.Path != want.path {
				t.Errorf("%s: %s %s in %s, want %s %s in %s", endpoint.Name, endpoint.Method, endpoint.Path, namespace.Name, want.method, want.path, want.namespace)
			}
			if !strings.Contains(string(source), "public async "+endpoint.Name+"(") {
				t.Errorf("namespaces/%s.ts has no method %s", namespace.Name, endpoint.Name)
			}
		}
	}
	for name := range userRouteOperations {
		if !found[name] {
			t.Errorf("the SDK has no method %s", name)
		}
	}
}

// TestUserRoutesTools: tools/schema.json lists each operation as a tool.
// The five whose input carries a password have an @mcp record that hides
// them, with the reason the loader gives; the others have none, as an
// operation without @mcp has none. Each tool's schemas are the ones
// toolsutil pins.
func TestUserRoutesTools(t *testing.T) {
	_, apiOutput, parseable := loadUserRoutesAPI(t)
	outDir := t.TempDir()
	writeUserRoutesSDK(t, apiOutput, parseable, outDir)

	var document struct {
		Tools []struct {
			Name                string         `json:"name"`
			MethodName          string         `json:"methodName"`
			HTTPMethod          string         `json:"httpMethod"`
			HTTPPath            string         `json:"httpPath"`
			RequiresAuth        bool           `json:"requiresAuth"`
			RequiredPermissions []string       `json:"requiredPermissions"`
			BindingStatus       string         `json:"bindingStatus"`
			MCP                 map[string]any `json:"mcp"`
		} `json:"tools"`
	}
	readJSON(t, filepath.Join(outDir, "tools", "schema.json"), &document)
	if len(document.Tools) != len(userRouteOperations) {
		t.Errorf("tools/schema.json has %d tools, want %d", len(document.Tools), len(userRouteOperations))
	}
	for _, tool := range document.Tools {
		want, ok := userRouteOperations[tool.MethodName]
		if !ok {
			t.Errorf("unexpected tool %s", tool.Name)
			continue
		}
		if tool.Name != want.namespace+"."+tool.MethodName || tool.HTTPMethod != want.method || tool.HTTPPath != want.path || tool.BindingStatus != "ready" {
			t.Errorf("%s: %s %s, binding %s", tool.Name, tool.HTTPMethod, tool.HTTPPath, tool.BindingStatus)
		}
		public := tool.MethodName == "login" || tool.MethodName == "register"
		if tool.RequiresAuth == public {
			t.Errorf("%s: requiresAuth = %v", tool.Name, tool.RequiresAuth)
		}
		var wantPermissions []string
		if want.permission != "" {
			wantPermissions = []string{want.permission}
		}
		if !slices.Equal(tool.RequiredPermissions, wantPermissions) {
			t.Errorf("%s: requiredPermissions = %v, want %v", tool.Name, tool.RequiredPermissions, wantPermissions)
		}
		var wantMCP map[string]any
		if want.carriesPassword {
			wantMCP = map[string]any{"hidden": true, "hiddenReason": ir.IdentityPasswordToolReason}
		}
		if !maps.Equal(tool.MCP, wantMCP) {
			t.Errorf("%s: mcp = %v, want %v", tool.Name, tool.MCP, wantMCP)
		}
	}
	requireToolSchemasMatchToolsutil(t, outDir, userRoutesAPI)
}

// TestUserRoutesSDKCompiles type-checks the TypeScript SDK of
// fixture-user-routes-api, its tools included, against its generated types
// package: boolean results, the Capabilities map, the SessionTransport
// enum, password and secret fields, and PUT and DELETE routes without a
// body.
func TestUserRoutesSDKCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	schema, apiOutput, parseable := loadUserRoutesAPI(t)
	typesDir := writeTypesPackage(t, bunPath, schema, userRoutesAPI, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", userRoutesAPI)
	writeUserRoutesSDK(t, apiOutput, parseable, sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
	requireToolsBuilt(t, sdkDir)
}

// TestUserRoutesSDKCookieSessions: the SDK of an API that serves the user
// model takes the fetch credentials mode for a cookie session (D50), passes
// it to fetch, and sends no stored token in its place; an SDK of an API
// without the user model is unchanged.
func TestUserRoutesSDKCookieSessions(t *testing.T) {
	_, apiOutput, parseable := loadUserRoutesAPI(t)
	outDir := t.TempDir()
	sdkOutput := writeUserRoutesSDK(t, apiOutput, parseable, outDir)
	if !sdkOutput.CookieSessions || sdkOutput.LoginNamespace != "account" {
		t.Errorf("cookie sessions %v, login namespace %q", sdkOutput.CookieSessions, sdkOutput.LoginNamespace)
	}
	for file, wants := range map[string][]string{
		"types.ts":  {"credentials?: RequestCredentials;"},
		"client.ts": {"{ credentials: this.config.credentials }", "if (this.config.credentials !== undefined && !this.config.auth) {"},
		"README.md": {"### Sessions", "credentials: 'include',", "session: SessionTransport.Cookie"},
	} {
		source, err := os.ReadFile(filepath.Join(outDir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(source), want) {
				t.Errorf("%s has no %s", file, want)
			}
		}
	}

	_, plainAPI, plainParseable := loadFixtureAPI(t)
	plain, err := Generate(plainAPI, plainParseable, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CookieSessions || plain.LoginNamespace != "" {
		t.Errorf("fixture-api: cookie sessions %v, login namespace %q", plain.CookieSessions, plain.LoginNamespace)
	}
}
