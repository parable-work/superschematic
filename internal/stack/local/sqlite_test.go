package local_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// A sqlite-first database on the local target (D64; docs/stack-model.md,
// section 18.13): a SQLite file per hosted DB schema under the
// environment's state directory, which the provisioner creates, the
// runner migrates from the SQLite plan, and the server opens at the
// sqlite: URL its edge derives.

// liteStack is a stack of one TypeScript API, notes, with a job, on
// notes-db, whose DB service lists sqlite first, and a local environment,
// Dev.
func liteStack() (*ir.Stack, []stack.Service) {
	db := ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB}
	api := ir.ServiceRef{Name: "notes", Kind: ir.SchemaKindAPI}
	s := &ir.Stack{
		Name:         "lite-stack",
		Deploy:       []ir.ServiceRef{api},
		Expose:       []ir.DeployableRef{{Service: &api}},
		Environments: []*ir.Environment{{Name: "Dev", Target: local.Target}},
	}
	return s, []stack.Service{
		{Name: "notes-db", Kind: ir.SchemaKindDB, Dialects: []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}},
		{
			Name: "notes", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{db},
			Operations:   []stack.Operation{{Name: "Notes.getNote"}},
			Jobs:         []ir.Job{{Name: "PruneNotes", Schedule: "0 3 * * *", Timeout: "1m"}},
		},
	}
}

// liteWorkerStack is the lite stack with a worker, IndexNotes, of notes,
// which claims notes-db's queue NoteWritten (D53, amended).
func liteWorkerStack() (*ir.Stack, []stack.Service) {
	s, services := liteStack()
	s.Name = "lite-worker-stack"
	for i := range services {
		switch services[i].Name {
		case "notes-db":
			services[i].Queues = []string{"NoteWritten"}
		case "notes":
			services[i].Jobs = nil
			services[i].Workers = []ir.Worker{{Name: "IndexNotes", Queue: "NoteWritten"}}
		}
	}
	return s, services
}

// TestSQLiteWorkerGolden: a TypeScript worker whose queue's database lists
// sqlite first claims on the SQLite file (D53, amended): its process's sql
// edge goes through local.worker-sqlite and derives the file's url, as the
// server's does.
func TestSQLiteWorkerGolden(t *testing.T) {
	s, services := liteWorkerStack()
	reg := assemble(t)
	checkGoldenEnvironment(t, reg, s, services, "Dev")
	env := resolve(t, reg, s, services, "Dev")
	var connectors []string
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeSQL {
			connectors = append(connectors, e.From+" by "+e.Connector)
		}
	}
	if got, want := strings.Join(connectors, "; "), "notes by local.process-sqlite; notes-index-notes by local.worker-sqlite"; got != want {
		t.Errorf("sql edges = %s, want %s", got, want)
	}
}

// TestSQLiteGolden: the lite stack's Dev places notes-db on local.sqlite,
// whose lowering is a SQLite file node under the state directory and no
// container, and whose sql edges, the server's and the job's, derive a
// reference to the file's url output; the program holds the file and a
// migration of it in sqlite.
func TestSQLiteGolden(t *testing.T) {
	s, services := liteStack()
	checkGoldenEnvironment(t, assemble(t), s, services, "Dev")
}

// liteFixture is the lite stack's Dev resolved in a checkout of its own,
// with its program rendered, notes-db's SQLite model written, an
// entrypoint directory per server and job, and a fake machine.
func liteFixture(t *testing.T) *fixture {
	t.Helper()
	s, services := liteStack()
	f := newFixtureWith(t, s, services, "Dev")
	model, err := vectorModel(t, filepath.Join("..", "sqlite-columns", "01-create.plan.json"), "notes-db").CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.req.Dir, local.ModelsDir, "notes-db.json"), model, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// stateFile is notes-db's SQLite file under the fixture's state directory.
func (f *fixture) stateFile(t *testing.T) string {
	t.Helper()
	dir, err := local.EnsureStateDir(f.schemasRoot, f.env.Stack, f.env.Environment)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "databases", "notes_db.sqlite")
}

// TestApplyASQLiteDatabase applies the lite stack's Dev on a fake machine:
// the infrastructure step creates notes-db's SQLite file, with no Docker,
// the migrate step reads its applied model and applies the SQLite plan at
// the file's sqlite: URL, and the server and the job get that URL as
// NOTES_DB_DATABASE_URL. Plan says what is missing before and nothing to
// create after; Destroy keeps the file and Purge removes it.
func TestApplyASQLiteDatabase(t *testing.T) {
	f := liteFixture(t)
	file := f.stateFile(t)
	url := "sqlite:" + filepath.ToSlash(file)
	f.runner.missing = []string{"docker"}
	f.runner.rules = []rule{
		{prefix: "/bin/superschematic-migrate status --service notes-db --model --database-url " + url, stderr: "superschematic-migrate: service notes-db has no applied model"},
		{prefix: "/bin/superschematic-migrate apply", out: "applied 1 step\n"},
	}
	changes, err := f.prov.Plan(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, c := range changes {
		planned = append(planned, c.Action+" "+c.Resource)
	}
	for _, want := range []string{"create notes-db.database.notes-db", "migrate notes-db.database.notes-db"} {
		if !slices.Contains(planned, want) {
			t.Errorf("the plan %v lacks %q", planned, want)
		}
	}
	if err := f.applyAll(t); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("notes-db's SQLite file: %v, %v; want it created, readable by its owner alone", info, err)
	}
	lines := f.runner.lines(f.req.Dir)
	applied := false
	for _, line := range lines {
		if strings.HasPrefix(line, "/bin/superschematic-migrate apply --plan ") && strings.HasSuffix(line, " --phase all --database-url "+url) {
			applied = true
		}
		if strings.HasPrefix(line, "/bin/docker") {
			t.Errorf("a SQLite database ran docker: %s", line)
		}
	}
	if !applied {
		t.Errorf("no apply at %s among:\n%s", url, strings.Join(lines, "\n"))
	}
	plan, err := os.ReadFile(filepath.Join(f.req.Dir, local.MigrationsDir, "notes-db.plan.json"))
	if err != nil || !bytes.Contains(plan, []byte(`"dialect":"sqlite"`)) {
		t.Errorf("the plan the provisioner wrote is not a SQLite plan: %v\n%s", err, plan)
	}
	var server *fakeProcess
	for _, p := range f.runner.started {
		if p.cmd.Dir == filepath.Join(f.req.OutputRoot, local.ModulePath("lite-stack", "notes")) {
			server = p
		}
	}
	if server == nil {
		t.Fatalf("notes was not started: %v", lines)
	}
	if got := envValue(server.cmd.Env, "NOTES_DB_DATABASE_URL"); got != url {
		t.Errorf("notes got NOTES_DB_DATABASE_URL=%q, want %q", got, url)
	}
	if !strings.Contains(f.out.String(), "create database notes-db: "+file+", a SQLite file") {
		t.Errorf("the output does not say it created the file:\n%s", f.out)
	}
	outputs, err := f.prov.Outputs(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if got := outputs["notes-db.database.notes-db"]["url"]; got != url {
		t.Errorf("notes-db's url output = %v, want %s", got, url)
	}

	// Applied again, the file is there and at the model: nothing to
	// create or migrate.
	model, err := os.ReadFile(filepath.Join(f.req.Dir, local.ModelsDir, "notes-db.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.runner.rules = []rule{{prefix: "/bin/superschematic-migrate status --service notes-db --model --database-url " + url, out: string(model)}}
	changes, err = f.prov.Plan(context.Background(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Action == "create" || c.Action == "migrate" {
			t.Errorf("a second plan would %s %s", c.Action, c.Resource)
		}
	}

	if err := f.prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("Destroy removed notes-db's file: %v", err)
	}
	if err := f.prov.Purge(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("Purge left notes-db's file: %v", err)
	}
	if !strings.Contains(f.out.String(), "removed database notes-db, the SQLite file "+file) {
		t.Errorf("the output does not say Purge removed the file:\n%s", f.out)
	}
}

// TestASQLiteDatabaseNeedsAStateDirectory: the file lives under the
// state directory, so an apply with no state backend is refused, naming
// what it needed.
func TestASQLiteDatabaseNeedsAStateDirectory(t *testing.T) {
	f := liteFixture(t)
	f.req.Backend = registry.StateBackend{}
	var infra *ir.DeployStep
	for _, step := range f.env.DeployOrder {
		if step.Step == ir.StepInfrastructure {
			infra = step
		}
	}
	err := f.prov.Apply(context.Background(), f.req, *infra)
	if err == nil || !strings.Contains(err.Error(), "environment Dev has SQLite databases to keep, but the request names no state backend") {
		t.Fatalf("Apply with no state backend = %v", err)
	}
}
