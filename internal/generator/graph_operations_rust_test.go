package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGraphOperationsRustServerServesTheGraph holds the generated Rust
// server's graph operations (D138) to the scenario the Go server's are held
// to, case for case: it builds fixture-version-graph-db and
// fixture-graph-api as a Rust server, with a binary over the generated
// router that authenticates as the scenario's test authenticator does, and
// runs graphOperationsTest in the Go module of the same API with that
// binary in the Go server's place, on the Postgres graphServerDatabaseEnv
// names. Each operation runs through the Recipe graph's Rust facade over
// the server's pool, and the root, its refs and its commits are read
// through the Rust repositories of fixture-version-graph-db. The build
// warns of no graph operation, and the trait holds get_recipe alone.
//
// The test skips without the database, without cargo and in -short mode.
func TestGraphOperationsRustServerServesTheGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's graph operations in -short mode")
	}
	if os.Getenv(graphServerDatabaseEnv) == "" {
		t.Skip("set " + graphServerDatabaseEnv + " to run the generated Rust server's graph operations against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's graph operations")
	}
	paths := testpaths.Local(t)

	// The Rust server, and a binary that serves its router.
	rustRoot := testpaths.TempDir(t)
	result := buildGraphFixture(t, rustRoot, APILanguageRust)
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "@graphOperations") {
			t.Errorf("the Rust build warned of a graph operation: %s", warning)
		}
	}
	apiCrate := APIDir(rustRoot, "fixture-graph-api")
	interfaces, err := os.ReadFile(filepath.Join(apiCrate, "src", "interfaces.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(interfaces), "async fn get_recipe(") || strings.Contains(string(interfaces), "async fn branch(") {
		t.Errorf("the Rust trait holds other than get_recipe:\n%s", interfaces)
	}
	crateDir := filepath.Join(rustRoot, "server", "recipes")
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	names := naming.Default()
	for name, content := range map[string]string{
		"Cargo.toml":  fmt.Sprintf(graphOperationsRustManifest, names.RustAPICrate("fixture-graph-api"), filepath.ToSlash(apiCrate)),
		"Cargo.lock":  string(lock),
		"src/main.rs": graphOperationsRustServer,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(rustRoot, "target")
	}
	compile := exec.Command(cargo, "build", "--quiet")
	compile.Dir = crateDir
	compile.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("cargo build in %s: %v\n%s", crateDir, err, out)
	}
	// A copy of the test's own, which a build of another run into a shared
	// target directory cannot replace while the scenario runs it.
	built, err := os.ReadFile(filepath.Join(target, "debug", "recipes-server"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(rustRoot, "bin", "recipes-server")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, built, 0o755); err != nil {
		t.Fatal(err)
	}

	// The scenario, in the Go module of the same API, with the Rust
	// binary in the Go server's place.
	goRoot := t.TempDir()
	buildGraphFixture(t, goRoot, APILanguageGo)
	apiDir := writeGraphScenario(t, goRoot)
	runGraphScenario(t, apiDir, "the Rust server", []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "GRAPH_OPERATIONS_SERVER=" + binary},
		[]string{"test", "-count=1", "-v", "-run", "^TestGraphOperations$", "./..."},
	)
}

// graphOperationsRustManifest is the Rust server's Cargo.toml, given the
// API crate's name and its directory.
const graphOperationsRustManifest = `# The generated Rust server of fixture-graph-api, as the graph
# operations' scenario calls it (D138).
[package]
name = "recipes-server"
version = "0.0.0"
edition = "2021"
publish = false

[dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
tokio = { version = "1.52.2", features = ["macros", "net", "rt-multi-thread"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// graphOperationsRustServer is the Rust server's src/main.rs: the
// generated router of fixture-graph-api over a pool of the scenario's
// schema, with the scenario's test authenticator. getRecipe, the one
// operation no generated handler serves, the scenario does not call.
const graphOperationsRustServer = `//! The generated Rust server of fixture-graph-api, for the graph
//! operations' scenario.

use std::sync::Arc;

use api::runtime::postgres;
use api::runtime::stackconfig::Database;
use api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Handlers, Implementations, RecipeGetRecipeArgs, RecipeImplementation};
use async_trait::async_trait;

/// The scenario's authenticator: Authorization: Bearer
/// <subject>[:<permission>[,...]] is that subject, holding those
/// permissions; no header, no caller.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let token = request
            .headers
            .get(http::header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .filter(|token| !token.is_empty());
        Ok(token.map(|token| {
            let (subject, held) = token.split_once(':').unwrap_or((token, ""));
            let permissions: Vec<&str> = held.split(',').filter(|p| !p.is_empty()).collect();
            Principal::new(subject, permissions)
        }))
    }
}

/// The project's implementation of getRecipe, which the scenario does not
/// call.
struct Recipes;

#[async_trait]
impl RecipeImplementation for Recipes {
    async fn get_recipe(&self, _ctx: RequestContext, _args: RecipeGetRecipeArgs) -> Result<api::types::Recipe, ApiError> {
        Err(ApiError::not_implemented("the scenario reads no recipe"))
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let url = std::env::var("GRAPH_OPERATIONS_DATABASE_URL")?;
    let pool = postgres::connect(&Database::Url(url))?;
    let handlers = Handlers {
        recipe: Arc::new(Recipes),
        authenticator: Arc::new(Bearer),
    };
    let router = build_router(Implementations::new(handlers, pool));
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", 0)).await?;
    println!("listening on http://{}", listener.local_addr()?);
    axum::serve(listener, router).await?;
    Ok(())
}
`
