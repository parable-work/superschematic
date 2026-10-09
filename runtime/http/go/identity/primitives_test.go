package identity_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// TestConfigAccessors: the durations and the cookie name follow the
// members, defaults included, and ParseConfig refuses trailing data.
func TestConfigAccessors(t *testing.T) {
	cfg, err := identity.ParseConfig([]byte(`{"sessionTtlSeconds": 60, "idleTimeoutSeconds": 30, "touchIntervalSeconds": 10}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionTTL() != time.Minute || cfg.IdleTimeout() != 30*time.Second || cfg.TouchInterval() != 10*time.Second {
		t.Errorf("durations = %v, %v, %v", cfg.SessionTTL(), cfg.IdleTimeout(), cfg.TouchInterval())
	}
	var zero identity.Config
	if zero.SessionTTL() != 14*24*time.Hour || zero.IdleTimeout() != 0 || zero.TouchInterval() != time.Minute || zero.CookieName() != identity.HostCookieName {
		t.Errorf("a zero config's defaults: %v, %v, %v, %s", zero.SessionTTL(), zero.IdleTimeout(), zero.TouchInterval(), zero.CookieName())
	}
	if _, err := identity.ParseConfig([]byte(`{} {}`)); err == nil {
		t.Error("ParseConfig took trailing data")
	}
	if err := (identity.Config{TrustedOrigins: []string{"https://a.example/", "ftp//b"}}).Validate(); err == nil || !strings.Contains(err.Error(), "trustedOrigins[0]") || !strings.Contains(err.Error(), "trustedOrigins[1]") {
		t.Errorf("Validate names each refused origin: %v", err)
	}
}

// TestPasswords: a hash verifies its password alone, a fresh salt makes
// every hash differ, and CheckPassword is Auth.Password's rule.
func TestPasswords(t *testing.T) {
	params := identity.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1}
	a, err := identity.HashPassword("correct horse", params)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := identity.HashPassword("correct horse", params)
	if a == b {
		t.Error("two hashes of one password share a salt")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("the hash is %s", a)
	}
	if ok, rehash, err := identity.VerifyPassword(a, "correct horse", params); !ok || rehash || err != nil {
		t.Errorf("verify the password = %v, %v, %v", ok, rehash, err)
	}
	if ok, _, _ := identity.VerifyPassword(a, "correct horsE", params); ok {
		t.Error("another password verifies")
	}
	if _, _, err := identity.VerifyPassword("$2b$10$x", "x", params); err != identity.ErrMalformedHash {
		t.Errorf("a bcrypt hash: %v, want ErrMalformedHash", err)
	}
	if identity.CheckPassword("1234567") == nil || identity.CheckPassword("12345678") != nil {
		t.Error("CheckPassword is not 8 to 128 characters")
	}
}

// TestNewToken: a token is 43 characters of base64url, and two differ.
func TestNewToken(t *testing.T) {
	a, err := identity.NewToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := identity.NewToken(nil)
	if a == b || len(a) != identity.TokenLength || strings.ContainsAny(a, "+/=") {
		t.Errorf("tokens %q and %q", a, b)
	}
	if h := identity.HashToken(a); len(h) != 64 || strings.ToLower(h) != h {
		t.Errorf("the token's hash is %q", h)
	}
	if _, err := identity.NewToken(strings.NewReader("short")); err == nil {
		t.Error("a token from too few random bytes")
	}
}

// TestPermissionsHelpers: a route admits by the router's rule, and a
// matcher the project passes replaces the default.
func TestPermissionsHelpers(t *testing.T) {
	route := identity.Route{OperationID: "x", Permissions: []string{"a.b"}}
	if route.Admits(false, nil, nil) {
		t.Error("a route with permissions admits no caller")
	}
	if !route.Admits(true, []session.Role{{Permissions: []string{"a"}}}, nil) {
		t.Error("a covering permission is refused")
	}
	if !(identity.Route{OperationID: "public"}).Admits(false, nil, nil) {
		t.Error("a public route refuses no caller")
	}
	if (identity.Route{OperationID: "own", RequireOwnership: true}).Admits(false, nil, nil) {
		t.Error("an ownership route admits no caller")
	}
	exact := func(roles []session.Role, required []string) bool {
		for _, r := range roles {
			for _, p := range r.Permissions {
				for _, req := range required {
					if p == req {
						return true
					}
				}
			}
		}
		return false
	}
	if route.Admits(true, []session.Role{{Permissions: []string{"a"}}}, exact) {
		t.Error("the project's matcher was not used")
	}
}

// TestCheckCrossOrigin: a cookie request's check refuses another origin's
// POST and lets its GET through.
func TestCheckCrossOrigin(t *testing.T) {
	var cfg identity.Config
	post := httptest.NewRequest("POST", "http://api.example.com/x", nil)
	post.Header.Set("Sec-Fetch-Site", "cross-site")
	if identity.CheckCrossOrigin(cfg, post) == nil {
		t.Error("a cross-site POST passes")
	}
	get := httptest.NewRequest("GET", "http://api.example.com/x", nil)
	get.Header.Set("Sec-Fetch-Site", "cross-site")
	if err := identity.CheckCrossOrigin(cfg, get); err != nil {
		t.Errorf("a cross-site GET: %v", err)
	}
}

// TestCORSWithoutOrigin: a request without Origin passes through untouched.
func TestCORSWithoutOrigin(t *testing.T) {
	h := identity.CORS([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(418) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("OPTIONS", "http://api.example.com/x", nil))
	if rec.Code != 418 || len(rec.Header()) != 0 {
		t.Errorf("a request without Origin: %d %v", rec.Code, rec.Header())
	}
}

// TestStoreQuotesNames: every table and column name is quoted, so names
// with a double quote, a space or a ? reach the tables the DDL created.
func TestStoreQuotesNames(t *testing.T) {
	ddl := strings.NewReplacer(`"user" `, `"app ""user"" ?" `, `"session" `, `"a session" `, `"email"`, `"e?mail"`).Replace(string(readFixture(t, "sqlite/create.sql")))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "names.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("apply the renamed DDL: %v", err)
	}
	s, err := identity.NewSQLStore(db, identity.SQLite, descriptorJSON(t, func(d map[string]any) {
		user := d["user"].(map[string]any)
		user["table"] = `app "user" ?`
		user["columns"].(map[string]any)["login"] = "e?mail"
		d["session"].(map[string]any)["table"] = "a session"
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice := mustCreateUser(t, s, "alice@example.com", "Alice")
	if rec, err := s.FindLogin(ctx, "Alice@example.com"); err != nil || rec.User.ID != alice.ID {
		t.Fatalf("FindLogin = %+v, %v", rec, err)
	}
	mustSession(t, s, alice, hashOf(1))
	if rec := findSession(t, s, hashOf(1)); rec.User.Login != "alice@example.com" {
		t.Errorf("FindSession = %+v", rec)
	}
	if users, err := s.ListUsers(ctx); err != nil || len(users) != 1 {
		t.Errorf("ListUsers = %+v, %v", users, err)
	}
}
