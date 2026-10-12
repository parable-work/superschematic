package rustrestgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// userRoutesAPI is the API whose @userSessions and @userAdministration sets
// the loader fills from fixture-user-model-db (D50).
const userRoutesAPI = "fixture-user-routes-api"

// routesOnly leaves fixture-user-routes-api its route sets alone.
func routesOnly(schema *ir.Schema) {
	schema.OperationSets = slices.DeleteFunc(schema.OperationSets, func(set *ir.OperationSet) bool { return !set.IsIdentityRoutes() })
	delete(schema.Types, "Greeting")
}

// TestWriteRustAPIGoldenUserRoutes pins the crate for
// fixture-user-routes-api (D50): the Implementations traits have the
// project's operation alone, Implementations.authenticator is the identity
// runtime's, the router mounts the runtime's handler of each of the user
// model's routes, behind its rate limit and a caller, and wraps itself in
// the trusted origins' CORS, the operation table and the capabilities
// route table list every operation, and src/identity.rs holds the authDb's
// descriptor. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenUserRoutes -update
func TestWriteRustAPIGoldenUserRoutes(t *testing.T) {
	output := generateRustAPI(t, userRoutesAPI, false, "", nil)
	if len(output.Endpoints) != 1 || output.Endpoints[0].Name != "greet" || !slices.Equal(output.Namespaces, []string{"greeting"}) {
		t.Errorf("endpoints %+v, namespaces %v; want greet alone", output.Endpoints, output.Namespaces)
	}
	if len(output.IdentityEndpoints) != 18 || output.Identity == nil || output.Identity.AuthDB != "fixture-user-model-db" {
		t.Fatalf("%d identity endpoints, identity %+v; want the 18 of the user model's routes over fixture-user-model-db", len(output.IdentityEndpoints), output.Identity)
	}
	generated := writeGoldenAPI(t, userRoutesAPI, output)
	for _, absent := range []string{"login", "change_password", "account", "Login"} {
		if strings.Contains(generated["src/interfaces.rs"], absent) {
			t.Errorf("src/interfaces.rs mentions %s, which the identity runtime serves", absent)
		}
	}
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-http-runtime = { path = "../../../../runtime/http/rust", features = ["identity"] }`,
			`identity-sqlite = ["superschematic-http-runtime/identity-sqlite"]`,
			`superscalar = { path = "../../../../third_party/superscalar/crates/core" }`,
		},
		"src/interfaces.rs": {"pub authenticator: Arc<IdentityAuthenticator>,"},
		"src/router.rs": {
			".rate_limit(10)\n        .apply(identity_handler(&identity_service, identity::OP_LOGIN));\n    router = router.route(\"/api/auth/login\", route);",
			`router = router.route("/api/auth/admin/users/{id}/roles/{roleId}", route);`,
			".layer(identity_service.cors())",
		},
		"src/operations.rs": {"pub const ACCOUNT_ADMIN_GRANT_ROLE: OperationInfo", `permissions: &["identity.roles.write"],`},
		"src/identity.rs":   {`Route::of("AccountLoginHandler", &operations::ACCOUNT_LOGIN),`, `Route::of("GreetingGreetHandler", &operations::GREETING_GREET),`},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	// An administration route's handler checks its permission itself, under
	// the service's prefix: the router asks only for a caller.
	if strings.Contains(generated["src/router.rs"], `"identity.`) {
		t.Error("src/router.rs checks an administration route's permission, which the identity runtime's handler checks")
	}
	if !strings.Contains(generated["openapi.json"], "/api/auth/login") {
		t.Error("openapi.json does not describe the user model's routes")
	}
	if !strings.Contains(output.PermissionCatalogJSON, `"identity.roles.write"`) {
		t.Error("the crate carries no permissions.json with the administration routes' permissions")
	}

	without := generateRustAPI(t, userRoutesAPI, false, "", nil, noRouteSets)
	if len(without.Endpoints) != 1 || len(without.IdentityEndpoints) != 0 || without.Identity == nil {
		t.Errorf("no route sets: endpoints %+v, %d identity endpoints, identity %+v; want greet authenticated by the identity runtime", without.Endpoints, len(without.IdentityEndpoints), without.Identity)
	}

	alone := generateRustAPI(t, userRoutesAPI, false, "", nil, routesOnly)
	if len(alone.Endpoints) != 0 || len(alone.Namespaces) != 0 || len(alone.IdentityEndpoints) != 18 || !alone.HasAuth || alone.Identity == nil {
		t.Errorf("the route sets alone: endpoints %+v, namespaces %v, %d identity endpoints, HasAuth %v, identity %+v", alone.Endpoints, alone.Namespaces, len(alone.IdentityEndpoints), alone.HasAuth, alone.Identity)
	}
}

// TestUserRoutesNeedTheirAuthDB: the user model's routes read the authDb's
// User table, so a crate generated without it is refused rather than
// written with routes nothing serves.
func TestUserRoutesNeedTheirAuthDB(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	_, err = generateFrom(schema, apiSource{}, Options{SchemaName: userRoutesAPI, Clock: nestedArraysClock})
	if err == nil || !strings.Contains(err.Error(), "authDb's User table") {
		t.Fatalf("generate without the authDb: %v, want the routes refused", err)
	}
}

// noRouteSets leaves fixture-user-routes-api its project operation alone:
// an API whose authDb has a User table and that serves none of the user
// model's routes, which another API of the authDb serves.
func noRouteSets(schema *ir.Schema) {
	schema.OperationSets = slices.DeleteFunc(schema.OperationSets, (*ir.OperationSet).IsIdentityRoutes)
}

// TestUserRoutesAPICrateBuilds runs clippy and cargo test on the Rust API
// crate of fixture-user-routes-api, of the API with its route sets alone,
// with no implementation of the user model's operations, and of the API
// without them, which still authenticates with the identity runtime.
func TestUserRoutesAPICrateBuilds(t *testing.T) {
	for name, build := range map[string]struct {
		edit func(*ir.Schema)
		test string
	}{
		"with a project operation": {func(*ir.Schema) {}, userRoutesCrateTest + identityCrateTest},
		"the route sets alone":     {routesOnly, userRoutesCrateTest + identityCrateTest},
		"no route sets":            {noRouteSets, identityCrateTest},
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
			if err != nil {
				t.Fatalf("load %s: %v", userRoutesAPI, err)
			}
			build.edit(schema)
			cargoTestAPICrate(t, userRoutesAPI, schema, "user_routes", build.test)
		})
	}
}

// userRoutesCrateTest and identityCrateTest make tests/user_routes.rs of
// the generated API crate, with API_CRATE replaced by its module name: the
// crate builds with no implementation of the user model's operations, its
// OpenAPI document still describes their routes, and its identity module
// holds the authDb's descriptor and a route per operation.
const userRoutesCrateTest = `#[test]
fn the_openapi_document_describes_the_user_model_routes() {
    for path in ["/api/auth/login", "/api/auth/me", "/api/auth/admin/users/{id}/roles/{roleId}"] {
        assert!(API_CRATE::openapi::OPENAPI_JSON.contains(path), "no {path}");
    }
}
`

const identityCrateTest = `
#[test]
fn the_descriptor_is_the_auth_db_s() {
    assert!(API_CRATE::identity::IDENTITY_DESCRIPTOR.contains(r#""loginScalar": "Contact.Email""#));
    assert_eq!(API_CRATE::identity::routes().len(), API_CRATE::operations::ALL.len());
}
`

// identityTestDatabaseEnv names the Postgres database the end-to-end test
// also runs on, as the runtime's identity tests do.
const identityTestDatabaseEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"

// TestRustSDKCallsTheUserModelRoutes builds fixture-user-routes-api's crate
// with its SQLite and Postgres stores over the authDb's DDL, serves
// build_router on a local socket, and drives it through the generated
// Rust SDK with bearer sessions, and through tower's oneshot with the
// session cookie a browser holds (identityRoundtripTest): register, login,
// me, capabilities, the project's own route, changePassword and logout; an
// administrator who writes and grants a role within their own permissions
// and no further; a cookie login, a cross-origin refusal, a trusted
// origin's CORS, a refused cookie cleared; and login's rate limit. Each
// runs on SQLite, and on the Postgres database identityTestDatabaseEnv
// names when it is set.
func TestRustSDKCallsTheUserModelRoutes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	authDB := authDBOf(t, schema)
	files := identityDDL(t, authDB)
	if os.Getenv(identityTestDatabaseEnv) == "" {
		t.Logf("%s is unset: the crate's tests run on SQLite alone", identityTestDatabaseEnv)
	}
	sdkCrate := strings.ReplaceAll(naming.Default().RustSDKCrate(userRoutesAPI), "-", "_")
	writeDDL := func(apiDir string, _ *APIOutput) error {
		if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
			return err
		}
		for name, ddl := range files {
			if err := os.WriteFile(filepath.Join(apiDir, "tests", name), []byte(ddl), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	cargoTestAPICrateWith(t, userRoutesAPI, schema, "identity", strings.ReplaceAll(identityRoundtripTest, "SDK_CRATE", sdkCrate), cargoOptions{
		features: []string{"identity-postgres", "identity-sqlite"},
		devDependencies: `rusqlite = { version = "0.40.2", features = ["bundled"] }
tokio-postgres = { version = "0.7.18", features = ["with-uuid-1"] }
`,
	}, withSDK(schema, userRoutesAPI), writeDDL)
}

// identityDDL is authDB's DDL as the build writes it, by the file name the
// end-to-end test reads it from: identity.sql for SQLite and
// identity_postgres.sql for Postgres.
func identityDDL(t *testing.T, authDB *ir.Schema) map[string]string {
	t.Helper()
	opts := sqlgen.Options{SchemaName: authDB.Name}
	model, err := sqlmigrate.BuildModel(authDB, opts, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build the SQLite model of %s: %v", authDB.Name, err)
	}
	sqlite, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("render the SQLite DDL of %s: %v", authDB.Name, err)
	}
	ddl, err := sqlgen.Generate(authDB, opts)
	if err != nil {
		t.Fatalf("generate the DDL of %s: %v", authDB.Name, err)
	}
	dir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, dir); err != nil {
		t.Fatalf("write the DDL of %s: %v", authDB.Name, err)
	}
	postgres, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"identity.sql": sqlite, "identity_postgres.sql": string(postgres)}
}

// identityRoundtripTest is tests/identity.rs of fixture-user-routes-api's
// API crate, beside the authDb's DDL.
const identityRoundtripTest = `//! The user model end to end (D50): the crate's router over its identity
//! store, called through the generated Rust SDK with bearer sessions, and
//! through tower's oneshot with the session cookie a browser holds. Each
//! test runs on SQLite, and on the Postgres database
//! SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL names when it is set.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::SystemTime;

use API_CRATE::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Rusqlite, SqlStore, Store, TokioPostgres,
    HOST_COOKIE_NAME,
};
use API_CRATE::runtime::{ApiError, RequestContext};
use API_CRATE::{build_router, identity, types, GreetingImplementation, Implementations};
use SDK_CRATE::{ClientConfig, FixtureUserRoutesApiSdk};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{HeaderMap, Request, StatusCode};
use axum::Router;
use serde_json::{json, Value};
use tower::ServiceExt;

/// A config at a low argon2 cost, so a test hashes quickly, with one
/// trusted origin.
const CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://app.example.com"]}"#;

const ADMIN: &str = "admin@example.com";
const ADMIN_PASSWORD: &str = "admin password";

/// A database holding the authDb's tables, and the Postgres schema to drop
/// after the test.
struct Database {
    name: &'static str,
    store: Arc<SqlStore>,
    cleanup: Option<(tokio_postgres::Client, String)>,
}

impl Database {
    async fn close(self) {
        if let Some((admin, schema)) = self.cleanup {
            admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
        }
    }
}

/// SQLite in memory, and Postgres when the variable names a database.
async fn databases() -> Vec<Database> {
    let connection = rusqlite::Connection::open_in_memory().unwrap();
    connection.execute_batch(include_str!("identity.sql")).unwrap();
    let sqlite = identity::store(Rusqlite::new(connection).unwrap()).unwrap();
    let mut databases = vec![Database { name: "sqlite", store: Arc::new(sqlite), cleanup: None }];
    if let Some(postgres) = postgres().await {
        databases.push(postgres);
    }
    databases
}

/// A Postgres connection whose search path is a schema of its own holding
/// the authDb's tables, with the DDL's extensions created once in public.
async fn postgres() -> Option<Database> {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let url = std::env::var("SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL").ok().filter(|url| !url.is_empty())?;
    let connect = |config: tokio_postgres::Config| async move {
        let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
        tokio::spawn(connection);
        client
    };
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(config.clone()).await;
    for extension in ["pgcrypto", "citext"] {
        if let Err(err) = admin.batch_execute(&format!("CREATE EXTENSION IF NOT EXISTS {extension} SCHEMA public")).await {
            // Two tests creating one extension at once: the other won.
            assert_eq!(err.code().map(|code| code.code()), Some("23505"), "create {extension}: {err}");
        }
    }
    let schema = format!("identity_e2e_{}_{}", std::process::id(), NEXT.fetch_add(1, Ordering::SeqCst));
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(scoped).await;
    client.batch_execute(include_str!("identity_postgres.sql")).await.unwrap();
    let store = identity::store(TokioPostgres::new(client)).unwrap();
    Some(Database { name: "postgres", store: Arc::new(store), cleanup: Some((admin, schema)) })
}

/// The project's own operation: a greeting for the caller.
struct Greetings;

#[async_trait]
impl GreetingImplementation for Greetings {
    async fn greet(&self, ctx: RequestContext) -> Result<types::Greeting, ApiError> {
        let name = ctx
            .principal
            .as_ref()
            .and_then(|principal| principal.claims.get("name"))
            .and_then(Value::as_str)
            .unwrap_or_default();
        Ok(types::Greeting { message: format!("Hello, {name}") })
    }
}

/// The API over the database's store, with an administrator seeded as a
/// deployment's bootstrap seeds one: a role holding the identity
/// permissions, the user, and the grant.
async fn implementations(database: &Database) -> Implementations {
    eprintln!("on {}", database.name);
    let store = Arc::clone(&database.store);
    let config = Config::parse(CONFIG.as_bytes()).unwrap();
    let admin = store
        .create_user(NewUser {
            login: ADMIN.to_string(),
            name: "Admin".to_string(),
            password_hash: hash_password(ADMIN_PASSWORD, config.argon2_params()).unwrap(),
            at: SystemTime::now(),
        })
        .await
        .unwrap();
    let role = store.create_role("admin", &["identity".to_string()]).await.unwrap();
    store.grant_role(&admin.id, &role.id, SystemTime::now()).await.unwrap();
    let service = Arc::new(identity::service(store, config).unwrap());
    Implementations {
        greeting: Arc::new(Greetings),
        authenticator: Arc::new(IdentityAuthenticator::new(service)),
    }
}

async fn serve(implementations: Implementations) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

fn sdk(base_url: &str, token: Option<String>) -> FixtureUserRoutesApiSdk {
    FixtureUserRoutesApiSdk::new(ClientConfig::with_base_url(base_url, token, None)).unwrap()
}

fn login(login: &str, password: &str) -> types::LoginInput {
    types::LoginInput { login: login.to_string(), password: password.to_string(), session: None }
}

fn register(login: &str, name: &str) -> types::RegisterInput {
    types::RegisterInput {
        login: login.to_string(),
        name: Some(name.to_string()),
        password: "correct horse".to_string(),
        session: None,
    }
}

#[tokio::test]
async fn a_user_signs_in_with_a_bearer_session() {
    for database in databases().await {
        bearer_session(implementations(&database).await).await;
        database.close().await;
    }
}

async fn bearer_session(implementations: Implementations) {
    let base_url = serve(implementations).await;
    let anonymous = sdk(&base_url, None);
    let err = anonymous.greeting.greet(None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(401), Some("unauthorized")), "{err}");

    // register signs the new user in with a bearer session, their login in
    // the scalar's one case.
    let registered = anonymous.account.register(register("Ada@Example.com", "Ada"), None).await.unwrap();
    assert_eq!(registered.user.login, "ada@example.com");
    let ada = sdk(&base_url, Some(registered.token.expect("a bearer session's token")));

    let me = ada.account.me(None).await.unwrap();
    assert_eq!((me.user.login.as_str(), me.user.name.as_str()), ("ada@example.com", "Ada"));
    assert!(me.roles.is_empty() && me.permissions.is_empty(), "{me:?}");
    assert_eq!(ada.greeting.greet(None).await.unwrap().message, "Hello, Ada");
    let capabilities = ada.account.capabilities(None).await.unwrap().operations;
    for (operation, admitted) in [
        ("GreetingGreetHandler", true),
        ("AccountMeHandler", true),
        ("AccountLoginHandler", true),
        ("AccountAdminListUsersHandler", false),
    ] {
        assert_eq!(capabilities.get(operation), Some(&admitted), "{operation}: {capabilities:?}");
    }

    // A password change ends the user's other sessions and keeps the
    // caller's.
    let second = anonymous.account.login(login("ada@example.com", "correct horse"), None).await.unwrap();
    let elsewhere = sdk(&base_url, second.token);
    assert!(elsewhere.account.me(None).await.is_ok());
    let change = types::ChangePasswordInput { current: "correct horse".to_string(), password: "battery staple".to_string() };
    assert!(ada.account.change_password(change, None).await.unwrap());
    let err = elsewhere.account.me(None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(401), Some("unauthorized")), "{err}");
    assert_eq!(ada.account.me(None).await.unwrap().user.name, "Ada");

    let err = anonymous.account.login(login("ada@example.com", "correct horse"), None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(401), Some("invalid_credentials")), "{err}");
    let again = anonymous.account.login(login("ADA@example.com", "battery staple"), None).await.unwrap();
    assert!(again.token.is_some());

    // logout ends the caller's session.
    assert!(ada.account.logout(None).await.unwrap());
    let err = ada.account.me(None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401), "{err}");
}

#[tokio::test]
async fn an_administrator_grants_no_more_than_they_hold() {
    for database in databases().await {
        grant_rule(implementations(&database).await).await;
        database.close().await;
    }
}

async fn grant_rule(implementations: Implementations) {
    let base_url = serve(implementations).await;
    let anonymous = sdk(&base_url, None);
    let admin = sdk(&base_url, anonymous.account.login(login(ADMIN, ADMIN_PASSWORD), None).await.unwrap().token);
    let registered = anonymous.account.register(register("member@example.com", "Member"), None).await.unwrap();
    let member_id = registered.user.id;
    let member = sdk(&base_url, registered.token);

    let err = member.account_admin.list_users(None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(403), Some("forbidden")), "{err}");

    // The administrator holds identity, which covers identity.users.read
    // and not orders.write.
    let auditor = types::RoleInput { name: "auditor".to_string(), permissions: vec!["identity.users.read".to_string()] };
    let auditor = admin.account_admin.create_role(auditor, None).await.unwrap();
    let seller = types::RoleInput { name: "seller".to_string(), permissions: vec!["orders.write".to_string()] };
    let err = admin.account_admin.create_role(seller, None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(403), Some("forbidden")), "{err}");
    assert_eq!(err.problem().unwrap().details, Some(json!({"permissions": ["orders.write"]})));

    // A grant reaches the member's next request.
    let granted = admin.account_admin.grant_role(member_id, auditor.id, None).await.unwrap();
    assert_eq!(granted.roles.iter().map(|role| role.name.as_str()).collect::<Vec<_>>(), ["auditor"]);
    let users = member.account_admin.list_users(None).await.unwrap();
    assert_eq!(users.len(), 2, "{users:?}");
    let capabilities = member.account.capabilities(None).await.unwrap().operations;
    assert_eq!(capabilities.get("AccountAdminListUsersHandler"), Some(&true), "{capabilities:?}");
    assert_eq!(capabilities.get("AccountAdminCreateUserHandler"), Some(&false), "{capabilities:?}");

    // identity.users.read grants no role.
    let err = member.account_admin.grant_role(member_id, auditor.id, None).await.unwrap_err();
    assert_eq!((err.status_code(), err.error_code()), (Some(403), Some("forbidden")), "{err}");
}

/// What a route answered.
struct Reply {
    status: StatusCode,
    headers: HeaderMap,
    body: Value,
}

impl Reply {
    fn code(&self) -> &str {
        self.body["code"].as_str().unwrap_or_default()
    }

    fn cookies(&self) -> Vec<&str> {
        self.headers.get_all("set-cookie").iter().map(|value| value.to_str().unwrap()).collect()
    }
}

async fn send(router: &Router, method: &str, path: &str, headers: &[(&str, &str)], body: Option<Value>) -> Reply {
    let mut request = Request::builder().method(method).uri(path).header("host", "api.example.com");
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let body = body.map_or_else(Body::empty, |body| Body::from(body.to_string()));
    let response = router.clone().oneshot(request.body(body).unwrap()).await.unwrap();
    let status = response.status();
    let headers = response.headers().clone();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = if bytes.is_empty() { Value::Null } else { serde_json::from_slice(&bytes).unwrap() };
    Reply { status, headers, body }
}

#[tokio::test]
async fn a_browser_signs_in_with_the_session_cookie() {
    for database in databases().await {
        cookie_session(build_router(implementations(&database).await)).await;
        database.close().await;
    }
}

async fn cookie_session(router: Router) {
    let credentials = json!({"login": ADMIN, "password": ADMIN_PASSWORD, "session": "cookie"});

    // A cookie login sets the cookie and answers no token, so no script
    // reads it.
    let signed_in = send(&router, "POST", "/api/auth/login", &[("sec-fetch-site", "same-origin")], Some(credentials.clone())).await;
    assert_eq!(signed_in.status, StatusCode::OK, "{}", signed_in.body);
    assert!(signed_in.body["data"].get("token").is_none(), "{}", signed_in.body);
    let set = signed_in.cookies();
    assert_eq!(set.len(), 1, "{set:?}");
    assert!(set[0].starts_with(&format!("{HOST_COOKIE_NAME}=")), "{}", set[0]);
    for attribute in ["Path=/", "HttpOnly", "Secure", "SameSite=Lax"] {
        assert!(set[0].contains(attribute), "{} lacks {attribute}", set[0]);
    }
    let session = set[0].split(';').next().unwrap();
    let cookie = [("cookie", session)];

    // The cookie signs in the session routes and the project's own.
    let me = send(&router, "GET", "/api/auth/me", &cookie, None).await;
    assert_eq!(me.status, StatusCode::OK, "{}", me.body);
    assert_eq!(me.body["data"]["user"]["login"], ADMIN);
    assert_eq!(me.body["data"]["permissions"], json!(["identity"]));
    let greeting = send(&router, "GET", "/api/greeting", &cookie, None).await;
    assert_eq!(greeting.body["data"]["message"], "Hello, Admin", "{}", greeting.body);

    // Another site's cookie request is refused before the session is read,
    // and so is its cookie login.
    let cross_site = [("cookie", session), ("origin", "https://evil.example.com"), ("sec-fetch-site", "cross-site")];
    let refused = send(&router, "POST", "/api/auth/logout", &cross_site, None).await;
    assert_eq!((refused.status, refused.code()), (StatusCode::FORBIDDEN, "cross_origin"), "{}", refused.body);
    assert!(refused.headers.get("access-control-allow-origin").is_none());
    let refused = send(&router, "POST", "/api/auth/login", &cross_site[1..], Some(credentials)).await;
    assert_eq!((refused.status, refused.code()), (StatusCode::FORBIDDEN, "cross_origin"), "{}", refused.body);
    assert!(refused.cookies().is_empty(), "{:?}", refused.cookies());

    // A trusted origin's preflight is answered, and its cookie request
    // passes with the credentialed CORS headers.
    let preflight = send(
        &router,
        "OPTIONS",
        "/api/auth/logout",
        &[("origin", "https://app.example.com"), ("access-control-request-method", "POST")],
        None,
    )
    .await;
    assert_eq!(preflight.status, StatusCode::NO_CONTENT);
    assert_eq!(preflight.headers["access-control-allow-origin"], "https://app.example.com");
    assert_eq!(preflight.headers["access-control-allow-credentials"], "true");
    let trusted = [("cookie", session), ("origin", "https://app.example.com"), ("sec-fetch-site", "cross-site")];
    let signed_out = send(&router, "POST", "/api/auth/logout", &trusted, None).await;
    assert_eq!(signed_out.status, StatusCode::OK, "{}", signed_out.body);
    assert_eq!(signed_out.body["data"], true);
    assert_eq!(signed_out.headers["access-control-allow-origin"], "https://app.example.com");
    let cleared = format!("{HOST_COOKIE_NAME}=; Path=/; Max-Age=0");
    assert!(signed_out.cookies().iter().any(|c| c.starts_with(&cleared)), "{:?}", signed_out.cookies());

    // The ended session's cookie is refused, and the refusal clears it.
    let ended = send(&router, "GET", "/api/auth/me", &cookie, None).await;
    assert_eq!((ended.status, ended.code()), (StatusCode::UNAUTHORIZED, "unauthorized"), "{}", ended.body);
    assert!(ended.cookies().iter().any(|c| c.starts_with(&cleared)), "{:?}", ended.cookies());
    let ended = send(&router, "GET", "/api/greeting", &cookie, None).await;
    assert_eq!(ended.status, StatusCode::UNAUTHORIZED, "{}", ended.body);
    assert!(ended.cookies().iter().any(|c| c.starts_with(&cleared)), "{:?}", ended.cookies());
}

#[tokio::test]
async fn login_is_rate_limited() {
    for database in databases().await {
        rate_limit(build_router(implementations(&database).await)).await;
        database.close().await;
    }
}

async fn rate_limit(router: Router) {
    let wrong = json!({"login": ADMIN, "password": "not the password"});
    for attempt in 0..10 {
        let refused = send(&router, "POST", "/api/auth/login", &[], Some(wrong.clone())).await;
        assert_eq!((refused.status, refused.code()), (StatusCode::UNAUTHORIZED, "invalid_credentials"), "attempt {attempt}");
    }
    let limited = send(&router, "POST", "/api/auth/login", &[], Some(wrong)).await;
    assert_eq!(limited.status, StatusCode::TOO_MANY_REQUESTS, "{}", limited.body);
    assert!(limited.headers.contains_key("retry-after"));
}
`
