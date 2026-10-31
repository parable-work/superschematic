package servergen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// rustBehaviorsGoldenRoot holds the Rust server of fixture-workflow-api,
// whose @behaviorOperations sets serve fixture-workflow-db's Order and
// Shipment (D103).
const rustBehaviorsGoldenRoot = "testdata/golden/behaviors/rust"

// rustBehaviorAPI is fixture-workflow-api's Rust crate as a build of the
// output root out makes it, its Deps holding a pool of fixture-workflow-db
// and the layout and the checkout's crates its behavior operations run
// with, its EnvConfig holding the database's field, and its apigen output.
func rustBehaviorAPI(t *testing.T, out string, paths naming.LocalPaths) (*rustrestgen.APIOutput, *apigen.APIOutput, *envgen.ConfigOutput) {
	t.Helper()
	const fixtures = "../../loader/tsreader/testdata/services"
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixtures, "fixture-workflow-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := loader.LoadService(filepath.Join(fixtures, "fixture-workflow-api"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	routes, err := apigen.Generate(schema, apigen.Options{
		SchemaName:     schema.Name,
		UpstreamSchema: db.Name,
		UpstreamIR:     db,
		Provider:       sessionauth.Provider{},
		Clock:          clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiDir := generator.APIDir(out, schema.Name)
	output, err := rustrestgen.Generate(schema, routes, rustrestgen.Options{
		SchemaName:   schema.Name,
		Dependencies: map[string]*ir.Schema{db.Name: db},
		TypesCrate:   naming.Default().RustTypesCrate(schema.Name),
		TypesDir:     generator.TypesDir(out, "rust", schema.Name),
		OutputDir:    apiDir,
		Clock:        clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.Of(db, reg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	behaviors := &rustrestgen.DepsBehaviors{Layout: string(encoded)}
	if behaviors.AdapterPath, err = naming.RelPath(apiDir, paths.RulesAdapterRust); err != nil {
		t.Fatal(err)
	}
	if behaviors.BehaviorsPath, err = naming.RelPath(apiDir, paths.RulesBehaviorsRust); err != nil {
		t.Fatal(err)
	}
	if err := output.SetDeps(rustrestgen.DepsInfo{Database: db.Name, Behaviors: behaviors}); err != nil {
		t.Fatal(err)
	}
	output.HasEnvConfig = true
	config := &envgen.ConfigOutput{
		SchemaName: schema.Name,
		EnvConfig:  true,
		Derived:    []envgen.DerivedField{{Key: "FIXTURE_WORKFLOW_DB_DATABASE", GoName: "FixtureWorkflowDbDatabase", Kind: ir.EdgeSQL, Service: db.Name}},
	}
	return output, routes, config
}

// TestRustServerOfBehaviorOperations: the Rust server of an API with
// behavior operations (D103) builds its Implementations with the pool of
// the API's database, on which their handlers run them, and its image's
// context takes in the Cargo workspace of superschematic-rules-adapter and
// superschematic-rules-behaviors, which the API crate depends on by path,
// and the crates beside it the workspace names. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustServerOfBehaviorOperations -update
func TestRustServerOfBehaviorOperations(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustFakePaths(repoRoot)
	paths.RulesAdapterRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-adapter")
	paths.RulesBehaviorsRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-behaviors")
	// The checkout's workspace, whose manifest the crates inherit their
	// package fields from, and a crate beside it the workspace names.
	for _, dir := range []string{filepath.Join("runtime", "engine", "rust"), filepath.Join("runtime", "engine-workqueue", "rust")} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, dir, "Cargo.toml"), []byte("[workspace]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output, routes, config := rustBehaviorAPI(t, out, paths)
	impl, err := servergen.ReadRustImplementation(naming.Default(), output.SchemaName, filepath.Join(repoRoot, "services", output.SchemaName, "rust"))
	if err != nil {
		t.Fatal(err)
	}
	dir := servergen.ServerDir(out, "workflow-stack", "workflow")
	in := servergen.RustInput{
		Stack:      "workflow-stack",
		Server:     "workflow",
		Dir:        dir,
		OutputRoot: out,
		APIs: []servergen.RustAPIInput{{
			Output:         output,
			Routes:         routes,
			Config:         config,
			Dir:            generator.APIDir(out, output.SchemaName),
			Implementation: impl,
		}},
		Naming:         naming.Default(),
		RepositoryRoot: repoRoot,
		Paths:          paths,
	}
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust: %v", err)
	}
	if s.Docker == nil {
		t.Fatalf("no Dockerfile: %s", s.NoDocker)
	}
	if err := servergen.WriteRust(s, dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[filepath.FromSlash(file)] = filepath.Join(dir, filepath.FromSlash(file))
	}
	compareGoldens(t, rustBehaviorsGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(servergen.RustMainFile)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "fixture_workflow_db_pool.clone()"; !strings.Contains(string(main), "Implementations::new(") || !strings.Contains(string(main), want) {
		t.Errorf("main.rs does not build the Implementations with %s:\n%s", want, main)
	}
	ignore, err := os.ReadFile(filepath.Join(dir, servergen.DockerIgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"!runtime/engine/rust\n", "!runtime/engine-workqueue/rust\n"} {
		if !strings.Contains(string(ignore), want) {
			t.Errorf("the image's context does not take in %q:\n%s", strings.TrimSpace(want), ignore)
		}
	}

	// A server's API whose behavior operations run on a database it opens
	// no pool on is refused.
	output.Deps.Database = ""
	if _, err := servergen.PlanRust(in); err == nil || !strings.Contains(err.Error(), "whose behavior operations run on fixture-workflow-db (D103), and opens no pool on it") {
		t.Errorf("PlanRust without the database's pool: %v", err)
	}
	output.Deps.Database = "fixture-workflow-db"

	// Without the workspace beside the adapter, the image cannot build it.
	if err := os.RemoveAll(filepath.Join(repoRoot, "runtime", "engine", "rust")); err != nil {
		t.Fatal(err)
	}
	s, err = servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust without the adapter's workspace: %v", err)
	}
	if s.Docker != nil || !strings.Contains(s.NoDocker, "has no Cargo workspace beside it") {
		t.Errorf("PlanRust without the adapter's workspace: docker %+v, why %q; want no image", s.Docker, s.NoDocker)
	}
}
