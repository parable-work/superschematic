package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// identityServerDatabaseEnv names the Postgres the generated server's test
// also runs against, as the identity runtime's store tests do; it runs on
// SQLite alone without it.
const identityServerDatabaseEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"

// TestUserRoutesServerServesTheSDK builds fixture-user-model-db and
// fixture-user-routes-api as build-all does, then writes
// userRoutesServerTest into the generated API module and runs it, after go
// vet: the generated Go server, wired to the identity runtime (D50) over
// fixture-user-model-db's store, called through the generated Go SDK. It
// runs on SQLite, with the DDL the identity runtime's store tests read, and
// on the Postgres identityServerDatabaseEnv names when it is set, with the
// DDL the build wrote.
func TestUserRoutesServerServesTheSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{"fixture-user-model-db", "fixture-user-routes-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-user-routes-api")
	copyFile := func(from, to string) {
		t.Helper()
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyFile(filepath.Join(SQLDir(outputRoot, "fixture-user-model-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	copyFile(filepath.Join(identityFixtureDir, SQLiteSubdir, "create.sql"), filepath.Join(apiDir, "testdata", SQLiteSubdir, "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "identity_server_test.go"), []byte(userRoutesServerTest), 0o644); err != nil {
		t.Fatal(err)
	}

	// The test reaches the Go SDK and a SQLite driver beside what the API
	// module requires.
	sdkModule := "example.com/schemas/sdk/go/fixture-user-routes-api"
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-user-routes-api", "-require=modernc.org/sqlite@v1.60.1"},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, want := range []string{"--- PASS: TestServerOverTheUserModel/sqlite/sessions ", "--- PASS: TestServerOverTheUserModel/sqlite/administration ", "--- PASS: TestServerOverTheUserModel/sqlite/cookies "} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("the generated API module did not run %q", strings.TrimSpace(strings.TrimPrefix(want, "--- PASS: ")))
		}
	}
	if os.Getenv(identityServerDatabaseEnv) != "" && !strings.Contains(string(out), "--- PASS: TestServerOverTheUserModel/postgres/cookies ") {
		t.Fatal("the generated API module did not run on Postgres")
	}
}

// userRoutesServerTest runs in the generated module of
// fixture-user-routes-api. It serves the generated routes over the identity
// runtime's SQLStore, built from fixture-user-model-db's descriptor
// constant, and calls them through the generated Go SDK.
const userRoutesServerTest = `package fixtureuserroutesapi_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"

	api "example.com/schemas/api/fixture-user-routes-api"
	sdk "example.com/schemas/sdk/go/fixture-user-routes-api"
	dbtypes "example.com/schemas/types/go/fixture-user-model-db"
	types "example.com/schemas/types/go/fixture-user-routes-api"
)

// config hashes at a low cost, trusts one origin, and writes a cookie
// without Secure, since httptest serves plain HTTP.
const config = ` + "`" + `{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}}` + "`" + `

const (
	adminLogin    = "admin@example.com"
	adminPassword = "admin password"
)

// greeter is the project's implementation of greet, the one operation the
// identity runtime does not serve.
type greeter struct{}

func (greeter) Greet(ctx context.Context) (*types.Greeting, error) {
	return &types.Greeting{Message: "Hello, " + api.GetPrincipalName(ctx)}, nil
}

// server is the generated routes over a store on db, as an entrypoint
// mounts them, with an admin who holds the role admin (identity).
type server struct {
	url   string
	store *identity.SQLStore
}

func newServer(t *testing.T, dialect identity.Dialect, db *sql.DB) *server {
	t.Helper()
	store, err := identity.NewSQLStore(db, dialect, []byte(dbtypes.IdentityDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := identity.ParseConfig([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := api.NewIdentity(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Identity:        svc,
		Implementations: api.Implementations{Greeting: greeter{}},
	}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)

	ctx := context.Background()
	hash, err := identity.HashPassword(adminPassword, cfg.Argon2Params())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.CreateUser(ctx, identity.NewUser{Login: adminLogin, Name: "Admin", PasswordHash: hash, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	role, err := store.CreateRole(ctx, "admin", []string{"identity"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantRole(ctx, admin.ID, role.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	return &server{url: httpServer.URL, store: store}
}

func (s *server) client(t *testing.T) *sdk.FixtureUserRoutesApiSDK {
	t.Helper()
	client, err := sdk.New(sdk.SDKConfig{BaseURL: s.url})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// login signs in through the SDK with a bearer session and gives client
// the token.
func login(t *testing.T, client *sdk.FixtureUserRoutesApiSDK, login, password string) types.LoginResult {
	t.Helper()
	result, err := client.AccountNamespace.Login(context.Background(), types.LoginInput{Login: types.ContactEmail(login), Password: types.AuthPassword(password)})
	if err != nil {
		t.Fatalf("login %s: %v", login, err)
	}
	if result.Token == "" {
		t.Fatalf("a bearer login answered no token: %+v", result)
	}
	client.SetToken(result.Token)
	return result
}

func status(err error) int {
	var (
		apiErr          *sdk.APIError
		unauthenticated *sdk.AuthenticationError
		forbidden       *sdk.AuthorizationError
	)
	switch {
	case err == nil:
		return 200
	case errors.As(err, &unauthenticated):
		return unauthenticated.StatusCode
	case errors.As(err, &forbidden):
		return forbidden.StatusCode
	case errors.As(err, &apiErr):
		return apiErr.StatusCode
	}
	return -1
}

func code(err error) string {
	var (
		apiErr          *sdk.APIError
		unauthenticated *sdk.AuthenticationError
		forbidden       *sdk.AuthorizationError
	)
	switch {
	case errors.As(err, &unauthenticated):
		return unauthenticated.Code
	case errors.As(err, &forbidden):
		return forbidden.Code
	case errors.As(err, &apiErr):
		return apiErr.Code
	}
	return ""
}

func expect(t *testing.T, what string, err error, wantStatus int, wantCode string) {
	t.Helper()
	if got := status(err); got != wantStatus || (wantCode != "" && code(err) != wantCode) {
		t.Fatalf("%s: %v (status %d, code %q), want %d %s", what, err, got, code(err), wantStatus, wantCode)
	}
}

// databases open a database of their own holding fixture-user-model-db's
// tables: SQLite always, and the Postgres the environment names when it is
// set.
func databases() map[identity.Dialect]func(*testing.T) *sql.DB {
	open := map[identity.Dialect]func(*testing.T) *sql.DB{identity.SQLite: openSQLite}
	if url := os.Getenv("` + identityServerDatabaseEnv + `"); url != "" {
		open[identity.Postgres] = func(t *testing.T) *sql.DB { return openPostgres(t, url) }
	}
	return open
}

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(filepath.Join("testdata", "sqlite", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply sqlite/create.sql: %v", err)
	}
	return db
}

// openPostgres opens a pool whose search path is a schema of its own that
// holds the build's create.sql, and drops the schema when the test ends.
func openPostgres(t *testing.T, url string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	for _, ext := range []string{"pgcrypto", "citext"} {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext+" SCHEMA public"); err != nil && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("create %s: %v", ext, err)
		}
	}
	schema := fmt.Sprintf("identity_server_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(filepath.Join("testdata", "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	return db
}

func TestServerOverTheUserModel(t *testing.T) {
	open := databases()
	for _, dialect := range []identity.Dialect{identity.SQLite, identity.Postgres} {
		db, ok := open[dialect]
		if !ok {
			continue
		}
		t.Run(string(dialect), func(t *testing.T) {
			t.Run("sessions", func(t *testing.T) { testSessions(t, newServer(t, dialect, db(t))) })
			t.Run("administration", func(t *testing.T) { testAdministration(t, newServer(t, dialect, db(t))) })
			t.Run("cookies", func(t *testing.T) { testCookies(t, newServer(t, dialect, db(t))) })
		})
	}
}

// testSessions: a user registers, signs in with a bearer session, reads
// me and capabilities, calls the project's protected route, changes their
// password and signs out, after which the session authenticates no one.
func testSessions(t *testing.T, s *server) {
	ctx := context.Background()
	anonymous := s.client(t)
	_, err := anonymous.GreetingNamespace.Greet(ctx)
	expect(t, "greet without a session", err, 401, "")

	registered, err := anonymous.AccountNamespace.Register(ctx, types.RegisterInput{
		Login:    "Ada@Example.com",
		Name:     types.InputField[types.IdentityName]{Set: true, Value: "Ada"},
		Password: "ada's password",
	})
	if err != nil || registered.Token == "" || registered.User.Login != "ada@example.com" || registered.User.Name != "Ada" {
		t.Fatalf("register: %+v, %v", registered, err)
	}

	ada := s.client(t)
	signedIn := login(t, ada, "ada@example.com", "ada's password")
	if signedIn.User.Id != registered.User.Id || time.Time(signedIn.ExpiresAt).Before(time.Now().Add(13*24*time.Hour)) {
		t.Fatalf("login answered %+v, want the registered user for 14 days", signedIn)
	}
	_, err = s.client(t).AccountNamespace.Login(ctx, types.LoginInput{Login: "ada@example.com", Password: "not her password"})
	expect(t, "login with a wrong password", err, 401, "invalid_credentials")

	me, err := ada.AccountNamespace.Me(ctx)
	if err != nil || me.User.Id != registered.User.Id || me.User.Name != "Ada" || len(me.Roles) != 0 || len(me.Permissions) != 0 {
		t.Fatalf("me: %+v, %v", me, err)
	}
	capabilities, err := ada.AccountNamespace.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for operation, want := range map[string]bool{
		"GreetingGreetHandler":         true,
		"AccountMeHandler":             true,
		"AccountLoginHandler":          true,
		"AccountAdminListUsersHandler": false,
		"AccountAdminCreateRoleHandler": false,
	} {
		if got, ok := capabilities.Operations[operation]; !ok || got != want {
			t.Errorf("capabilities[%s] = %v (listed %v), want %v", operation, got, ok, want)
		}
	}
	if len(capabilities.Operations) != 19 {
		t.Errorf("capabilities lists %d operations, want 19", len(capabilities.Operations))
	}

	greeting, err := ada.GreetingNamespace.Greet(ctx)
	if err != nil || greeting.Message != "Hello, Ada" {
		t.Fatalf("greet: %+v, %v", greeting, err)
	}

	// A second session of hers ends when she changes her password; the
	// session she changes it with lives on.
	other := s.client(t)
	login(t, other, "ada@example.com", "ada's password")
	changed, err := ada.AccountNamespace.ChangePassword(ctx, types.ChangePasswordInput{Current: "ada's password", Password: "a new password"})
	if err != nil || !changed {
		t.Fatalf("changePassword: %v, %v", changed, err)
	}
	_, err = other.AccountNamespace.Me(ctx)
	expect(t, "me on a session the password change ended", err, 401, "")
	_, err = s.client(t).AccountNamespace.Login(ctx, types.LoginInput{Login: "ada@example.com", Password: "ada's password"})
	expect(t, "login with the old password", err, 401, "invalid_credentials")
	login(t, s.client(t), "ada@example.com", "a new password")

	out, err := ada.AccountNamespace.Logout(ctx)
	if err != nil || !out {
		t.Fatalf("logout: %v, %v", out, err)
	}
	_, err = ada.AccountNamespace.Me(ctx)
	expect(t, "me after logout", err, 401, "")
	_, err = ada.GreetingNamespace.Greet(ctx)
	expect(t, "greet after logout", err, 401, "")
}

// testAdministration: an admin holding identity manages roles and grants;
// a user is admitted to a route by the role they are granted; and no one
// grants a permission they do not hold.
func testAdministration(t *testing.T, s *server) {
	ctx := context.Background()
	admin := s.client(t)
	login(t, admin, adminLogin, adminPassword)

	created, err := admin.AccountAdminNamespace.CreateUser(ctx, types.CreateUserInput{Login: "grace@example.com", Name: types.InputField[types.IdentityName]{Set: true, Value: "Grace"}, Password: "grace's password"})
	if err != nil || created.Login != "grace@example.com" || created.Disabled {
		t.Fatalf("createUser: %+v, %v", created, err)
	}
	grace := s.client(t)
	login(t, grace, "grace@example.com", "grace's password")
	_, err = grace.AccountAdminNamespace.ListUsers(ctx)
	expect(t, "listUsers without identity.users.read", err, 403, "forbidden")

	reader, err := admin.AccountAdminNamespace.CreateRole(ctx, types.RoleInput{Name: "user reader", Permissions: []string{"identity.users.read"}})
	if err != nil || reader.Name != "user reader" {
		t.Fatalf("createRole: %+v, %v", reader, err)
	}
	granted, err := admin.AccountAdminNamespace.GrantRole(ctx, created.Id.String(), reader.Id.String())
	if err != nil || len(granted.Roles) != 1 || granted.Roles[0].Name != "user reader" {
		t.Fatalf("grantRole: %+v, %v", granted, err)
	}
	users, err := grace.AccountAdminNamespace.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("listUsers with identity.users.read: %+v, %v", users, err)
	}
	capabilities, err := grace.AccountNamespace.Capabilities(ctx)
	if err != nil || !capabilities.Operations["AccountAdminListUsersHandler"] || capabilities.Operations["AccountAdminCreateRoleHandler"] {
		t.Fatalf("capabilities with identity.users.read: %+v, %v", capabilities, err)
	}
	me, err := grace.AccountNamespace.Me(ctx)
	if err != nil || !slices.Equal(me.Permissions, []string{"identity.users.read"}) {
		t.Fatalf("me with the role: %+v, %v", me, err)
	}
	_, err = grace.AccountAdminNamespace.CreateRole(ctx, types.RoleInput{Name: "writer", Permissions: []string{"identity.users.read"}})
	expect(t, "createRole without identity.roles.write", err, 403, "forbidden")

	// The admin holds identity and nothing else, so cannot write a role
	// that grants orders.read.
	_, err = admin.AccountAdminNamespace.CreateRole(ctx, types.RoleInput{Name: "orders", Permissions: []string{"orders.read"}})
	expect(t, "createRole with a permission the admin does not hold", err, 403, "forbidden")
	_, err = admin.AccountAdminNamespace.CreateRole(ctx, types.RoleInput{Name: "bad", Permissions: []string{"not a permission"}})
	expect(t, "createRole with a malformed permission", err, 422, "invalid_permission")

	revoked, err := admin.AccountAdminNamespace.RevokeRole(ctx, created.Id.String(), reader.Id.String())
	if err != nil || len(revoked.Roles) != 0 {
		t.Fatalf("revokeRole: %+v, %v", revoked, err)
	}
	_, err = grace.AccountAdminNamespace.ListUsers(ctx)
	expect(t, "listUsers once the role is revoked", err, 403, "forbidden")

	deleted, err := admin.AccountAdminNamespace.DeleteRole(ctx, reader.Id.String())
	if err != nil || !deleted {
		t.Fatalf("deleteRole: %v, %v", deleted, err)
	}
	set, err := admin.AccountAdminNamespace.SetUserPassword(ctx, created.Id.String(), types.SetPasswordInput{Password: "reset password"})
	if err != nil || !set {
		t.Fatalf("setUserPassword: %v, %v", set, err)
	}
	_, err = grace.AccountNamespace.Me(ctx)
	expect(t, "me on a session the reset ended", err, 401, "")
	disabled, err := admin.AccountAdminNamespace.DisableUser(ctx, created.Id.String())
	if err != nil || !disabled.Disabled {
		t.Fatalf("disableUser: %+v, %v", disabled, err)
	}
	_, err = s.client(t).AccountNamespace.Login(ctx, types.LoginInput{Login: "grace@example.com", Password: "reset password"})
	expect(t, "a disabled user's login", err, 401, "invalid_credentials")
}

// testCookies: over plain HTTP, a cookie login sets the session cookie and
// answers no token; a cross-origin cookie login, and a cross-origin cookie
// request that changes state, are refused 403 cross_origin; a trusted
// origin's preflight and request get the credentialed CORS headers.
func testCookies(t *testing.T, s *server) {
	post := func(path, body string, headers ...string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, s.url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			request.Header.Set(headers[i], headers[i+1])
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response
	}
	credentials := ` + "`" + `{"login": "admin@example.com", "password": "admin password", "session": "cookie"}` + "`" + `

	refused := post("/api/auth/login", credentials, "Origin", "https://evil.example.com", "Sec-Fetch-Site", "cross-site")
	if refused.StatusCode != http.StatusForbidden || len(refused.Cookies()) != 0 {
		t.Fatalf("a cross-site cookie login answered %d with %v", refused.StatusCode, refused.Cookies())
	}

	signedIn := post("/api/auth/login", credentials, "Sec-Fetch-Site", "same-origin")
	if signedIn.StatusCode != http.StatusOK || len(signedIn.Cookies()) != 1 {
		t.Fatalf("a same-origin cookie login answered %d with %v", signedIn.StatusCode, signedIn.Cookies())
	}
	cookie := signedIn.Cookies()[0]
	if cookie.Name != identity.PlainCookieName || cookie.Secure || !cookie.HttpOnly || cookie.Value == "" {
		t.Fatalf("the session cookie is %s", cookie)
	}
	withCookie := "session=" + cookie.Value

	request, _ := http.NewRequest(http.MethodGet, s.url+"/api/auth/me", nil)
	request.Header.Set("Cookie", withCookie)
	me, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me with the cookie answered %d", me.StatusCode)
	}

	if out := post("/api/auth/logout", "", "Cookie", withCookie, "Origin", "https://evil.example.com", "Sec-Fetch-Site", "cross-site"); out.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site cookie logout answered %d", out.StatusCode)
	}

	preflight, _ := http.NewRequest(http.MethodOptions, s.url+"/api/auth/logout", nil)
	preflight.Header.Set("Origin", "https://app.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	answer, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	_ = answer.Body.Close()
	if answer.StatusCode != http.StatusNoContent || answer.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || answer.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("a trusted origin's preflight answered %d with %v", answer.StatusCode, answer.Header)
	}

	out := post("/api/auth/logout", "", "Cookie", withCookie, "Origin", "https://app.example.com", "Sec-Fetch-Site", "cross-site")
	if out.StatusCode != http.StatusOK || out.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("a trusted origin's cookie logout answered %d with %v", out.StatusCode, out.Header)
	}
	if cleared := out.Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout's cookies = %v, want the clear", cleared)
	}
	request, _ = http.NewRequest(http.MethodGet, s.url+"/api/auth/me", nil)
	request.Header.Set("Cookie", withCookie)
	after, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with the cookie after logout answered %d", after.StatusCode)
	}
}
`
