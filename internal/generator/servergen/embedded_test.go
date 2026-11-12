package servergen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
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
			Artifact:  ir.ArtifactBinary,
			NameOf:    func(ctx publicregistry.PlatformContext) any { return ctx.Deployable.Name },
			AddressOf: origin, PublicAddressOf: origin,
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
