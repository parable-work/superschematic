package gosdkgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesService is the API whose @userSessions and @userAdministration
// sets the loader fills from fixture-user-model-db (D50).
const userRoutesService = "fixture-user-routes-api"

// loadUserRoutesAPI loads fixture-user-routes-api: the identity runtime's
// eighteen operations beside the project's own greet.
func loadUserRoutesAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesService))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  userRoutesService,
		ModulePath:  "example.com/schemas/api/" + userRoutesService,
		TypesModule: "example.com/schemas/types/go/" + userRoutesService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// writeUserRoutesSDK writes the Go SDK of fixture-user-routes-api into a
// temp tree laid out as a build writes it, and returns its directory.
func writeUserRoutesSDK(t *testing.T, apiOutput *apigen.APIOutput) (*SDKOutput, string) {
	t.Helper()
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", userRoutesService)
	typesDir := filepath.Join(root, "types", "go", userRoutesService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+userRoutesService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput, sdkDir
}

// TestWriteSDKGoldenUserRoutes pins every file of the Go SDK for
// fixture-user-routes-api, whose identity runtime operations are methods
// and tools as any other. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenUserRoutes -update
func TestWriteSDKGoldenUserRoutes(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	_, sdkDir := writeUserRoutesSDK(t, apiOutput)
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", userRoutesService))
}

// TestUserRoutesAreSDKMethods: each of the identity runtime's operations
// is a method of its namespace, with the route the server answers, beside
// the project's greet.
func TestUserRoutesAreSDKMethods(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+userRoutesService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	type route struct{ method, path string }
	want := map[string]map[string]route{
		"AccountNamespace": {
			"Login":          {"POST", "/api/auth/login"},
			"Logout":         {"POST", "/api/auth/logout"},
			"Me":             {"GET", "/api/auth/me"},
			"Capabilities":   {"GET", "/api/auth/capabilities"},
			"ChangePassword": {"POST", "/api/auth/password"},
			"Register":       {"POST", "/api/auth/register"},
		},
		"AccountAdminNamespace": {
			"CreateUser":      {"POST", "/api/auth/admin/users"},
			"ListUsers":       {"GET", "/api/auth/admin/users"},
			"GetUser":         {"GET", "/api/auth/admin/users/%v"},
			"DisableUser":     {"POST", "/api/auth/admin/users/%v/disable"},
			"EnableUser":      {"POST", "/api/auth/admin/users/%v/enable"},
			"SetUserPassword": {"PUT", "/api/auth/admin/users/%v/password"},
			"ListRoles":       {"GET", "/api/auth/admin/roles"},
			"CreateRole":      {"POST", "/api/auth/admin/roles"},
			"UpdateRole":      {"PUT", "/api/auth/admin/roles/%v"},
			"DeleteRole":      {"DELETE", "/api/auth/admin/roles/%v"},
			"GrantRole":       {"PUT", "/api/auth/admin/users/%v/roles/%v"},
			"RevokeRole":      {"DELETE", "/api/auth/admin/users/%v/roles/%v"},
		},
		"GreetingNamespace": {
			"Greet": {"GET", "/api/greeting"},
		},
	}
	if !sdkOutput.HasAuth {
		t.Error("HasAuth = false with operations that need a caller")
	}
	got := map[string]map[string]route{}
	for _, namespace := range sdkOutput.Namespaces {
		methods := map[string]route{}
		for _, endpoint := range namespace.Endpoints {
			methods[endpoint.MethodName] = route{endpoint.HTTPMethod, endpoint.PathFormat}
		}
		got[namespace.StructName] = methods
	}
	for namespace, methods := range want {
		if len(got[namespace]) != len(methods) {
			t.Errorf("%s has %d methods, want %d: %v", namespace, len(got[namespace]), len(methods), got[namespace])
		}
		for name, wantRoute := range methods {
			gotRoute, ok := got[namespace][name]
			if !ok {
				t.Errorf("%s has no method %s", namespace, name)
				continue
			}
			if gotRoute != wantRoute {
				t.Errorf("%s.%s = %s %s, want %s %s", namespace, name, gotRoute.method, gotRoute.path, wantRoute.method, wantRoute.path)
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("namespaces = %v", got)
	}
}

// TestUserRoutesAreTools: in the Go SDK's tool schema every operation is a
// tool, and those that carry a password are hidden from an agent with the
// reason the identity runtime gives.
func TestUserRoutesAreTools(t *testing.T) {
	_, apiOutput := loadUserRoutesAPI(t)
	_, sdkDir := writeUserRoutesSDK(t, apiOutput)
	raw, err := os.ReadFile(filepath.Join(sdkDir, "tools", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Tools []struct {
			Name          string `json:"name"`
			BindingStatus string `json:"bindingStatus"`
			MCP           *struct {
				Hidden       bool   `json:"hidden"`
				HiddenReason string `json:"hiddenReason"`
			} `json:"mcp"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("tools/schema.json: %v", err)
	}
	const reason = "It carries a password, which an agent does not hold."
	hidden := map[string]bool{
		"account.login": true, "account.register": true, "account.changePassword": true,
		"account-admin.createUser": true, "account-admin.setUserPassword": true,
	}
	want := []string{
		"account.login", "account.logout", "account.me", "account.capabilities", "account.changePassword", "account.register",
		"account-admin.createUser", "account-admin.listUsers", "account-admin.getUser", "account-admin.disableUser",
		"account-admin.enableUser", "account-admin.setUserPassword", "account-admin.listRoles", "account-admin.createRole",
		"account-admin.updateRole", "account-admin.deleteRole", "account-admin.grantRole", "account-admin.revokeRole",
		"greeting.greet",
	}
	seen := map[string]bool{}
	for _, tool := range document.Tools {
		seen[tool.Name] = true
		if tool.BindingStatus != "ready" {
			t.Errorf("%s: bindingStatus = %q, want ready", tool.Name, tool.BindingStatus)
		}
		switch {
		case hidden[tool.Name]:
			if tool.MCP == nil || !tool.MCP.Hidden || tool.MCP.HiddenReason != reason {
				t.Errorf("%s: mcp = %+v, want hidden because %q", tool.Name, tool.MCP, reason)
			}
		case tool.MCP != nil && tool.MCP.Hidden:
			t.Errorf("%s is hidden (%q)", tool.Name, tool.MCP.HiddenReason)
		}
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("tools/schema.json has no tool %s", name)
		}
	}
	if len(document.Tools) != len(want) {
		t.Errorf("tools/schema.json has %d tools, want %d", len(document.Tools), len(want))
	}
}

// TestUserRoutesSDKBuildsAndRuns runs userRoutesSDKTest in the generated
// SDK: login and register send their credentials and decode the session,
// a password too short is refused before any request, capabilities decode
// a map of booleans, operations that answer true decode a boolean, and a
// PUT or DELETE whose values all travel in the path sends no body.
func TestUserRoutesSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadUserRoutesAPI(t)
	runInSDK(t, schema, apiOutput, userRoutesService, "user_routes_test.go", userRoutesSDKTest)
}

// userRoutesSDKTest runs in the generated SDK module against an httptest
// server that answers every request with the success envelope around a
// canned body.
const userRoutesSDKTest = `package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	sdk "example.com/schemas/sdk/go/fixture-user-routes-api"
	"example.com/schemas/sdk/go/fixture-user-routes-api/namespaces"
	types "example.com/schemas/types/go/fixture-user-routes-api"
)

const (
	userID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
	roleID = "5d3c2b1a-0f9e-4d8c-8b7a-6e5f4d3c2b1a"
)

// sameUUID reports whether id is the UUID written as text. The scalar's
// String form is its short encoding, so the parsed values are compared.
func sameUUID(t *testing.T, id types.IdentityUUID, text string) bool {
	t.Helper()
	want, err := types.ParseIdentityUUID(text)
	if err != nil {
		t.Fatal(err)
	}
	return id == want
}

type call struct {
	method, path, authorization string
	body                        any
}

func serve(t *testing.T, data string) (*sdk.FixtureUserRoutesApiSDK, *[]call) {
	t.Helper()
	calls := &[]call{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		*calls = append(*calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ` + "`" + `{"data":` + "`" + `+data+` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, calls
}

func decode(t *testing.T, value string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// fields returns the sorted field paths of a client-side validation error.
func fields(t *testing.T, err error) []string {
	t.Helper()
	var validation *namespaces.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	var names []string
	for name := range validation.Errors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const session = ` + "`" + `{"user":{"id":"` + "`" + ` + userID + ` + "`" + `","login":"ada@example.com","name":"Ada"},"expiresAt":"2026-01-02T03:04:05Z","token":"tok-1"}` + "`" + `

func TestLoginSendsCredentialsAndDecodesTheSession(t *testing.T) {
	client, calls := serve(t, session)
	bearer := types.SessionTransport_Bearer
	got, err := client.AccountNamespace.Login(context.Background(), types.LoginInput{
		Login:    "ada@example.com",
		Password: "correct horse",
		Session:  types.InputField[*types.SessionTransport]{Set: true, Value: &bearer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "POST" || (*calls)[0].path != "/api/auth/login" {
		t.Fatalf("calls = %+v", *calls)
	}
	if want := decode(t, ` + "`" + `{"login":"ada@example.com","password":"correct horse","session":"bearer"}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
	if got.Token != "tok-1" || got.User.Login != "ada@example.com" || !sameUUID(t, got.User.Id, userID) ||
		!time.Time(got.ExpiresAt).Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("session = %+v", got)
	}
}

func TestRegisterLeavesOutUnsetFields(t *testing.T) {
	client, calls := serve(t, session)
	if _, err := client.AccountNamespace.Register(context.Background(), types.RegisterInput{
		Login:    "ada@example.com",
		Password: "correct horse",
	}); err != nil {
		t.Fatal(err)
	}
	if want := decode(t, ` + "`" + `{"login":"ada@example.com","password":"correct horse"}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
}

func TestPasswordsAreCheckedBeforeAnyRequest(t *testing.T) {
	client, calls := serve(t, "true")
	_, err := client.AccountNamespace.Login(context.Background(), types.LoginInput{Login: "ada@example.com", Password: "short"})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"password"}) {
		t.Errorf("login: fields = %v, want [password]", got)
	}
	_, err = client.AccountAdminNamespace.SetUserPassword(context.Background(), userID, types.SetPasswordInput{Password: "short"})
	if got := fields(t, err); !reflect.DeepEqual(got, []string{"password"}) {
		t.Errorf("setUserPassword: fields = %v, want [password]", got)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused request was sent: %+v", *calls)
	}
}

func TestCapabilitiesDecodeAMapOfBooleans(t *testing.T) {
	client, calls := serve(t, ` + "`" + `{"operations":{"GreetingGreetHandler":true,"AccountAdminListUsersHandler":false}}` + "`" + `)
	client.SetToken("tok-1")
	got, err := client.AccountNamespace.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"GreetingGreetHandler": true, "AccountAdminListUsersHandler": false}; !reflect.DeepEqual(got.Operations, want) {
		t.Errorf("operations = %v, want %v", got.Operations, want)
	}
	if (*calls)[0].method != "GET" || (*calls)[0].path != "/api/auth/capabilities" || (*calls)[0].authorization != "Bearer tok-1" {
		t.Errorf("call = %+v", (*calls)[0])
	}
}

func TestOperationsThatAnswerTrueDecodeABoolean(t *testing.T) {
	client, calls := serve(t, "true")
	ctx := context.Background()
	for name, run := range map[string]func() (bool, error){
		"logout":     func() (bool, error) { return client.AccountNamespace.Logout(ctx) },
		"deleteRole": func() (bool, error) { return client.AccountAdminNamespace.DeleteRole(ctx, roleID) },
		"changePassword": func() (bool, error) {
			return client.AccountNamespace.ChangePassword(ctx, types.ChangePasswordInput{Current: "old password", Password: "new password"})
		},
	} {
		if ok, err := run(); err != nil || !ok {
			t.Errorf("%s = %v, %v; want true", name, ok, err)
		}
	}
	if len(*calls) != 3 {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestPathOnlyWritesSendNoBody(t *testing.T) {
	user := ` + "`" + `{"id":"` + "`" + ` + userID + ` + "`" + `","login":"ada@example.com","name":"Ada","disabled":false,"roles":[{"id":"` + "`" + ` + roleID + ` + "`" + `","name":"admin"}]}` + "`" + `
	client, calls := serve(t, user)
	ctx := context.Background()
	granted, err := client.AccountAdminNamespace.GrantRole(ctx, userID, roleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted.Roles) != 1 || granted.Roles[0].Name != "admin" || !sameUUID(t, granted.Roles[0].Id, roleID) {
		t.Errorf("granted = %+v", granted)
	}
	if _, err := client.AccountAdminNamespace.RevokeRole(ctx, userID, roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AccountAdminNamespace.DisableUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"PUT", "/api/auth/admin/users/" + userID + "/roles/" + roleID, "", nil},
		{"DELETE", "/api/auth/admin/users/" + userID + "/roles/" + roleID, "", nil},
		{"POST", "/api/auth/admin/users/" + userID + "/disable", "", nil},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls = %+v, want %+v", *calls, want)
	}
}

func TestUpdateRoleSendsItsBody(t *testing.T) {
	client, calls := serve(t, ` + "`" + `{"id":"` + "`" + ` + roleID + ` + "`" + `","name":"editor","permissions":["identity.users.read"]}` + "`" + `)
	got, err := client.AccountAdminNamespace.UpdateRole(context.Background(), roleID, types.RoleInput{Name: "editor", Permissions: []string{"identity.users.read"}})
	if err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].method != "PUT" || (*calls)[0].path != "/api/auth/admin/roles/"+roleID {
		t.Errorf("call = %+v", (*calls)[0])
	}
	if want := decode(t, ` + "`" + `{"name":"editor","permissions":["identity.users.read"]}` + "`" + `); !reflect.DeepEqual((*calls)[0].body, want) {
		t.Errorf("body = %v, want %v", (*calls)[0].body, want)
	}
	if got.Name != "editor" || !reflect.DeepEqual(got.Permissions, []string{"identity.users.read"}) {
		t.Errorf("role = %+v", got)
	}
}
`
