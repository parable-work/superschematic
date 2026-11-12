package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTableOperationsRustServerServesItsTables holds the generated Rust
// server's table operations (D106, D116) to the scenario the Go and the
// TypeScript servers' are held to, case for case: it builds
// fixture-tracker-db and fixture-tracker-api as a Rust server, with a
// binary over the generated router that implements the one operation the
// project writes and authenticates as the scenario's test authenticator
// does, and runs tableOperationsTest in the Go module of the same API with
// that binary in the Go server's place, on the Postgres
// behaviorServerDatabaseEnv names. Every route's rows are read and written
// through the Rust repositories of fixture-tracker-db.
//
// The test skips without the database, without cargo and in -short mode.
func TestTableOperationsRustServerServesItsTables(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's table operations in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's table operations against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's table operations")
	}
	paths := testpaths.Local(t)
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	rustTypes := func(cfg *schemaconfig.SchemaConfig) {
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
	}
	build := func(outputRoot, language string) {
		for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
			if err != nil {
				t.Fatalf("load %s: %v", service, err)
			}
			if language == APILanguageRust {
				rustTypes(cfg)
				if service == "fixture-tracker-api" {
					cfg.Outputs["api"] = map[string]any{"enabled": true, "language": language}
				}
			}
			if _, err := Run(schema, cfg, Options{
				OutputRoot:     outputRoot,
				Paths:          paths,
				Naming:         naming.Default(),
				Clock:          clock,
				LoadDependency: loadDependency,
			}); err != nil {
				t.Fatalf("run %s as %s: %v", service, language, err)
			}
		}
	}

	// The Rust server, and a binary that serves its router.
	rustRoot := testpaths.TempDir(t)
	build(rustRoot, APILanguageRust)
	crateDir := filepath.Join(rustRoot, "server", "tracker")
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	names := naming.Default()
	for name, content := range map[string]string{
		"Cargo.toml":  fmt.Sprintf(tableOperationsRustManifest, names.RustAPICrate("fixture-tracker-api"), filepath.ToSlash(APIDir(rustRoot, "fixture-tracker-api"))),
		"Cargo.lock":  string(lock),
		"src/main.rs": tableOperationsRustServer,
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
	built, err := os.ReadFile(filepath.Join(target, "debug", "tracker-server"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(rustRoot, "bin", "tracker-server")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, built, 0o755); err != nil {
		t.Fatal(err)
	}

	// The scenario, in the Go module of the same API, with the Rust
	// binary in the Go server's place.
	goRoot := t.TempDir()
	build(goRoot, APILanguageGo)
	apiDir := APIDir(goRoot, "fixture-tracker-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(goRoot, "fixture-tracker-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "table_operations_test.go"), []byte(tableOperationsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "TABLE_OPERATIONS_SERVER=" + binary}
	var out []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"test", "-count=1", "-v", "-run", "^TestTableOperations$", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module, against the Rust server: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("the scenario against the Rust server:\n%s", out)
	for _, name := range tableOperationCases {
		if want := "--- PASS: TestTableOperations/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the Rust server did not pass %q", name)
		}
	}
}

// tableOperationsRustManifest is the Rust server's Cargo.toml, given the
// API crate's name and its directory.
const tableOperationsRustManifest = `# The generated Rust server of fixture-tracker-api, as the table
# operations' scenario calls it (D106, D116).
[package]
name = "tracker-server"
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

// tableOperationsRustServer is the Rust server's src/main.rs: the
// generated router of fixture-tracker-api over a pool of the scenario's
// schema, with the scenario's test authenticator and the project's
// implementation of summarizeTeam, the one operation no generated handler
// serves, counted through the Rust repositories.
const tableOperationsRustServer = `//! The generated Rust server of fixture-tracker-api, for the table
//! operations' scenario.

use std::sync::Arc;

use api::database_types::orm::postgres::Repositories;
use api::database_types::orm::{Equals, FindOptions, IssueFilter, Text};
use api::runtime::postgres::{self, Pool};
use api::runtime::stackconfig::Database;
use api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Handlers, Implementations, TeamsImplementation, TeamsSummarizeTeamArgs};
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

/// The project's implementation of summarizeTeam: the team's open issues,
/// counted through the repositories.
struct Teams {
    pool: Pool,
}

#[async_trait]
impl TeamsImplementation for Teams {
    async fn summarize_team(
        &self,
        _ctx: RequestContext,
        args: TeamsSummarizeTeamArgs,
    ) -> Result<api::types::TeamSummary, ApiError> {
        let client = self
            .pool
            .get()
            .await
            .map_err(|err| ApiError::service_unavailable(err.to_string()))?;
        let found = Repositories::new(&client)?
            .issue()?
            .find_many(
                Some(&IssueFilter {
                    team_id: Some(Equals::eq(args.id.clone())),
                    status: Some(Text::eq(api::database_types::IssueStatus::Open)),
                    ..Default::default()
                }),
                &FindOptions::default(),
            )
            .await?;
        Ok(api::types::TeamSummary {
            team_id: args.id,
            open_issues: found.total.unwrap_or(0) as f64,
        })
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let url = std::env::var("TABLE_OPERATIONS_DATABASE_URL")?;
    let pool = postgres::connect(&Database::Url(url))?;
    let handlers = Handlers {
        teams: Arc::new(Teams { pool: pool.clone() }),
        authenticator: Arc::new(Bearer),
    };
    let router = build_router(Implementations::new(handlers, pool));
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", 0)).await?;
    println!("listening on http://{}", listener.local_addr()?);
    axum::serve(listener, router).await?;
    Ok(())
}
`
