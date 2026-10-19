package servergen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

const (
	// engineRoot holds the engine fixture (D58): desk-db, an engine
	// database whose Account and Ticket compose behaviors, beside the user
	// model's tables; desk-api, a public Go API over it with the session
	// routes and a route of its own; and desk-stack, which serves desk-api
	// from its default server on the local target, and runs desk-db's
	// engine runner beside it.
	engineRoot = "testdata/engine"

	// engineStack is the fixture's stack, engineServer its server and
	// engineRunner the engine's runner of desk-db.
	engineStack  = "desk-stack"
	engineServer = "desk-api"
	engineRunner = "desk-db-engine-runner"

	// engineDatabaseEnv names the Postgres the engine fixture's server and
	// runner also run on, as the identity fixture's entrypoint test reads
	// it.
	engineDatabaseEnv = identityDatabaseEnv
)

// engineOrder is the order a build-all builds the engine fixture in.
var engineOrder = []string{"desk-db", "desk-api", engineStack}

// loadEngineFixture loads the engine fixture with the core registry, whose
// local target the stack's environment runs on.
func loadEngineFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range engineOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestTheEngineDatabaseOfAnAPI: an API whose database is an engine
// database lists, in its permission catalog, what the engine's permission
// policy asks of each engine type, a version 2 catalog beside the
// operations' permissions; and the stack resolves desk-db's runner in
// desk-api's Go, on the local runner platform, with its database's
// connection as its one binding (D58).
func TestTheEngineDatabaseOfAnAPI(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), engineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")

	data, err := os.ReadFile(filepath.Join(generator.APIDir(out, "desk-api"), permcatalog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permcatalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != permcatalog.EngineVersion || catalog.AuthDB != "desk-db" {
		t.Errorf("catalog = version %d over %s", catalog.Version, catalog.AuthDB)
	}
	var engine []string
	for _, p := range catalog.Permissions {
		if p.Engine != nil {
			if p.Engine.Database != "desk-db" || p.Engine.Namespace != "desk-db" || p.Name != p.Engine.Schema+"."+p.Engine.Action {
				t.Errorf("engine permission %+v", p)
			}
			engine = append(engine, p.Name)
		}
	}
	want := []string{"Account.define", "Account.publish", "Account.read", "Account.write", "Ticket.define", "Ticket.publish", "Ticket.read", "Ticket.write"}
	if !slices.Equal(engine, want) {
		t.Errorf("engine permissions = %v, want %v", engine, want)
	}

	data, err = os.ReadFile(filepath.Join(out, "stack", engineStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	runner := env.Deployable(engineRunner)
	if runner == nil || runner.Kind != ir.DeployableRunner || runner.Language != "GO" || runner.Platform != "local.runner" ||
		runner.Runner == nil || runner.Runner.Database != "desk-db" || runner.Runner.API != "desk-api" || runner.Runner.Instances != 1 {
		t.Fatalf("the runner is %+v", runner)
	}
	var fields []string
	for _, b := range runner.Bindings {
		fields = append(fields, b.Field+"="+string(b.Source))
	}
	if got := strings.Join(fields, " "); got != "DESK_DB_DATABASE=derived" {
		t.Errorf("the runner binds %s, want its database alone", got)
	}
}
