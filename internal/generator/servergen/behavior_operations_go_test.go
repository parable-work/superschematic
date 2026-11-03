package servergen_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/release"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// goBehaviorsGoldenRoot holds the Go server of fixture-workflow-api, whose
// @behaviorOperations sets serve fixture-workflow-db's Order and Shipment
// (D103).
const goBehaviorsGoldenRoot = "testdata/golden/behaviors/go"

// goBehaviorAPI is fixture-workflow-api's Go server output as the dispatch
// layer hands it to the server's plan, made public, so its Config takes
// the implementation's auth middleware, which authenticates the caller its
// behavior operation's route requires: one behavior operation among its
// endpoints, its Deps holding the ORM of fixture-workflow-db, whose tables'
// fields are behaviors, and its EnvConfig the database's field.
func goBehaviorAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:     "fixture-workflow-api",
		ModulePath:     n.GoAPIModule("fixture-workflow-api"),
		TypesModule:    n.GoTypesModule("fixture-workflow-api"),
		Naming:         n,
		IsPublic:       true,
		UpstreamSchema: "fixture-workflow-db",
		Endpoints: []apigen.EndpointInfo{{
			Name:         "statusTransition",
			Path:         "/api/order/{id}/status/transition",
			RoutePath:    "/order/{id}/status/transition",
			Method:       "POST",
			Namespace:    "order",
			RequiresAuth: true,
			Operation: &ir.FieldDef{Name: "statusTransition", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-workflow-db", Type: "Order", Key: "id", Use: "status", Behavior: "Workflow", Operation: "transition", Writes: true,
			}},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-workflow-db", ORMModule: n.GoORMModule("fixture-workflow-db"), Rules: true},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-workflow-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_WORKFLOW_DB_DATABASE", GoName: "FixtureWorkflowDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-workflow-db"}},
		},
	}
}

// goBehaviorInput is the plan's input of a server of goBehaviorAPI in the
// repository repoRoot, whose modules, the rules module among them, lie in
// its checkout.
func goBehaviorInput(repoRoot string, paths naming.LocalPaths) servergen.Input {
	n := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	o := goBehaviorAPI()
	return servergen.Input{
		Stack:  "workflow-stack",
		Server: "workflow",
		Dir:    servergen.ServerDir(out, "workflow-stack", "workflow"),
		APIs: []servergen.APIInput{{
			Output: o,
			Implementation: servergen.Implementation{
				Dir:       filepath.Join(repoRoot, "services", "fixture-workflow-api", "go"),
				Import:    "example.com/services/fixture-workflow-api/go",
				Module:    "example.com/services/fixture-workflow-api/go",
				ModuleDir: filepath.Join(repoRoot, "services", "fixture-workflow-api", "go"),
			},
		}},
		Modules: []servergen.Module{
			{Path: o.ModulePath, Dir: filepath.Join(out, "api", "fixture-workflow-api"), Direct: true},
			{Path: o.TypesModule, Dir: filepath.Join(out, "types", "go", "fixture-workflow-api")},
			{Path: n.GoTypesModule("fixture-workflow-db"), Dir: filepath.Join(out, "types", "go", "fixture-workflow-db")},
			{Path: o.Deps.ORMModule, Dir: filepath.Join(out, "orm", "fixture-workflow-db"), Direct: true},
			{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
			{Path: n.ScalarGoModule, Version: "v1.0.0", Dir: paths.ScalarGo},
			{Path: n.RulesGoModule, Dir: paths.RulesGo},
			{Path: "example.com/services/fixture-workflow-api/go", Dir: filepath.Join(repoRoot, "services", "fixture-workflow-api", "go"), Direct: true},
		},
		Naming:         n,
		RepositoryRoot: repoRoot,
		ScalarGo:       paths.ScalarGo,
		RulesGo:        paths.RulesGo,
	}
}

// TestGoServerOfBehaviorOperations: the Go server of an API with behavior
// operations (D103) passes its database's ORM to RegisterRoutes, which
// runs them through it; its go.mod requires and replaces the rules module,
// which the ORM requires; and a checkout's image builds the behaviors'
// core's archive in a rules stage, from the engine's Cargo workspace
// beside the rules module, and puts it where the module links it.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoServerOfBehaviorOperations -update
func TestGoServerOfBehaviorOperations(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	in := goBehaviorInput(repoRoot, paths)
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if s.Docker == nil {
		t.Fatalf("no Dockerfile: %s", s.NoDocker)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range []string{servergen.MainFile, servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[file] = filepath.Join(in.Dir, file)
	}
	compareGoldens(t, goBehaviorsGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`RegisterRoutes\(fixtureWorkflowApiRouter, fixtureworkflowapi\.Config\{\n\s+DB: +fixtureWorkflowDb,`).Match(main) {
		t.Errorf("main.go passes RegisterRoutes no DB:\n%s", main)
	}
	docker, err := os.ReadFile(filepath.Join(in.Dir, servergen.DockerFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM rust:${RUST_VERSION}-trixie AS rules",
		"cargo build --locked --release --manifest-path runtime/engine/rust/rules-wasm/Cargo.toml",
		"cp runtime/engine/rust/target/release/libsuperschematic_rules_wasm.a /libsuperschematic_rules.a",
		"COPY --from=rules /libsuperschematic_rules.a /archives/",
		`RUN lib="runtime/rules/go/lib/linux_$(go env GOARCH)"`,
	} {
		if !strings.Contains(string(docker), want) {
			t.Errorf("the Dockerfile holds no %q", want)
		}
	}
}

// TestGoServerOfBehaviorOperationsNeedsACaller: the Go server of an API
// neither public nor over a user model authenticates no caller, so one
// whose behavior operation's route requires a caller is refused, naming
// the route, the table, the field and the behavior (D103); one whose
// behavior operations' set is @publicRoute, or only services call, is
// planned.
func TestGoServerOfBehaviorOperationsNeedsACaller(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	in := goBehaviorInput(repoRoot, paths)
	o := in.APIs[0].Output
	o.IsPublic, o.UpstreamSchema = false, ""
	_, err := servergen.Plan(in)
	const want = "stack workflow-stack: server workflow serves fixture-workflow-api, whose route POST /api/order/{id}/status/transition runs Order's status, a Workflow (D103), for the caller its @behaviorOperations set requires, and the Go server authenticates no caller"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Plan = %v, want an error containing %q", err, want)
	}

	o.Endpoints[0].RequiresAuth, o.Endpoints[0].PublicRoute = false, true
	if _, err := servergen.Plan(in); err != nil {
		t.Errorf("Plan of a @publicRoute set's behavior operation = %v", err)
	}
	o.Endpoints[0].PublicRoute, o.Endpoints[0].ServiceCallers = false, &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	o.HasServiceCallers = true
	if _, err := servergen.Plan(in); err != nil {
		t.Errorf("Plan of a behavior operation only services call = %v", err)
	}
}

// TestGoServerOfBehaviorOperationsNeedsTheRulesCheckout: a checkout's image
// of a server whose database's tables' fields are behaviors builds the
// core's archive from the engine's workspace beside [paths] rules_go, so
// without it no Dockerfile is planned, and the plan says which key to set;
// a release's image takes the archive from the release's tarball, which
// holds it, and builds no rules stage.
func TestGoServerOfBehaviorOperationsNeedsTheRulesCheckout(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
	}
	in := goBehaviorInput(repoRoot, paths)
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if s.Docker != nil || !strings.Contains(s.NoDocker, "[paths] rules_go") {
		t.Fatalf("planned a Dockerfile %v, or says %q: want none, naming [paths] rules_go", s.Docker, s.NoDocker)
	}

	in.ScalarGo = ""
	in.Release = release.Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": strings.Repeat("0", 64), "linux-arm64": strings.Repeat("1", 64)}}
	s, err = servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if s.Docker == nil {
		t.Fatalf("no Dockerfile from a release: %s", s.NoDocker)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	docker, err := os.ReadFile(filepath.Join(in.Dir, servergen.DockerFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(docker), "AS rules") || !strings.Contains(string(docker), "It holds the behaviors' core's archive") || !strings.Contains(string(docker), "ENV CGO_LDFLAGS=-L/archives/lib") {
		t.Errorf("a release's Dockerfile builds a rules stage, or says nothing of the core's archive in the tarball:\n%s", docker)
	}
}
