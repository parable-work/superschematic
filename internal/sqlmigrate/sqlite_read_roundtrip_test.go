package sqlmigrate

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// The migration runner, built once for the tests that read a SQLite file:
// it holds the SQLite driver, which stays out of this module (D27, Apply).
var (
	runnerOnce sync.Once
	runnerPath string
	runnerErr  error
)

// migrationRunner builds superschematic-migrate from runtime/migrate/go
// and returns its path. A -short run skips the test.
func migrationRunner(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: builds the migration runner")
	}
	runnerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "superschematic-migrate-")
		if err != nil {
			runnerErr = err
			return
		}
		runnerPath = filepath.Join(dir, "superschematic-migrate")
		build := exec.Command("go", "build", "-o", runnerPath, "./cmd/superschematic-migrate")
		build.Dir = filepath.Join("..", "..", "runtime", "migrate", "go")
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
		if out, err := build.CombinedOutput(); err != nil {
			runnerErr = &exec.Error{Name: "go build superschematic-migrate: " + string(out), Err: err}
		}
	})
	if runnerErr != nil {
		t.Fatal(runnerErr)
	}
	return runnerPath
}

// runRunner runs the migration runner and returns its stdout.
func runRunner(t *testing.T, runner string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(runner, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("superschematic-migrate %s: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

// applyPlan applies plan to the SQLite file at path, creating it.
func applyPlan(t *testing.T, runner, path string, plan *Plan) {
	t.Helper()
	doc := filepath.Join(t.TempDir(), "plan.json")
	writeJSON(t, doc, plan)
	runRunner(t, runner, "apply", "--plan", doc, "--database-url", path)
}

// inspectFile reads the SQLite file at path as the runner inspects it.
func inspectFile(t *testing.T, runner, path string) *Inspection {
	t.Helper()
	in, err := ReadInspection(runRunner(t, runner, "inspect", "--database-url", path))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// TestReadDatabaseRoundTrip: a file made from a schema's own create.sql
// reads back as a model whose plan to the schema's model has no step
// (D116), for every fixture whose SQLite model builds and for the shop. The
// file is made by the runner applying the plan from an empty database,
// which is create.sql (CreateSQL), so it holds the runner's state tables
// too, which are no part of the model. Roles are rows, which the read does
// not see: a schema that seeds roles plans their seeding, and nothing else.
func TestReadDatabaseRoundTrip(t *testing.T) {
	runner := migrationRunner(t)
	schemas := map[string]*ir.Schema{"shop-db": planCase{}.load(t, sqliteShop), "types-db": sqliteTypesSchema()}
	entries, err := os.ReadDir(sqlgenFixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "fixture-") {
			continue
		}
		schema, err := loader.LoadService(filepath.Join(sqlgenFixtures, entry.Name()))
		if err != nil || schema.Kind != ir.SchemaKindDB {
			continue
		}
		schemas[entry.Name()] = schema
	}

	read := map[string]bool{}
	for name, schema := range schemas {
		model, err := BuildModel(schema, sqlgen.Options{SchemaName: name}, SQLite)
		if err != nil {
			t.Logf("%s: no SQLite model: %v", name, firstLine(err.Error()))
			continue
		}
		if len(model.Tables) == 0 {
			continue
		}
		read[name] = true
		t.Run(name, func(t *testing.T) {
			create, err := Diff(nil, model, Options{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), name+".db")
			applyPlan(t, runner, path, create)

			holdsModel(t, runner, path, model)
		})
	}
	// The fixtures that hold what a read must get right: checks, defaults,
	// expression and partial indexes and a SET NULL key (ddl), the
	// @optimistic bump trigger, a queue's indexes and triggers, unique
	// constraints, lists and JSON, a search_text column, join tables, and
	// the triggers that refuse a write, a @stateMachine's guards (D118)
	// and a @fenced counter's triggers (D119).
	for _, name := range []string{
		"shop-db", "types-db", "fixture-ddl-db", "fixture-ledger-db", "fixture-optimistic-db", "fixture-queue-db",
		"fixture-user-model-db", "fixture-users-db", "fixture-list-defaults-db", "fixture-nested-arrays-db",
		"fixture-tracker-db", "fixture-vault-db", "fixture-state-machine-db", "fixture-fenced-db",
	} {
		if !read[name] {
			t.Errorf("%s was not read", name)
		}
	}
}

// buildByHand makes the SQLite file at path that the statements of
// <name>.sql in inspectTestdata build. The runner runs them as the one
// step of a plan of a service of its own, so the file holds the runner's
// state tables and no state of the service a plan from the file migrates.
func buildByHand(t *testing.T, runner, path, name string) {
	t.Helper()
	statements, err := os.ReadFile(filepath.Join(inspectTestdata, name+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	empty := &Model{Version: ModelVersion, Dialect: SQLite, Service: "built-by-hand"}
	plan := &Plan{Version: PlanVersion, Dialect: SQLite, Service: empty.Service, Steps: []*Step{{
		Index: 1, Phase: Expand, Op: "buildByHand", Subject: "table/" + name, Statements: []string{string(statements)}, Transactional: true,
	}}}
	if plan.To, err = empty.Hash(); err != nil {
		t.Fatal(err)
	}
	if plan.ToModel, err = empty.CanonicalJSON(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Seal(); err != nil {
		t.Fatal(err)
	}
	applyPlan(t, runner, path, plan)
}

// holdsModel fails unless the file at path reads, against want, as want
// but for its roles, which are rows, with no step between the two: the
// file holds want's schema.
func holdsModel(t *testing.T, runner, path string, want *Model) {
	t.Helper()
	db, err := ReadDatabase(inspectFile(t, runner, path), want, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffDatabase(db, want, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.Op != string(opSeedRole) {
			t.Errorf("step %d %s %s:\n%s", step.Index, step.Op, step.Subject, strings.Join(step.Statements, ";\n"))
		}
	}
	if len(plan.Steps) != len(want.Roles) {
		t.Errorf("the plan has %d steps, and the model seeds %d roles", len(plan.Steps), len(want.Roles))
	}
	rest := *want
	rest.Roles = nil
	wantJSON, err := rest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := db.Model.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("the file's model is not the one it should hold:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

// TestAdoptAndMigrateAFileBuiltByHand (D116): the file by-hand.sql builds
// is inspected as the committed inspection says; the runner adopts the
// model read from it and applies the plan from it, phase by phase; after
// expand the file holds the plan's expanded model, and at the end the
// schema's model, by the runner's state and by a read of the file.
func TestAdoptAndMigrateAFileBuiltByHand(t *testing.T) {
	runner := migrationRunner(t)
	schema := byHandModel(t)
	path := filepath.Join(t.TempDir(), "by-hand.db")
	buildByHand(t, runner, path, "by-hand")

	in := inspectFile(t, runner, path)
	if want := readInspection(t, "by-hand"); !reflect.DeepEqual(in, want) {
		t.Fatalf("the runner inspects the file as\n%s\nand the committed inspection is\n%s", describe(in), describe(want))
	}
	db, err := ReadDatabase(in, schema, nil, byHandDrops)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffDatabase(db, schema, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// The runner refuses the plan until the file's model is adopted.
	dir := t.TempDir()
	planFile, modelFile := filepath.Join(dir, "plan.json"), filepath.Join(dir, "model.json")
	writeJSON(t, planFile, plan)
	if out, err := exec.Command(runner, "apply", "--plan", planFile, "--database-url", path).CombinedOutput(); err == nil || !strings.Contains(string(out), "adopt") {
		t.Fatalf("applying the plan to a file with no adopted model: %v\n%s", err, out)
	}
	canonical, err := db.Model.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, modelFile, canonical)
	if out := runRunner(t, runner, "adopt", "--model", modelFile, "--database-url", path); !strings.Contains(string(out), "adopted model "+plan.From+" for service byhand-db") {
		t.Fatalf("adopt: %s", out)
	}

	runRunner(t, runner, "apply", "--plan", planFile, "--phase", "expand", "--database-url", path)
	var expanded Model
	if err := json.Unmarshal(plan.ExpandedModel, &expanded); err != nil {
		t.Fatal(err)
	}
	holdsModel(t, runner, path, &expanded)

	runRunner(t, runner, "apply", "--plan", planFile, "--phase", "contract", "--database-url", path)
	want, err := schema.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.TrimSpace(runRunner(t, runner, "status", "--service", "byhand-db", "--model", "--database-url", path)); !bytes.Equal(got, want) {
		t.Errorf("the runner records the model\n%s\nand the schema's is\n%s", got, want)
	}
	holdsModel(t, runner, path, schema)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
