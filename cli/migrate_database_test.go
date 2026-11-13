package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack/local"
)

// The schema that adopts a file built by hand, and the statements that
// build the file, which sqlmigrate's tests and the migration runner's read
// too (D116).
const (
	byHandService    = "../internal/sqlmigrate/testdata/services/byhand"
	byHandStatements = "../runtime/migrate/testdata/inspect/by-hand.sql"
)

// TestMigratePlanFromDatabaseFlagErrors: --from-database is one of three
// flags that name the previous version, reads a SQLite file alone, and
// owns --drop. Each refusal names the flags, before anything loads.
func TestMigratePlanFromDatabaseFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--from-database", "app.db"}, "--from-database reads a SQLite file; give --dialect sqlite with it"},
		{[]string{"--from-database", "app.db", "--dialect", "postgres"}, "--from-database reads a SQLite file, and --dialect is postgres; give --dialect sqlite with it"},
		{[]string{"--dialect", "sqlite", "--from-database", "app.db", "--from", "a"}, "--from and --from-database both name the previous version; give one"},
		{[]string{"--dialect", "sqlite", "--from-database", "app.db", "--from-ref", "main"}, "--from-ref and --from-database both name the previous version; give one"},
		{[]string{"--from-database", "app.db", "--from-ref", "main", "--from", "a"}, "--from, --from-ref and --from-database all name the previous version; give one"},
		{[]string{"--drop", "view:order_totals"}, "--drop names an object of the file --from-database reads; give --from-database with it"},
		{[]string{"--dialect", "sqlite", "--from", "model.json", "--drop", "view:order_totals"}, "--drop names an object of the file --from-database reads"},
		{[]string{"--dialect", "sqlite", "--from-database", "app.db", "--drop", "table:order"}, `--drop: drop "table:order": want <kind>:<name>, the kind one of trigger, view, index and virtual-table (trigger:audit_order)`},
		{[]string{"--dialect", "sqlite", "--from-database", "app.db", "--drop", "order_totals"}, `--drop: drop "order_totals": want <kind>:<name>`},
		{[]string{"--print-model", "--dialect", "sqlite", "--from-database", "app.db", "--format", "json", "--out", "p.json"}, "--print-model prints the file's model and plans nothing; drop --out, --format"},
		{[]string{"--print-model", "--drop", "view:order_totals"}, "--print-model prints the new version's model and plans nothing; drop --drop"},
	} {
		_, _, err := runMigratePlanCmd(t, append([]string{"/nonexistent/shop-db"}, tc.args...)...)
		require.Error(t, err, "%v", tc.args)
		assert.Contains(t, err.Error(), tc.want, "%v", tc.args)
	}
}

// TestMigratePlanFromDatabaseNeedsTheFileAndTheRunner: a file that does
// not exist is refused and not created, a directory is no file, and the
// plan says how to get the migration runner when it finds none.
func TestMigratePlanFromDatabaseNeedsTheFileAndTheRunner(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": sqliteDB(orderIDField, orderTotalField),
	})
	service := filepath.Join(servicesRoot, "shop-db")
	missing := filepath.Join(t.TempDir(), "missing.db")
	_, _, err := runMigratePlanCmd(t, service, "--dialect", "sqlite", "--from-database", missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from-database: stat "+missing)
	_, statErr := os.Stat(missing)
	assert.True(t, os.IsNotExist(statErr), "the plan created the file it was to read")

	_, _, err = runMigratePlanCmd(t, service, "--dialect", "sqlite", "--from-database", servicesRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a directory, not a SQLite file")

	file := filepath.Join(t.TempDir(), "app.db")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	t.Setenv(local.MigrateEnv, "")
	t.Setenv("PATH", t.TempDir())
	_, _, err = runMigratePlanCmd(t, service, "--dialect", "sqlite", "--from-database", file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "superschematic-migrate is not on PATH, and SUPERSCHEMATIC_MIGRATE names no runner; it reads the file --from-database names")
}

// migrationRunner builds superschematic-migrate, which reads a SQLite
// file for --from-database and applies the plan, and points
// $SUPERSCHEMATIC_MIGRATE at it.
func migrationRunner(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: builds the migration runner")
	}
	runner := filepath.Join(t.TempDir(), local.MigrateBinary)
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join("..", "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv(local.MigrateEnv, runner)
	return runner
}

// runMigrationRunner runs the migration runner and returns its output.
func runMigrationRunner(t *testing.T, runner string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command(runner, args...).CombinedOutput()
	return string(out), err
}

// buildFileByHand makes the SQLite file at path that statements build. The
// runner runs them as the one step of a plan of a service of its own, so
// the file holds the runner's state tables and no state of the service a
// plan from the file migrates.
func buildFileByHand(t *testing.T, runner, path, statements string) {
	t.Helper()
	empty := &sqlmigrate.Model{Version: sqlmigrate.ModelVersion, Dialect: sqlmigrate.SQLite, Service: "built-by-hand"}
	to, err := empty.Hash()
	require.NoError(t, err)
	toModel, err := empty.CanonicalJSON()
	require.NoError(t, err)
	plan := &sqlmigrate.Plan{
		Version: sqlmigrate.PlanVersion, Dialect: sqlmigrate.SQLite, Service: empty.Service, To: to, ToModel: toModel,
		Steps: []*sqlmigrate.Step{{
			Index: 1, Phase: sqlmigrate.Expand, Op: "buildByHand", Subject: "table/by-hand", Statements: []string{statements}, Transactional: true,
		}},
	}
	require.NoError(t, plan.Seal())
	doc, err := plan.CanonicalJSON()
	require.NoError(t, err)
	planFile := filepath.Join(t.TempDir(), "by-hand.plan.json")
	require.NoError(t, os.WriteFile(planFile, doc, 0o644))
	out, err := runMigrationRunner(t, runner, "apply", "--plan", planFile, "--database-url", path)
	require.NoError(t, err, out)
}

// TestMigratePlanFromDatabase (D116): migrate plan --from-database reads a
// SQLite file built by hand and plans from what it holds. The trigger and
// the view no model holds stop the plan, by name, until --drop names each;
// the plan then drops them first, and --fail-on stops on their hazards
// until --allow names them. --print-model prints the file's model, which
// the runner adopts; it then applies the plan, and the file holds the
// schema's model, by the runner's state and by a plan from the file, which
// has no step. Planning writes nothing to the file.
func TestMigratePlanFromDatabase(t *testing.T) {
	runner := migrationRunner(t)
	files := map[string]string{}
	for _, rel := range []string{"schema.config.json", "src/byhand.schema.json"} {
		data, err := os.ReadFile(filepath.Join(byHandService, filepath.FromSlash(rel)))
		require.NoError(t, err)
		files[rel] = string(data)
	}
	servicesRoot := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{"byhand-db": files})
	service := filepath.Join(servicesRoot, "byhand-db")
	statements, err := os.ReadFile(byHandStatements)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "app.db")
	buildFileByHand(t, runner, file, string(statements))
	built, err := os.ReadFile(file)
	require.NoError(t, err)
	base := []string{service, "--dialect", "sqlite", "--from-database", file}
	run := func(args ...string) (stdout, stderr string, err error) {
		return runMigratePlanCmd(t, append(append([]string(nil), base...), args...)...)
	}

	// The trigger and the view stop the plan, and the model's printing.
	for _, args := range [][]string{nil, {"--print-model"}} {
		_, _, err := run(args...)
		require.Error(t, err, "%v", args)
		for _, want := range []string{
			"--from-database " + file + ": sqlmigrate: the file holds 2 objects the model has no form of",
			"\n  trigger order_audit on order\n", "\n  view order_totals\n",
			"\n  --drop trigger:order_audit", "\n  --drop view:order_totals",
		} {
			assert.Contains(t, err.Error(), want, "%v", args)
		}
	}
	drops := []string{"--drop", "trigger:order_audit", "--drop", "view:order_totals"}
	_, _, err = run(append([]string{"--drop", "view:order_summary"}, drops...)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--drop view:order_summary: the file holds no view order_summary")

	planFile := filepath.Join(t.TempDir(), "plan.json")
	stdout, stderr, err := run(append([]string{"--format", "json", "--out", planFile}, drops...)...)
	require.NoError(t, err, stderr)
	assert.NotContains(t, stderr, "planning from an empty database")
	plan := decodePlan(t, stdout)
	var steps []string
	for _, step := range plan.Steps {
		steps = append(steps, string(step.Phase)+" "+step.Op+" "+step.Subject)
	}
	assert.Equal(t, []string{
		"expand dropTrigger trigger/order/order_audit",
		"expand dropView view/order_totals",
		"expand copyTable table/customer",
		"expand copyTable table/order",
		"contract copyTable table/order",
		"contract dropColumn table/customer/column/nickname",
		"contract dropTable table/audit_log",
	}, steps)
	assert.Equal(t, []string{`DROP TRIGGER "order_audit"`}, plan.Steps[0].Statements)
	assert.Equal(t, []string{`DROP VIEW "order_totals"`}, plan.Steps[1].Statements)

	// The drops' hazards gate as any other's.
	destructive := []string{
		"destructive:trigger/order/order_audit", "destructive:view/order_totals",
		"destructive:table/customer/column/nickname", "destructive:table/audit_log",
	}
	_, stderr, err = run(append([]string{"--fail-on", "destructive"}, drops...)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4 hazards not allowed")
	var allow []string
	for _, id := range destructive {
		assert.Contains(t, stderr, "--allow '"+id+"'")
		allow = append(allow, "--allow", id)
	}
	_, stderr, err = run(append(append([]string{"--fail-on", "destructive"}, drops...), allow[2:]...)...)
	require.Error(t, err)
	assert.Contains(t, stderr, "--allow 'destructive:trigger/order/order_audit'")
	assert.NotContains(t, stderr, "--allow 'destructive:table/audit_log'")
	_, stderr, err = run(append(append([]string{"--fail-on", "destructive"}, drops...), allow...)...)
	require.NoError(t, err, stderr)
	assert.Subset(t, hazardIDs(plan), append(destructive, "compat:trigger/order/order_audit", "compat:view/order_totals", "copy-table:table/customer"))

	// The file's model is the one the plan starts from, and the one adopt
	// takes.
	stdout, stderr, err = run(append([]string{"--print-model"}, drops...)...)
	require.NoError(t, err, stderr)
	var model sqlmigrate.Model
	require.NoError(t, json.Unmarshal([]byte(stdout), &model))
	assert.Equal(t, "byhand-db", model.Service)
	assert.Equal(t, sqlmigrate.SQLite, model.Dialect)
	canonical, err := model.CanonicalJSON()
	require.NoError(t, err)
	assert.Equal(t, string(canonical)+"\n", stdout, "printed in canonical form, as adopt hashes it")
	hash, err := model.Hash()
	require.NoError(t, err)
	assert.Equal(t, plan.From, hash)

	planned, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(built, planned), "planning from the file changed it")

	// The runner adopts the file's model and applies the plan.
	modelFile := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(modelFile, []byte(stdout), 0o644))
	out, err := runMigrationRunner(t, runner, "apply", "--plan", planFile, "--database-url", file)
	require.Error(t, err, "the runner applies a plan to a file whose model it has not adopted:\n%s", out)
	out, err = runMigrationRunner(t, runner, "adopt", "--model", modelFile, "--database-url", file)
	require.NoError(t, err, out)
	assert.Contains(t, out, "adopted model "+plan.From+" for service byhand-db")
	out, err = runMigrationRunner(t, runner, "apply", "--plan", planFile, "--database-url", file)
	require.NoError(t, err, out)

	schemaModel, _, err := runMigratePlanCmd(t, service, "--dialect", "sqlite", "--print-model")
	require.NoError(t, err)
	out, err = runMigrationRunner(t, runner, "status", "--service", "byhand-db", "--model", "--database-url", file)
	require.NoError(t, err, out)
	assert.Equal(t, schemaModel, out, "the runner records the schema's model")

	// The file now holds the schema: a plan from it has no step, and no
	// object to drop.
	stdout, stderr, err = run("--format", "json", "--fail-on", "all")
	require.NoError(t, err, stderr)
	after := decodePlan(t, stdout)
	assert.Empty(t, after.Steps)
	assert.Equal(t, plan.To, after.To)
	assert.Equal(t, after.From, after.To, "the file's model is the schema's")
	_, _, err = run(append([]string{"--format", "json"}, drops...)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--drop trigger:order_audit: the file holds no trigger order_audit")
}

// TestMigratePlanFromDatabaseFollowsARename: --rename names a table of
// the file by the schema's name for it, for the plan and for the model
// --print-model prints alike.
func TestMigratePlanFromDatabaseFollowsARename(t *testing.T) {
	runner := migrationRunner(t)
	servicesRoot := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": sqliteDB(orderIDField, orderTotalField, `{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "required": true}`),
	})
	file := filepath.Join(t.TempDir(), "app.db")
	buildFileByHand(t, runner, file, `CREATE TABLE purchase (id TEXT NOT NULL PRIMARY KEY, total REAL NOT NULL, tags TEXT NOT NULL DEFAULT '[]')`)
	base := []string{filepath.Join(servicesRoot, "shop-db"), "--dialect", "sqlite", "--from-database", file, "--rename", "purchase=order"}

	stdout, stderr, err := runMigratePlanCmd(t, append(base, "--format", "json")...)
	require.NoError(t, err, stderr)
	plan := decodePlan(t, stdout)
	require.Len(t, plan.Steps, 1)
	assert.Equal(t, []string{`ALTER TABLE "purchase" RENAME TO "order"`}, plan.Steps[0].Statements)

	stdout, stderr, err = runMigratePlanCmd(t, append(base, "--print-model")...)
	require.NoError(t, err, stderr)
	var model sqlmigrate.Model
	require.NoError(t, json.Unmarshal([]byte(stdout), &model))
	hash, err := model.Hash()
	require.NoError(t, err)
	assert.Equal(t, plan.From, hash)
	require.Len(t, model.Tables, 1)
	assert.Equal(t, "purchase", model.Tables[0].Name)
	assert.Equal(t, "Order", model.Tables[0].Origin)

	// Without the rename, the file's text column and the schema's list are
	// columns of two tables, and the plan drops the one and creates the
	// other.
	stdout, stderr, err = runMigratePlanCmd(t, append(base[:len(base)-2], "--format", "json")...)
	require.NoError(t, err, stderr)
	var ops []string
	for _, step := range decodePlan(t, stdout).Steps {
		ops = append(ops, step.Op+" "+step.Subject)
	}
	assert.Equal(t, []string{"createTable table/order", "dropTable table/purchase"}, ops)
}
