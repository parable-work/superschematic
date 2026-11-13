package servergen_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The identity fixture as Rust APIs over a SQLite file (D116): users-db
// with the sqlite dialect listed first, and users-api and users-admin in
// Rust, which users-stack serves from its one server, Accounts, on the
// local target. The APIs' users, their sessions and their grants are kept
// in the runtime's identity store on the file.
const rustIdentitySQLiteGoldenRoot = "testdata/golden/identity/rust-sqlite"

// loadRustIdentityOnSQLite loads the identity fixture with users-db
// listing sqlite before postgres and its two APIs written in Rust.
func loadRustIdentityOnSQLite(t *testing.T) fixture {
	t.Helper()
	f := loadIdentityFixture(t)
	enabled := map[string]any{"enabled": true}
	f.configs["users-db"].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"sqlite", "postgres"}},
	}
	for _, api := range []string{"users-api", "users-admin"} {
		f.configs[api].Outputs = map[string]any{
			"types": map[string]any{"rust": enabled},
			"api":   map[string]any{"enabled": true, "language": "RUST"},
		}
	}
	return f
}

// TestRustIdentityEntrypointOnSQLiteGolden: a Rust server whose APIs
// authenticate with the identity runtime over a database whose DB service
// lists sqlite first (D116) opens the database's SQLite file for the
// APIs' Deps and builds one identity store on a connection of its own to
// the file, by the API crate's `identity::store_on_file`, and each API's
// identity service over it. The API crate takes the runtime's
// identity-sqlite feature. Its main.rs is laid out as rustfmt lays it out.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustIdentityEntrypointOnSQLiteGolden -update
func TestRustIdentityEntrypointOnSQLiteGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustIdentityOnSQLite(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), identityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, identityStack, identityServer)
	api := generator.APIDir(out, "users-api")
	files := map[string]string{
		filepath.Join("api", "users-api", "Cargo.toml"):         filepath.Join(api, "Cargo.toml"),
		filepath.Join("api", "users-api", "src", "identity.rs"): filepath.Join(api, "src", "identity.rs"),
	}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile} {
		files[filepath.Join("server", identityStack, identityServer, filepath.FromSlash(file))] = filepath.Join(dir, filepath.FromSlash(file))
	}
	compareGoldens(t, rustIdentitySQLiteGoldenRoot, files)

	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	source := filepath.Join(dir, filepath.FromSlash(servergen.RustMainFile))
	main := read(source)
	for _, want := range []string{
		"use superschematic_http_runtime::identity::Store;",
		"let users_db_pool = open_file(",
		"let users_db_identity_store: Arc<dyn Store> = Arc::new(\n        users_admin_api::identity::store_on_file(&users_db_pool)",
		"users_api_api::identity::service(\n        Arc::clone(&users_db_identity_store),",
		"users_admin_api::identity::service(\n        Arc::clone(&users_db_identity_store),",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.rs does not contain %q:\n%s", want, main)
		}
	}
	for _, absent := range []string{"PostgresPool", "postgres::", "fn connect("} {
		if strings.Contains(main, absent) {
			t.Errorf("main.rs holds %s, which a server on a SQLite file has none of:\n%s", absent, main)
		}
	}
	if manifest := read(filepath.Join(api, "Cargo.toml")); !strings.Contains(manifest, `features = ["identity", "sqlite", "identity-sqlite"]`) {
		t.Errorf("the API crate does not take the runtime's identity-sqlite feature:\n%s", manifest)
	}
	identity := read(filepath.Join(api, "src", "identity.rs"))
	for _, want := range []string{
		"pub fn store_on_file(\n    db: &superschematic_http_runtime::orm::sqlite::Database,\n) -> Result<SqlStore, StoreError> {",
		"superschematic_http_runtime::identity::Rusqlite::open(db.path())",
	} {
		if !strings.Contains(identity, want) {
			t.Errorf("src/identity.rs does not contain %q:\n%s", want, identity)
		}
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
		}
	}
}

// TestRustIdentityEntrypointServesOnSQLite: the Rust server of the
// identity fixture builds in the output root's workspace over a database
// whose DB service lists sqlite first, passes clippy, and serves
// users-db's user model on a SQLite file the build's SQLite DDL creates
// (D116): an end user registers through users-api and signs in, the
// session authenticates the project's route, which the scaffold answers,
// and me; users-admin, which serves the administration routes over the
// same store, refuses the user a permission they do not hold; a second
// user's login taken is a conflict; a signed-out session is refused; and
// the user and their session outlive the server, which a second start
// over the file signs in again.
//
// The test needs cargo and the sqlite3 command, which it skips without,
// and runs outside -short.
func TestRustIdentityEntrypointServesOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not available; skipping the Rust server over a SQLite file")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadRustIdentityOnSQLite(t)
	f.build(t, repoRoot, paths, identityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--quiet", "-p", naming.Default().RustServerCrate(identityStack, identityServer))
	binary := filepath.Join(target, "debug", identityServer)

	// The file, from the SQLite DDL the build wrote.
	file := filepath.Join(testpaths.TempDir(t), "users.sqlite")
	sql := func(statement string) string {
		t.Helper()
		output, err := exec.Command(sqlite3, "-cmd", ".timeout 5000", file, statement).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 %s: %v\n%s", statement, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	sql(".read " + filepath.Join(generator.SQLDir(out, "users-db"), generator.SQLiteSubdir, "create.sql"))

	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	env := []string{
		"USERS_DB_DATABASE_URL=sqlite:" + file,
		"USERS_API_IDENTITY=" + identityConfig,
		"USERS_ADMIN_IDENTITY=" + identityConfig,
		"USERS_API_CORS_ORIGINS=https://users-web.example.com",
	}
	online := start(t, binary, env...)
	online.expect(t, http.MethodGet, "/readyz", http.StatusOK)
	online.expect(t, http.MethodGet, "/api/greeting", http.StatusUnauthorized)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ADA@example.com", "name": "Ada too", "password": "another password"}`, http.StatusConflict)
	token := online.login(t, "ada@example.com", "ada's password")
	online.authorized(t, token, http.MethodGet, "/api/greeting", http.StatusNotImplemented, "greet")
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	online.authorized(t, token, http.MethodGet, "/api/auth/admin/users", http.StatusForbidden, `"code":"forbidden"`)
	online.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	kept := online.login(t, "ada@example.com", "ada's password")
	online.stop(t)

	// The file holds the one user and her two sessions, one of them
	// revoked.
	if users := sql(`SELECT COUNT(*) FROM account`); users != "1" {
		t.Errorf("the file holds %s users, want ada alone", users)
	}
	if sessions := sql(`SELECT COUNT(*) FROM session`); sessions != "3" {
		t.Errorf("the file holds %s sessions, want the one her registration issued and the two she signed in for", sessions)
	}

	// A second start over the file serves what the first wrote.
	again := start(t, binary, env...)
	again.authorized(t, kept, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	again.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	again.stop(t)
}
