package servergen_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

// The embedded fixture (D116): fixture-tracker-api built as a Rust API
// over fixture-tracker-db, whose DB service lists sqlite first, and
// tracker-embedded-stack, whose environment Desktop builds as one binary.
const (
	embeddedStackRoot  = "testdata/tables/embedded/services"
	embeddedStack      = "tracker-embedded-stack"
	embeddedGoldenRoot = "testdata/golden/tables/embedded"
)

var embeddedOrder = []string{rustTablesDB, rustTablesServer, embeddedStack}

// binaryTarget stands in for extensions/embedded, a module of its own the
// core's tests cannot link: a target named as its target is, whose server
// platform's artifact is the environment's binary and whose database
// platform keeps a SQLite file under the binary's data directory.
type binaryTarget struct{}

func (binaryTarget) Name() string { return "binary-target" }

func (binaryTarget) Register(r *publicregistry.Registry) error {
	none := func(publicregistry.PlatformContext) any { return nil }
	origin := func(publicregistry.PlatformContext) any { return "http://127.0.0.1:4170" }
	file := func(service string) string {
		return "databases/" + strings.ReplaceAll(service, "-", "_") + ".sqlite"
	}
	for _, spec := range []publicregistry.PlatformSpec{
		{
			Name: "embedded.process", Kind: ir.DeployableServer, Languages: []string{publicregistry.APILanguageRust},
			Artifact: ir.ArtifactBinary,
			// The binary serves plain HTTP on loopback, as the extension's
			// platform says.
			IdentityConfig: `{"cookie":{"secure":false}}`,
			NameOf:         func(ctx publicregistry.PlatformContext) any { return ctx.Deployable.Name },
			AddressOf:      origin, PublicAddressOf: origin,
			Lower: func(publicregistry.PlatformContext) (publicregistry.Lowered, error) {
				return publicregistry.Lowered{}, nil
			},
		},
		{
			Name: "embedded.sqlite", Kind: ir.DeployableDatabase, Dialects: []string{publicregistry.SQLDialectSQLite},
			NameOf:    func(ctx publicregistry.PlatformContext) any { return ctx.Deployable.Name },
			AddressOf: none,
			Lower: func(ctx publicregistry.PlatformContext) (publicregistry.Lowered, error) {
				var out publicregistry.Lowered
				for _, svc := range ctx.Deployable.Services {
					out.Resources = append(out.Resources, &ir.Resource{
						ID:         ctx.Deployable.Name + ".database." + svc.Name,
						Type:       "embedded:sqlite/database:Database",
						Properties: map[string]any{"service": svc.Name, "file": file(svc.Name)},
					})
				}
				return out, nil
			},
		},
	} {
		spec.Extension, spec.Provider = "binary-target", "embedded"
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	if err := r.RegisterConnector(publicregistry.ConnectorSpec{
		Name: "embedded.process-sqlite", Extension: "binary-target", Edge: ir.EdgeSQL,
		From: "embedded.process", To: "embedded.sqlite",
		Connect: func(ctx publicregistry.ConnectorContext) (publicregistry.Connected, error) {
			return publicregistry.Connected{Value: ir.DatabaseConnection{URL: "sqlite:" + file(ctx.Edge.Service.Name)}}, nil
		},
	}); err != nil {
		return err
	}
	return r.RegisterTarget(publicregistry.TargetSpec{
		Name:      "embedded",
		Extension: "binary-target",
		Platforms: map[ir.DeployableKind]string{
			ir.DeployableServer:   "embedded.process",
			ir.DeployableDatabase: "embedded.sqlite",
		},
		Values: []byte(`{"type":"object","properties":{"port":{"type":"integer"}},"additionalProperties":false}`),
		ResourceTypes: map[string]json.RawMessage{
			"embedded:sqlite/database:Database": json.RawMessage(`{"type":"object"}`),
		},
	})
}

// loadEmbeddedFixture loads the fixture with the core registry and the
// stand-in target.
func loadEmbeddedFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), binaryTarget{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range embeddedOrder {
		root := tsBehaviorsServicesRoot
		if name == embeddedStack {
			root = embeddedStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[rustTablesDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"sqlite", "postgres"}},
	}
	f.configs[rustTablesServer].Outputs = map[string]any{
		"types": map[string]any{"rust": enabled},
		"api":   map[string]any{"enabled": true, "language": "RUST"},
	}
	return f
}

// embeddedFiles are the files of the environment's crate, by their path
// under the output root.
func embeddedFiles(out string) map[string]string {
	dir := servergen.EmbeddedDir(out, embeddedStack, "Desktop")
	files := map[string]string{servergen.RustWorkspaceFile: filepath.Join(out, servergen.RustWorkspaceFile)}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.EmbeddedLibFile, servergen.EmbeddedCarriedSource} {
		files[filepath.Join(servergen.EmbeddedRoot, embeddedStack, "Desktop", filepath.FromSlash(file))] = filepath.Join(dir, filepath.FromSlash(file))
	}
	return files
}

// TestEmbeddedCrateGolden: the stack's build writes the crate of an
// environment that builds as one binary (D116), a library with a short
// main.rs over it and a member of the output root's workspace beside the
// server's crate: the library migrates the database's file with the plans
// the binary carries, opens it for the API and answers a request under
// the API's path through the API's handle; main.rs listens on loopback.
// Each file is laid out as rustfmt lays it out. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEmbeddedCrateGolden -update
func TestEmbeddedCrateGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEmbeddedFixture(t)
	paths := rustFakePaths(repoRoot)
	paths.MigrateRust = filepath.Join(repoRoot, "runtime", "migrate", "rust")
	f.build(t, repoRoot, paths, embeddedOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := embeddedFiles(out)
	compareGoldens(t, embeddedGoldenRoot, files)

	dir := servergen.EmbeddedDir(out, embeddedStack, "Desktop")
	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for file, wants := range map[string][]string{
		servergen.RustManifestFile: {
			`name = "tracker-embedded-stack-desktop"`,
			`superschematic-migrate = { path = "../../../../../runtime/migrate/rust" }`,
			`schemas-fixture-tracker-api-implementation = { path = "../../../impl/fixture-tracker-api" }`,
			`"sqlite"`,
		},
		servergen.EmbeddedLibFile: {
			`pub const FIXTURE_TRACKER_API_PATH: &str = "/api/fixture-tracker-api";`,
			`pub const FIXTURE_TRACKER_DB_FILE: &str = "databases/fixture_tracker_db.sqlite";`,
			`migrate_file(&data, "fixture-tracker-db", FIXTURE_TRACKER_DB_FILE)?;`,
			`"FIXTURE_TRACKER_DB_DATABASE_URL",`,
			`fixture_tracker_api_api::Api::new(`,
			`pub const DEFAULT_PORT: u16 = 4170;`,
		},
		servergen.RustMainFile: {`--every-interface`, `Ipv4Addr::LOCALHOST`},
		servergen.EmbeddedCarriedSource: {
			`pub(crate) const DATABASES: &[CarriedDatabase] = &[];`,
			`pub(crate) const SITE: &[SiteFile] = &[];`,
		},
	} {
		got := read(file)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %s", file, want)
			}
		}
	}
	if workspace, err := os.ReadFile(filepath.Join(out, servergen.RustWorkspaceFile)); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(workspace), `"embedded/tracker-embedded-stack/Desktop"`) {
		t.Errorf("the workspace does not hold the environment's crate:\n%s", workspace)
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		for _, file := range []string{servergen.EmbeddedLibFile, servergen.RustMainFile} {
			source := filepath.Join(dir, filepath.FromSlash(file))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
	}

	// What `stack build` wrote stays through a build of the stack, and an
	// environment that builds a binary no longer leaves no crate.
	carried := filepath.Join(dir, filepath.FromSlash(servergen.EmbeddedCarriedSource))
	if err := os.WriteFile(carried, []byte("// stack build wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, paths, embeddedStack)
	if got := read(servergen.EmbeddedCarriedSource); got != "// stack build wrote this\n" {
		t.Errorf("a build of the stack rewrote what the binary carries:\n%s", got)
	}
	if err := servergen.PruneEmbedded(out, embeddedStack, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, servergen.EmbeddedRoot)); !os.IsNotExist(err) {
		t.Errorf("the crates of the environments that build no binary stay: %v", err)
	}
}

// TestEmbeddedCarriedIsWrittenWhole: what `stack build` compiles into a
// binary is each database's plans in order and its adoption, and the
// site's files with their content types and digests, each a file under
// carried/ that src/carried.rs names (D116).
func TestEmbeddedCarriedIsWrittenWhole(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, servergen.EmbeddedCarriedDir, "plans", "gone-db", "0001.json")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := servergen.WriteEmbeddedCarried(dir, servergen.EmbeddedCarried{
		Stack: "Notes", Environment: "Desktop", Release: true,
		Databases: []servergen.EmbeddedCarriedDatabase{{
			Service:  "notes-db",
			Plans:    [][]byte{[]byte(`{"plan":1}`), []byte(`{"plan":2}`)},
			Adoption: &servergen.EmbeddedCarriedAdoption{Model: []byte(`{"model":0}`), Check: `SELECT "v" = 7 FROM pragma_user_version`},
		}},
		Site: []servergen.EmbeddedCarriedFile{
			{Path: "index.html", ContentType: servergen.SiteContentType("index.html"), Digest: "aa", Content: []byte("<html>")},
			{Path: "assets/app.js", ContentType: servergen.SiteContentType("assets/app.JS"), Digest: "bb", Content: []byte("x")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a plan an earlier build carried stays: %v", err)
	}
	for file, want := range map[string]string{
		"carried/plans/notes-db/0001.json": `{"plan":1}`,
		"carried/plans/notes-db/0002.json": `{"plan":2}`,
		"carried/adoption/notes-db.json":   `{"model":0}`,
		"carried/site/index.html":          "<html>",
		"carried/site/assets/app.js":       "x",
	} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	source, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(servergen.EmbeddedCarriedSource)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub(crate) const RELEASE: bool = true;",
		`include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/carried/plans/notes-db/0002.json")),`,
		`check: "SELECT \"v\" = 7 FROM pragma_user_version",`,
		`content_type: "text/javascript; charset=utf-8",`,
		`etag: "\"bb\"",`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("src/carried.rs lacks %s:\n%s", want, source)
		}
	}
	if strings.Index(string(source), "assets/app.js") > strings.Index(string(source), `path: "index.html"`) {
		t.Errorf("the site's files are not in path order:\n%s", source)
	}
}

// TestEmbeddedCrateBuilds: the crate builds in the output root's
// workspace with the implementation's scaffold and passes clippy, and
// main.rs's own tests hold its listener to loopback (D116).
func TestEmbeddedCrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo clippy in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	repoRoot := testpaths.TempDir(t)
	f := loadEmbeddedFixture(t)
	f.build(t, repoRoot, testpaths.Local(t), embeddedOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	for _, args := range [][]string{
		{"clippy", "--workspace", "--all-targets", "--quiet", "--", "-D", "warnings"},
		{"test", "--quiet", "-p", "schemas-tracker-embedded-stack-desktop-embedded"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = out
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", strings.Join(args, " "), out, err, output)
		}
	}
}

// embeddedWorkerGoldenRoot holds the crates of binaries with a behavior
// worker in them (D116), a directory per worker.
const embeddedWorkerGoldenRoot = "testdata/golden/embedded-worker"

// embeddedInput is the crate of a binary that holds the worker's API, its
// server and the worker, over the database's SQLite file, as the stack's
// build reads it from an environment that builds as one binary.
func (w rustBehaviorWorkerOnSQLite) embeddedInput(in servergen.RustInput, out string) servergen.EmbeddedInput {
	f := w.fixture
	file := "databases/" + strings.ReplaceAll(f.db, "-", "_") + ".sqlite"
	paths := in.Paths
	paths.MigrateRust = filepath.Join(filepath.Dir(paths.HTTPRuntimeRust), "..", "migrate", "rust")
	return servergen.EmbeddedInput{
		Stack:       in.Stack,
		Environment: "Desktop",
		Dir:         servergen.EmbeddedDir(out, in.Stack, "Desktop"),
		OutputRoot:  out,
		Port:        8080,
		Servers:     []servergen.EmbeddedServerInput{{Name: f.api, APIs: in.APIs}},
		Workers: []servergen.EmbeddedWorkerInput{{
			Name: w.server, API: f.api, Class: w.name, Concurrency: 2, GraceSeconds: 5,
		}},
		Databases:      []servergen.EmbeddedDatabaseInput{{Service: f.db, File: file}},
		Bound:          []servergen.EmbeddedVariable{{Name: f.databaseKey + "_URL", Value: "sqlite:" + file, Server: f.api}},
		Naming:         in.Naming,
		RepositoryRoot: in.RepositoryRoot,
		SDKDirs:        in.SDKDirs,
		Paths:          paths,
	}
}

// TestEmbeddedCrateWithABehaviorWorker: the crate of a binary whose
// environment runs a behavior worker (D116) builds the worker's queues by
// the function the API crate's behaviors module holds, over the file the
// binary opened, and gives its host the worker: its drain, for a host with
// no async runtime, and the loop over it, which main.rs runs beside the
// server and stops within the worker's grace. TicketTimers needs no
// implementation; ProblemWakeUps, whose table's Schedules emits events,
// takes the implementation's Workers, built from Deps of their own. Each
// file is laid out as rustfmt lays it out, and the crate passes clippy
// against the API crate, the database's Rust types and the checkout's
// crates, unless in -short mode or without cargo. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEmbeddedCrateWithABehaviorWorker -update
func TestEmbeddedCrateWithABehaviorWorker(t *testing.T) {
	for _, w := range rustBehaviorWorkersOnSQLite {
		t.Run(w.name, func(t *testing.T) {
			repoRoot := testpaths.TempDir(t)
			out := filepath.Join(repoRoot, "schemas", "dist")
			paths := rustFakePaths(repoRoot)
			paths.RulesAdapterRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-adapter")
			paths.RulesBehaviorsRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-behaviors")
			crate := w.fixture.onSQLite(t, w.fixture.apiCrate(t, out, paths))
			in := w.embeddedInput(w.fixture.workerInput(t, repoRoot, out, paths, crate, w.name, w.server), out)
			c, err := servergen.PlanEmbedded(in)
			if err != nil {
				t.Fatalf("PlanEmbedded: %v", err)
			}
			if err := servergen.WriteEmbedded(c, in.Dir); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{}
			for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.EmbeddedLibFile} {
				files[filepath.FromSlash(file)] = filepath.Join(in.Dir, filepath.FromSlash(file))
			}
			compareGoldens(t, filepath.Join(embeddedWorkerGoldenRoot, w.server), files)
			read := func(file string) string {
				data, err := os.ReadFile(filepath.Join(in.Dir, filepath.FromSlash(file)))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			lib, main := read(servergen.EmbeddedLibFile), read(servergen.RustMainFile)
			stem := strings.ReplaceAll(w.fixture.api, "-", "_")
			// The function of the API crate's behaviors module that builds
			// the worker's queues: its class in snake case.
			var snake strings.Builder
			for i, r := range w.name {
				if r >= 'A' && r <= 'Z' {
					if i > 0 {
						snake.WriteByte('_')
					}
					r += 'a' - 'A'
				}
				snake.WriteRune(r)
			}
			worker := snake.String() + "_worker"
			wants := []string{
				"pub fn workers(&self) -> &[Worker] {",
				"pub fn drain(",
				"pub async fn run(&self, shutdown: Shutdown) -> Result<(), Failure> {",
				"use superschematic_http_runtime::worker::{self, DrainOptions, Drained, SqliteWorker, Wake};",
				`Worker::new(`,
				`"` + w.server + `",`,
			}
			if w.emits {
				wants = append(wants,
					stem+"_impl::create_workers("+stem+"_worker_deps)",
					"Arc::clone(&"+stem+"_workers);",
					stem+"_api::behaviors::"+worker+"(",
				)
			} else {
				wants = append(wants, stem+"_api::behaviors::"+worker+"()?;")
			}
			for _, want := range wants {
				if !strings.Contains(lib, want) {
					t.Errorf("lib.rs does not contain %q:\n%s", want, lib)
				}
			}
			if !w.emits && strings.Contains(lib, "create_workers") {
				t.Errorf("lib.rs builds the implementation's workers, which a behavior worker of a table that emits no events has none of:\n%s", lib)
			}
			for _, want := range []string{"let shutdown = Shutdown::new();", "embedded.workers()[index].run(shutdown).await", "shutdown.cancel();"} {
				if !strings.Contains(main, want) {
					t.Errorf("main.rs does not contain %q:\n%s", want, main)
				}
			}
			if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
				for _, file := range []string{servergen.EmbeddedLibFile, servergen.RustMainFile} {
					source := filepath.Join(in.Dir, filepath.FromSlash(file))
					if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
						t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
					}
				}
			}

			if testing.Short() {
				return
			}
			cargo, err := exec.LookPath("cargo")
			if err != nil {
				t.Log("cargo not available; the crate is not compiled")
				return
			}
			w.checkEmbeddedCompiles(t, cargo)
		})
	}
}

// checkEmbeddedCompiles writes the binary's crate, the API crate it holds
// and the Rust types both read under a repository of their own with this
// checkout's runtimes, with the implementation's scaffold, and lints the
// crate with clippy, warnings denied.
func (w rustBehaviorWorkerOnSQLite) checkEmbeddedCompiles(t *testing.T, cargo string) {
	t.Helper()
	paths := testpaths.Local(t)
	names := naming.Default()
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	crate := w.fixture.onSQLite(t, w.fixture.apiCrate(t, out, paths))
	w.fixture.writeCrates(t, out, paths, crate)
	// The scaffold in the implementation's directory, which the plan
	// reads, and in its mirror, which the crate builds against.
	apiDir := apiDirOf(out, crate)
	for _, dir := range []string{filepath.Join(repoRoot, "services", crate.output.SchemaName, "rust"), servergen.MirrorDir(out, crate.output.SchemaName)} {
		if _, err := rustrestgen.WriteImplementationScaffold(crate.output, dir, apiDir, paths, names); err != nil {
			t.Fatalf("write the implementation's scaffold: %v", err)
		}
	}
	in := w.embeddedInput(w.fixture.workerInput(t, repoRoot, out, paths, crate, w.name, w.server), out)
	c, err := servergen.PlanEmbedded(in)
	if err != nil {
		t.Fatalf("PlanEmbedded: %v", err)
	}
	if err := servergen.WriteEmbedded(c, in.Dir); err != nil {
		t.Fatal(err)
	}
	// The crate is a member of the output root's workspace, which a build
	// writes; here it is a workspace of its own.
	appendManifest(t, in.Dir, "\n[workspace]\n\n"+testpaths.RustPatch(paths, names))
	cargoCommand(t, cargo, in.Dir, rustTargetDir(t), "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// The identity fixture as one binary (D116): users-db with the sqlite
// dialect listed first, users-api and users-admin in Rust, and
// users-embedded-stack, whose environment Desktop builds as one binary.
const (
	embeddedIdentityStack      = "users-embedded-stack"
	embeddedIdentityGoldenRoot = "testdata/golden/identity/embedded"
)

var embeddedIdentityOrder = []string{"users-db", "users-api", "users-admin", embeddedIdentityStack}

// loadEmbeddedIdentityFixture loads the fixture with the core registry and
// the stand-in target. users-api's job is left out: a binary runs none,
// and the embedded target refuses an environment with one.
func loadEmbeddedIdentityFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), binaryTarget{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range embeddedIdentityOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(identityRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	f.schemas["users-api"].Jobs = nil
	enabled := map[string]any{"enabled": true}
	f.configs["users-db"].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
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

// TestEmbeddedCrateOverTheUserModelGolden: the crate of a binary whose
// APIs authenticate with the identity runtime (D50, D116) builds one
// identity store on a connection of its own to the database's file, by
// the API crate's `identity::store_on_file`, and each API's identity
// service over it, with the config its identity config field holds, which
// the environment binds to a cookie without Secure and the binary carries.
// Its crate takes the runtime's identity-sqlite feature. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEmbeddedCrateOverTheUserModelGolden -update
func TestEmbeddedCrateOverTheUserModelGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEmbeddedIdentityFixture(t)
	paths := rustFakePaths(repoRoot)
	paths.MigrateRust = filepath.Join(repoRoot, "runtime", "migrate", "rust")
	f.build(t, repoRoot, paths, embeddedIdentityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.EmbeddedDir(out, embeddedIdentityStack, "Desktop")
	files := map[string]string{}
	for _, file := range []string{servergen.RustManifestFile, servergen.EmbeddedLibFile} {
		files[filepath.FromSlash(file)] = filepath.Join(dir, filepath.FromSlash(file))
	}
	compareGoldens(t, embeddedIdentityGoldenRoot, files)
	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	lib := read(servergen.EmbeddedLibFile)
	for _, want := range []string{
		"use superschematic_http_runtime::identity::Store;",
		"IdentityAuthenticator",
		"let users_db_identity_store: Arc<dyn Store> = Arc::new(\n        users_admin_api::identity::store_on_file(&users_db_pool)",
		"users_api_api::identity::service(\n        Arc::clone(&users_db_identity_store),",
		"users_admin_api::identity::service(\n        Arc::clone(&users_db_identity_store),",
		`("USERS_API_IDENTITY", "{\"cookie\":{\"secure\":false}}"),`,
		`("USERS_ADMIN_IDENTITY", "{\"cookie\":{\"secure\":false}}"),`,
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("lib.rs does not contain %q:\n%s", want, lib)
		}
	}
	if manifest := read(servergen.RustManifestFile); !strings.Contains(manifest, `"identity-sqlite"`) || !strings.Contains(manifest, `"identity"`) {
		t.Errorf("the crate does not take the runtime's identity-sqlite feature:\n%s", manifest)
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		source := filepath.Join(dir, filepath.FromSlash(servergen.EmbeddedLibFile))
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
		}
	}
}

// TestEmbeddedBinaryServesTheUserModel: the binary of the identity fixture
// builds in the output root's workspace, passes clippy, and keeps
// users-db's user model in the runtime's identity store on the file it
// migrates as it starts, from the plan it carries (D116): an end user
// registers through users-api under its path on the binary's origin and
// signs in, the session authenticates the project's route, which the
// scaffold answers, and me; users-admin, under its own path, over the same
// store, refuses the user a permission they do not hold; a second user's
// login taken is a conflict; a signed-out session is refused; and the user
// and her session outlive the binary, which a second start over the data
// directory signs in again.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestEmbeddedBinaryServesTheUserModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadEmbeddedIdentityFixture(t)
	f.build(t, repoRoot, paths, embeddedIdentityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.EmbeddedDir(out, embeddedIdentityStack, "Desktop")

	// What `stack build` carries: the plan from an empty database to the
	// schema's model on SQLite, which holds the user model's tables.
	outputs, err := registry.ParseOutputs(f.configs["users-db"].Outputs, f.reg)
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(f.schemas["users-db"], generator.SQLOptions(f.configs["users-db"], outputs, nil, naming.Default()), sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := sqlmigrate.Diff(nil, model, sqlmigrate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.WriteEmbeddedCarried(dir, servergen.EmbeddedCarried{
		Stack: embeddedIdentityStack, Environment: "Desktop",
		Databases: []servergen.EmbeddedCarriedDatabase{{Service: "users-db", Plans: [][]byte{document}}},
	}); err != nil {
		t.Fatal(err)
	}
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--workspace", "--all-targets", "--quiet", "--", "-D", "warnings")
	crate := strings.TrimSuffix(naming.Default().RustServerCrate(embeddedIdentityStack, "Desktop"), "-server") + "-embedded"
	cargoCommand(t, cargo, out, target, "build", "--quiet", "-p", crate)
	binary := filepath.Join(target, "debug", servergen.EmbeddedBinary(embeddedIdentityStack, "Desktop"))

	// The process's environment says each API's identity config over the
	// one the binary carries: a password hash a test can afford.
	identityConfig := `{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	env := []string{"USERS_API_IDENTITY=" + identityConfig, "USERS_ADMIN_IDENTITY=" + identityConfig}
	data := filepath.Join(testpaths.TempDir(t), "data")
	port := freePort(t)
	startBinary := func() (api, admin *started) {
		t.Helper()
		log := &syncBuilder{}
		cmd := exec.Command(binary, "--data", data)
		cmd.Env = append(os.Environ(), append(env, "PORT="+port)...)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		origin := "http://127.0.0.1:" + port
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if resp, err := http.Get(origin + "/"); err == nil {
				_ = resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not serve at %s:\n%s", binary, origin, log.String())
			}
		}
		// Each API is under its own path on the binary's one origin.
		return &started{cmd: cmd, base: origin + "/api/users-api", out: log}, &started{cmd: cmd, base: origin + "/api/users-admin", out: log}
	}

	api, admin := startBinary()
	api.expect(t, http.MethodGet, "/api/greeting", http.StatusUnauthorized)
	api.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	api.send(t, http.MethodPost, "/api/auth/register", `{"login": "ADA@example.com", "name": "Ada too", "password": "another password"}`, http.StatusConflict)
	token := api.login(t, "ada@example.com", "ada's password")
	api.authorized(t, token, http.MethodGet, "/api/greeting", http.StatusNotImplemented, "greet")
	api.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	admin.authorized(t, token, http.MethodGet, "/api/auth/admin/users", http.StatusForbidden, `"code":"forbidden"`)
	api.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	api.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	kept := api.login(t, "ada@example.com", "ada's password")
	api.stop(t)

	// A second start over the data directory serves what the first wrote.
	api, _ = startBinary()
	api.authorized(t, kept, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	api.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	api.stop(t)
}
