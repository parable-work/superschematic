package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// The fields of Order, the one table of the shop-db fixture.
const (
	orderIDField     = `{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}`
	orderTotalField  = `{"name": "total", "typeRef": {"name": "number"}, "required": true}`
	orderAmountField = `{"name": "amount", "typeRef": {"name": "number"}, "required": true}`
	orderNoteField   = `{"name": "note", "typeRef": {"name": "string"}}`
)

// migrateDB is shop-db in the JSON form with Order's fields.
func migrateDB(fields ...string) map[string]string {
	return map[string]string{
		"schema.config.json": `{"name": "shop-db", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}}}`,
		"src/shop.schema.json": `{
			"name": "shop-db",
			"kind": "DB",
			"types": {
				"Order": {"name": "Order", "role": "DBTable", "fields": [` + strings.Join(fields, ", ") + `]}
			}
		}`,
	}
}

// migrateReader is an API service named name whose view reads shop-db's
// Order through the fields given, each a name and a JSON type.
func migrateReader(name, view string, fields ...string) map[string]string {
	var defs []string
	for _, field := range fields {
		typ := "string"
		if field == "total" || field == "amount" {
			typ = "number"
		}
		defs = append(defs, `{"name": "`+field+`", "typeRef": {"name": "`+typ+`"}, "required": true}`)
	}
	defs = append(defs, `{"name": "label", "typeRef": {"name": "string"}, "virtual": true}`)
	return map[string]string{
		"schema.config.json": `{"name": "` + name + `", "kind": "API", "dependencies": [{"name": "shop-db", "kind": "DB"}], "outputs": {}}`,
		"src/views.schema.json": `{
			"imports": [{"package": "@schemas/shop-db", "types": ["Order"]}],
			"types": {
				"` + view + `": {
					"name": "` + view + `",
					"role": "APIView",
					"source": {"target": "shop-db.Order"},
					"fields": [` + strings.Join(defs, ", ") + `]
				}
			}
		}`,
	}
}

// migrateGeneral is a General service with no @source view.
func migrateGeneral() map[string]string {
	return map[string]string{
		"schema.config.json": `{"name": "shop-common", "kind": "General", "outputs": {}}`,
		"src/common.schema.json": `{
			"types": {
				"Money": {"name": "Money", "role": "EmbeddedStruct", "fields": [{"name": "cents", "typeRef": {"name": "number"}, "required": true}]}
			}
		}`,
	}
}

// writeSchemasRoot writes each service under <root>/services/<name> and
// returns the services directory.
func writeSchemasRoot(t *testing.T, root string, services map[string]map[string]string) string {
	t.Helper()
	servicesRoot := filepath.Join(root, "services")
	for name, files := range services {
		for rel, content := range files {
			path := filepath.Join(servicesRoot, name, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		}
	}
	return servicesRoot
}

func runMigratePlanCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"migrate", "plan"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestMigratePlanFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "a", "--from-ref", "main"}, "--from and --from-ref both name the previous version"},
		{[]string{"--format", "yaml"}, `--format "yaml": want json, sql or markdown`},
		{[]string{"--dialect", "mysql"}, `--dialect "mysql": want postgres or sqlite`},
		{[]string{"--fail-on", "destructive,oops"}, `--fail-on "oops" is not a hazard class: want destructive, blocking, compat, data-dependent, copy-table, api-breaking, history, or all`},
		{[]string{"--allow", "table/order"}, `--allow "table/order" is not a hazard id`},
		{[]string{"--allow", "oops:table/order"}, `--allow "oops:table/order" is not a hazard id`},
		{[]string{"--rename", "order.total=amount"}, "--rename: rename \"order.total=amount\": want old=new for a table (purchase=order) or oldTable.oldColumn=newTable.newColumn for a column (order.total=order.amount)"},
		{[]string{"--rename", "order"}, "oldTable.oldColumn=newTable.newColumn"},
		{[]string{"--print-model", "--from", "a", "--format", "json"}, "--print-model prints the new version's model and plans nothing; drop --from, --format"},
		{[]string{"--print-model", "--fail-on", "all"}, "drop --fail-on"},
	} {
		// The flags are checked before the service directory is read.
		_, _, err := runMigratePlanCmd(t, append([]string{"/nonexistent/shop-db"}, tc.args...)...)
		require.Error(t, err, "%v", tc.args)
		assert.Contains(t, err.Error(), tc.want, "%v", tc.args)
	}
}

func TestMigratePlanMissingServiceDir(t *testing.T) {
	_, _, err := runMigratePlanCmd(t, "/nonexistent/shop-db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service directory not found")
}

func TestParseHazardClasses(t *testing.T) {
	classes, err := parseHazardClasses([]string{"compat", "destructive", "compat"})
	require.NoError(t, err)
	assert.Equal(t, []sqlmigrate.HazardClass{sqlmigrate.HazardDestructive, sqlmigrate.HazardCompat}, classes, "in the order of HazardClasses, each once")

	classes, err = parseHazardClasses([]string{"all"})
	require.NoError(t, err)
	assert.Equal(t, sqlmigrate.HazardClasses, classes)

	classes, err = parseHazardClasses(nil)
	require.NoError(t, err)
	assert.Empty(t, classes)
}

func TestMigratePlanRefusesAServiceWithNoDatabase(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db":  migrateDB(orderIDField, orderTotalField),
		"shop-api": migrateReader("shop-api", "OrderView", "id", "total"),
	})
	_, _, err := runMigratePlanCmd(t, filepath.Join(servicesRoot, "shop-api"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shop-api is of kind API, which has no database")
}

// TestMigratePlanFromDetectsModelFiles: --from takes a directory as a
// checkout of the service and a file as a model, and checks either before
// any model is built.
func TestMigratePlanFromDetectsModelFiles(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	service := filepath.Join(servicesRoot, "shop-db")
	other := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-api": migrateReader("shop-api", "OrderView", "id"),
		"shop-db":  migrateDB(orderIDField),
	})
	garbage := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(garbage, []byte("not json"), 0o644))
	otherModel := filepath.Join(t.TempDir(), "model.json")
	writeModelJSON(t, otherModel, &sqlmigrate.Model{Version: sqlmigrate.ModelVersion, Dialect: sqlmigrate.Postgres, Service: "legacy-db"})

	for _, tc := range []struct {
		from string
		want string
	}{
		{garbage, "not a model"},
		{otherModel, "the model is legacy-db's, not shop-db's"},
		{filepath.Join(other, "shop-api"), "it is service shop-api, not shop-db"},
		{other, "is not a schema service under"},
		{filepath.Join(t.TempDir(), "missing.json"), "--from: stat"},
	} {
		_, _, err := runMigratePlanCmd(t, service, "--from", tc.from)
		require.Error(t, err, tc.from)
		assert.Contains(t, err.Error(), tc.want, tc.from)
	}
}

func writeModelJSON(t *testing.T, path string, model *sqlmigrate.Model) {
	t.Helper()
	data, err := model.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
}

func TestReadModelFile(t *testing.T) {
	dir := t.TempDir()
	model := &sqlmigrate.Model{
		Version: sqlmigrate.ModelVersion,
		Dialect: sqlmigrate.Postgres,
		Service: "shop-db",
		Tables: []*sqlmigrate.Table{{Name: "order", Kind: sqlmigrate.TableEntity, Origin: "Order", Columns: []*sqlmigrate.Column{
			{Name: "id", Origin: "Order.id", Type: "TEXT"},
		}}},
	}
	path := filepath.Join(dir, "model.json")
	writeModelJSON(t, path, model)

	got, err := readModelFile(path, "shop-db", sqlmigrate.Postgres)
	require.NoError(t, err)
	assert.Equal(t, model, got)

	_, err = readModelFile(path, "shop-db", sqlmigrate.SQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the model is for postgres, not --dialect sqlite")

	future := filepath.Join(dir, "future.json")
	writeModelJSON(t, future, &sqlmigrate.Model{Version: sqlmigrate.ModelVersion + 1, Dialect: sqlmigrate.Postgres, Service: "shop-db"})
	_, err = readModelFile(future, "shop-db", sqlmigrate.Postgres)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model version 2; this compiler reads version 1")

	unknown := filepath.Join(dir, "unknown.json")
	require.NoError(t, os.WriteFile(unknown, []byte(`{"version": 1, "dialect": "postgres", "service": "shop-db", "sequences": []}`), 0o644))
	_, err = readModelFile(unknown, "shop-db", sqlmigrate.Postgres)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "sequences"`)
}

func TestVersionNamingPrefersTheRootsOwnFile(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "naming.toml")
	require.NoError(t, os.WriteFile(explicit, []byte("npm_scope = \"@explicit\"\n"), 0o644))

	names, err := versionNaming(root, "")
	require.NoError(t, err)
	assert.Equal(t, naming.Default().NpmScope, names.NpmScope)

	names, err = versionNaming(root, explicit)
	require.NoError(t, err)
	assert.Equal(t, "@explicit", names.NpmScope, "a root with no naming file takes --naming")

	require.NoError(t, os.WriteFile(filepath.Join(root, naming.FileName), []byte("npm_scope = \"@own\"\n"), 0o644))
	names, err = versionNaming(root, explicit)
	require.NoError(t, err)
	assert.Equal(t, "@own", names.NpmScope, "a root's own naming file wins")
}

// migrateTestModel is the model the planner would build for shop-db with
// Order's columns named.
func migrateTestModel(columns ...string) *sqlmigrate.Model {
	table := &sqlmigrate.Table{Name: "order", Kind: sqlmigrate.TableEntity, Origin: "Order"}
	for _, column := range columns {
		table.Columns = append(table.Columns, &sqlmigrate.Column{Name: column, Origin: "Order." + column, Type: "TEXT"})
	}
	return &sqlmigrate.Model{Version: sqlmigrate.ModelVersion, Dialect: sqlmigrate.Postgres, Service: "shop-db", Tables: []*sqlmigrate.Table{table}}
}

// TestSchemaVersionReads: the readers of a version are its API and General
// services' @source views over the database, resolved against the model
// given; a DB service and a service with no views read nothing.
func TestSchemaVersionReads(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db":     migrateDB(orderIDField, orderTotalField),
		"shop-api":    migrateReader("shop-api", "OrderView", "id", "total"),
		"shop-orders": migrateReader("shop-orders", "OrderRow", "total"),
		"shop-common": migrateGeneral(),
	})
	names := naming.Default()
	version, err := openSchemaVersion(servicesRoot, names, generator.CoreRegistry(names))
	require.NoError(t, err)
	defer version.close()

	reads, err := version.reads("shop-db", migrateTestModel("id", "total"))
	require.NoError(t, err)
	assert.Equal(t, []sqlmigrate.Read{
		{Reader: "shop-api", Via: "OrderView.id", Table: "order", Column: "id"},
		{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"},
		{Reader: "shop-orders", Via: "OrderRow.total", Table: "order", Column: "total"},
	}, reads)

	// A column the model does not have is read by no one: the same views
	// against a model without total.
	reads, err = version.reads("shop-db", migrateTestModel("id"))
	require.NoError(t, err)
	assert.Equal(t, []sqlmigrate.Read{
		{Reader: "shop-api", Via: "OrderView.id", Table: "order", Column: "id"},
	}, reads)

	reads, err = version.reads("shop-db", nil)
	require.NoError(t, err)
	assert.Empty(t, reads, "an empty database has no readers")
}

func TestLoadReader(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	elsewhere := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db":   migrateDB(orderIDField, orderTotalField),
		"admin-api": migrateReader("admin-api", "OrderRow", "total"),
	})
	names := naming.Default()
	current, err := openSchemaVersion(servicesRoot, names, generator.CoreRegistry(names))
	require.NoError(t, err)
	defer current.close()
	a := &app{}

	schema, err := loadReader(a, "", current, filepath.Join(elsewhere, "admin-api"))
	require.NoError(t, err)
	reads, err := sqlmigrate.SourceReads(schema, "shop-db", migrateTestModel("id", "total"))
	require.NoError(t, err)
	assert.Equal(t, []sqlmigrate.Read{{Reader: "admin-api", Via: "OrderRow.total", Table: "order", Column: "total"}}, reads)

	_, err = loadReader(a, "", current, filepath.Join(servicesRoot, "shop-db"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shop-db is of kind DB; a reader is an API or General service")

	_, err = loadReader(a, "", current, filepath.Join(elsewhere, "missing"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service directory not found")
}

// migrateTestPlan is a hand-built plan with one hazard of each of two
// classes, for the output and the gate.
func migrateTestPlan(t *testing.T) *sqlmigrate.Plan {
	t.Helper()
	plan := &sqlmigrate.Plan{
		Version: sqlmigrate.PlanVersion,
		Dialect: sqlmigrate.Postgres,
		Service: "shop-db",
		To:      strings.Repeat("b2", 32),
		ToModel: json.RawMessage(`{}`),
		Steps: []*sqlmigrate.Step{
			{
				Index: 1, Phase: sqlmigrate.Expand, Op: "addColumn", Subject: "table/order/column/amount",
				Statements: []string{`ALTER TABLE "order" ADD COLUMN "amount" BIGINT NOT NULL`}, Transactional: true,
				Hazards: []*sqlmigrate.Hazard{{
					ID: "compat:table/order/column/amount", Class: sqlmigrate.HazardCompat, Subject: "table/order/column/amount",
					Reason: "Order.amount is required with no default: a server built from the previous version does not write it.",
				}},
			},
			{
				Index: 2, Phase: sqlmigrate.Contract, Op: "dropColumn", Subject: "table/order/column/total",
				Statements: []string{`ALTER TABLE "order" DROP COLUMN "total"`}, Transactional: true,
				Hazards: []*sqlmigrate.Hazard{{
					ID: "destructive:table/order/column/total", Class: sqlmigrate.HazardDestructive, Subject: "table/order/column/total",
					Reason: "Order.total is dropped with its data.",
				}},
			},
		},
	}
	require.NoError(t, plan.Seal())
	return plan
}

func TestFailOnHazards(t *testing.T) {
	plan := migrateTestPlan(t)
	both := []sqlmigrate.HazardClass{sqlmigrate.HazardDestructive, sqlmigrate.HazardCompat}

	var stderr bytes.Buffer
	err := failOnHazards(&stderr, plan, both, nil)
	require.Error(t, err)
	assert.Equal(t, "migrate plan: 2 hazards not allowed", err.Error())
	assert.Equal(t, `migrate plan: 2 hazards of a --fail-on class that no --allow names:
  compat:table/order/column/amount
    Order.amount is required with no default: a server built from the previous version does not write it.
  destructive:table/order/column/total
    Order.total is dropped with its data.
Review each, then allow it by its id:
  --allow 'compat:table/order/column/amount'
  --allow 'destructive:table/order/column/total'
`, stderr.String())

	stderr.Reset()
	err = failOnHazards(&stderr, plan, both, []string{"compat:table/order/column/amount"})
	require.Error(t, err)
	assert.Equal(t, "migrate plan: 1 hazard not allowed", err.Error())
	assert.NotContains(t, stderr.String(), "compat:")

	stderr.Reset()
	require.NoError(t, failOnHazards(&stderr, plan, both, []string{"compat:table/order/column/amount", "destructive:table/order/column/total"}))
	require.NoError(t, failOnHazards(&stderr, plan, nil, nil), "no --fail-on fails on nothing")
	require.NoError(t, failOnHazards(&stderr, plan, []sqlmigrate.HazardClass{sqlmigrate.HazardAPIBreaking}, nil))
	assert.Empty(t, stderr.String())
}

// TestWriteMigratePlan: --out holds the canonical plan JSON, --format
// picks what stdout gets, and --fail-on fails after both are written.
func TestWriteMigratePlan(t *testing.T) {
	plan := migrateTestPlan(t)
	canonical, err := plan.CanonicalJSON()
	require.NoError(t, err)

	for _, tc := range []struct {
		format string
		want   string
	}{
		{"json", string(canonical) + "\n"},
		{"sql", plan.SQL()},
		{"markdown", plan.Markdown()},
	} {
		out := filepath.Join(t.TempDir(), "plan.json")
		cmd := newMigratePlanCmd(&app{})
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		cmd.SetOut(stdout)
		cmd.SetErr(stderr)
		flags := &migratePlanFlags{out: out, format: tc.format, allow: []string{"compat:table/order/column/amount"}}
		in := migratePlanInputs{failOn: []sqlmigrate.HazardClass{sqlmigrate.HazardDestructive, sqlmigrate.HazardCompat}}

		err := writeMigratePlan(cmd, flags, in, plan)
		require.Error(t, err, tc.format)
		assert.Contains(t, err.Error(), "1 hazard not allowed")
		assert.Equal(t, tc.want, stdout.String(), tc.format)
		assert.Contains(t, stderr.String(), "--allow 'destructive:table/order/column/total'")
		written, err := os.ReadFile(out)
		require.NoError(t, err)
		assert.Equal(t, string(canonical)+"\n", string(written), "--out is written before --fail-on decides")
	}
}

// --- git ---

// gitTestRepo is a git repository in a temporary directory, with no
// configuration from the user's.
type gitTestRepo struct {
	t   *testing.T
	dir string
	env []string
}

func newGitTestRepo(t *testing.T) *gitTestRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	require.NoError(t, os.WriteFile(global, nil, 0o644))
	repo := &gitTestRepo{t: t, dir: filepath.Join(t.TempDir(), "repo"), env: append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+global,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)}
	require.NoError(t, os.MkdirAll(repo.dir, 0o755))
	repo.run("init", "--quiet", "--initial-branch=main")
	return repo
}

func (r *gitTestRepo) run(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
}

func (r *gitTestRepo) commit(message string) {
	r.t.Helper()
	r.run("add", "--all")
	r.run("commit", "--quiet", "--allow-empty", "-m", message)
}

func TestExtractSchemasRoot(t *testing.T) {
	repo := newGitTestRepo(t)
	schemasRoot := filepath.Join(repo.dir, "schemas")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderTotalField)})
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, naming.FileName), []byte("npm_scope = \"@shop\"\n"), 0o644))
	require.NoError(t, os.Symlink("superschematic.toml", filepath.Join(schemasRoot, "naming-link.toml")))
	repo.commit("v1")
	repo.run("tag", "v1")

	// The working tree moves on; the ref does not.
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderNoteField)})
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, "services", "shop-db", "src", "extra.schema.json"), []byte(`{"types": {}}`), 0o644))
	repo.commit("v2")

	dir, found, cleanup, err := extractSchemasRoot(t.Context(), schemasRoot, "v1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, repo.dir, filepath.Dir(dir), "the extracted root sits beside the schemas root")
	assert.True(t, strings.HasPrefix(filepath.Base(dir), ".superschematic-migrate-"))
	schema, err := os.ReadFile(filepath.Join(dir, "services", "shop-db", "src", "shop.schema.json"))
	require.NoError(t, err)
	assert.Equal(t, migrateDB(orderIDField, orderTotalField)["src/shop.schema.json"], string(schema))
	assert.NoFileExists(t, filepath.Join(dir, "services", "shop-db", "src", "extra.schema.json"))
	assert.FileExists(t, filepath.Join(dir, naming.FileName))
	link, err := os.Readlink(filepath.Join(dir, "naming-link.toml"))
	require.NoError(t, err)
	assert.Equal(t, "superschematic.toml", link)
	cleanup()
	assert.NoDirExists(t, dir)

	// A schemas root added after the ref does not exist at it.
	laterRoot := filepath.Join(repo.dir, "later")
	writeSchemasRoot(t, laterRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField)})
	repo.commit("later")
	dir, found, cleanup, err = extractSchemasRoot(t.Context(), laterRoot, "v1")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, dir)
	cleanup()

	_, _, _, err = extractSchemasRoot(t.Context(), schemasRoot, "no-such-ref")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from-ref no-such-ref: not a commit of the git repository at")

	_, _, _, err = extractSchemasRoot(t.Context(), t.TempDir(), "main")
	require.Error(t, err, "outside a repository")

	entries, err := os.ReadDir(repo.dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".superschematic-migrate-"), "left behind: %s", entry.Name())
	}
}

// --- end to end: these need sqlmigrate.BuildModel and Diff ---

func decodePlan(t *testing.T, data string) *sqlmigrate.Plan {
	t.Helper()
	var plan sqlmigrate.Plan
	require.NoError(t, json.Unmarshal([]byte(data), &plan))
	return &plan
}

func hazardIDs(plan *sqlmigrate.Plan) []string {
	var ids []string
	for _, hazard := range plan.Hazards() {
		ids = append(ids, hazard.ID)
	}
	return ids
}

func TestMigratePlanFromAnEmptyDatabase(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	out := filepath.Join(t.TempDir(), "plan.json")
	stdout, stderr, err := runMigratePlanCmd(t, filepath.Join(servicesRoot, "shop-db"), "--format", "json", "--out", out)
	assert.Contains(t, stderr, "no previous version (--from, --from-ref); planning from an empty database")
	require.NoError(t, err)

	plan := decodePlan(t, stdout)
	assert.Equal(t, "shop-db", plan.Service)
	assert.Equal(t, sqlmigrate.Postgres, plan.Dialect)
	assert.Empty(t, plan.From)
	assert.NotEmpty(t, plan.Steps)
	written, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, stdout, string(written))
}

func TestMigratePlanPrintModel(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	stdout, _, err := runMigratePlanCmd(t, filepath.Join(servicesRoot, "shop-db"), "--print-model")
	require.NoError(t, err)

	var model sqlmigrate.Model
	require.NoError(t, json.Unmarshal([]byte(stdout), &model))
	assert.Equal(t, "shop-db", model.Service)
	canonical, err := model.CanonicalJSON()
	require.NoError(t, err)
	assert.Equal(t, string(canonical)+"\n", stdout, "printed in canonical form, as adopt hashes it")

	// A plan from the printed model to the same version has no steps.
	modelFile := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(modelFile, []byte(stdout), 0o644))
	stdout, _, err = runMigratePlanCmd(t, filepath.Join(servicesRoot, "shop-db"), "--from", modelFile, "--format", "json")
	require.NoError(t, err)
	plan := decodePlan(t, stdout)
	assert.Empty(t, plan.Steps)
	assert.Equal(t, plan.From, plan.To)
}

// TestMigratePlanFromRef: the previous version is the schemas root at a
// git ref, and --fail-on stops on the drop until --allow names it.
func TestMigratePlanFromRef(t *testing.T) {
	repo := newGitTestRepo(t)
	schemasRoot := filepath.Join(repo.dir, "schemas")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderTotalField)})
	repo.commit("v1")
	// The new version is the working tree, committed or not.
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderNoteField)})
	service := filepath.Join(schemasRoot, "services", "shop-db")

	stdout, stderr, err := runMigratePlanCmd(t, service, "--from-ref", "main", "--format", "markdown", "--fail-on", "destructive")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
	assert.Contains(t, stdout, "## Migration plan for `shop-db`")
	assert.Contains(t, stderr, "--allow 'destructive:table/order/column/total'")

	stdout, _, err = runMigratePlanCmd(t, service, "--from-ref", "main", "--format", "json", "--fail-on", "destructive", "--allow", "destructive:table/order/column/total")
	require.NoError(t, err)
	plan := decodePlan(t, stdout)
	assert.NotEmpty(t, plan.From)
	assert.Contains(t, hazardIDs(plan), "destructive:table/order/column/total")

	entries, err := os.ReadDir(repo.dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".superschematic-migrate-"), "left behind: %s", entry.Name())
	}
}

func TestMigratePlanFromRefWithoutTheService(t *testing.T) {
	repo := newGitTestRepo(t)
	schemasRoot := filepath.Join(repo.dir, "schemas")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-common": migrateGeneral()})
	repo.commit("v1")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderTotalField)})

	stdout, stderr, err := runMigratePlanCmd(t, filepath.Join(schemasRoot, "services", "shop-db"), "--from-ref", "main", "--format", "json")
	assert.Contains(t, stderr, "at main there is no service shop-db; planning from an empty database")
	require.NoError(t, err)
	assert.Empty(t, decodePlan(t, stdout).From)
}

// TestMigratePlanReaders: a column the previous version's reader reads is
// dropped in contract, where only the new version's readers count. The new
// API no longer reads it, so the drop breaks no reader until a --reader
// that still does is named.
func TestMigratePlanReaders(t *testing.T) {
	previous := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db":  migrateDB(orderIDField, orderTotalField),
		"shop-api": migrateReader("shop-api", "OrderView", "id", "total"),
	})
	current := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db":  migrateDB(orderIDField),
		"shop-api": migrateReader("shop-api", "OrderView", "id"),
	})
	lagging := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db":   migrateDB(orderIDField, orderTotalField),
		"admin-api": migrateReader("admin-api", "OrderRow", "total"),
	})
	service := filepath.Join(current, "shop-db")
	from := filepath.Join(previous, "shop-db")

	stdout, _, err := runMigratePlanCmd(t, service, "--from", from, "--format", "json", "--fail-on", "api-breaking")
	require.NoError(t, err)
	for _, id := range hazardIDs(decodePlan(t, stdout)) {
		assert.NotContains(t, id, "api-breaking:", "no reader after the rollout reads total")
	}

	const breaking = "api-breaking:table/order/column/total@admin-api/OrderRow.total"
	stdout, stderr, err := runMigratePlanCmd(t, service, "--from", from, "--reader", filepath.Join(lagging, "admin-api"), "--format", "json", "--fail-on", "api-breaking")
	require.Error(t, err)
	assert.Contains(t, hazardIDs(decodePlan(t, stdout)), breaking)
	assert.Contains(t, stderr, "--allow '"+breaking+"'")

	_, _, err = runMigratePlanCmd(t, service, "--from", from, "--reader", filepath.Join(lagging, "admin-api"), "--fail-on", "api-breaking", "--allow", breaking)
	require.NoError(t, err)
}

// sqliteDB is migrateDB with outputs.sql.dialects listing sqlite.
func sqliteDB(fields ...string) map[string]string {
	files := migrateDB(fields...)
	files["schema.config.json"] = `{"name": "shop-db", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}, "sql": {"dialects": ["postgres", "sqlite"]}}}`
	return files
}

// TestMigratePlanSQLite: --dialect sqlite plans a service whose
// outputs.sql.dialects lists sqlite, with a rebuild for what SQLite's ALTER
// TABLE cannot change, and refuses one that does not list it.
func TestMigratePlanSQLite(t *testing.T) {
	postgresOnly := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	for _, args := range [][]string{{"--dialect", "sqlite"}, {"--dialect", "sqlite", "--print-model"}} {
		_, _, err := runMigratePlanCmd(t, append([]string{filepath.Join(postgresOnly, "shop-db")}, args...)...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), "shop-db is built for postgres, not sqlite: add sqlite to its outputs.sql.dialects to plan for it")
	}

	// A data-form config is checked against its JSON Schema first, which
	// names the dialects; the registry says why for the rest.
	for list, want := range map[string]string{
		`["mysql", "postgres"]`:    "at '/outputs/sql/dialects/0': value must be one of 'postgres', 'sqlite'",
		`["sqlite"]`:               "outputs.sql.dialects must list postgres",
		`["postgres", "postgres"]`: "outputs.sql.dialects lists postgres twice",
	} {
		files := migrateDB(orderIDField)
		files["schema.config.json"] = `{"name": "shop-db", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}, "sql": {"dialects": ` + list + `}}}`
		root := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{"shop-db": files})
		_, _, err := runMigratePlanCmd(t, filepath.Join(root, "shop-db"), "--print-model")
		require.Error(t, err, list)
		assert.Contains(t, err.Error(), want, list)
	}

	previous := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": sqliteDB(orderIDField, orderTotalField),
	})
	current := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": sqliteDB(orderIDField, `{"name": "total", "typeRef": {"name": "number"}}`, orderNoteField),
	})
	service := filepath.Join(current, "shop-db")

	stdout, _, err := runMigratePlanCmd(t, service, "--dialect", "sqlite", "--print-model")
	require.NoError(t, err)
	var model sqlmigrate.Model
	require.NoError(t, json.Unmarshal([]byte(stdout), &model))
	assert.Equal(t, sqlmigrate.SQLite, model.Dialect)

	stdout, _, err = runMigratePlanCmd(t, service, "--dialect", "sqlite", "--from", filepath.Join(previous, "shop-db"), "--format", "json")
	require.NoError(t, err)
	plan := decodePlan(t, stdout)
	assert.Equal(t, sqlmigrate.SQLite, plan.Dialect)
	var ops []string
	for _, step := range plan.Steps {
		ops = append(ops, string(step.Phase)+" "+step.Op+" "+step.Subject)
		assert.True(t, step.Transactional, "step %d", step.Index)
	}
	// The rebuild that drops total's NOT NULL adds note too.
	require.Equal(t, []string{"expand copyTable table/order"}, ops)
	// The rebuild runs with foreign keys on, its checks deferred to its
	// commit, as D1 runs it (D27, amended).
	assert.False(t, plan.Steps[0].ForeignKeysOff)
	assert.Equal(t, "PRAGMA defer_foreign_keys = ON", plan.Steps[0].Statements[0])
	assert.Contains(t, strings.Join(plan.Steps[0].Statements, "\n"), `"note" TEXT`)
	assert.Contains(t, hazardIDs(plan), "copy-table:table/order")

	// Postgres plans the same service too.
	stdout, _, err = runMigratePlanCmd(t, service, "--from", filepath.Join(previous, "shop-db"), "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, sqlmigrate.Postgres, decodePlan(t, stdout).Dialect)
}

// TestMigratePlanSQLiteChecksThePreviousVersion: --dialect sqlite refuses a
// previous version, a directory or a git ref, whose outputs.sql.dialects
// does not list sqlite, and says to plan from the model the database
// recorded or from an empty database. A model file is checked by its own
// dialect, so a SQLite model plans.
func TestMigratePlanSQLiteChecksThePreviousVersion(t *testing.T) {
	const refused = "the previous version of shop-db was not built for sqlite (its outputs.sql.dialects lists postgres); " +
		"plan from the model the database recorded (--from <model.json>, as superschematic-migrate status --model prints it), " +
		"or from an empty database with neither --from nor --from-ref"

	repo := newGitTestRepo(t)
	schemasRoot := filepath.Join(repo.dir, "schemas")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": migrateDB(orderIDField, orderTotalField)})
	repo.commit("v1, for postgres only")
	writeSchemasRoot(t, schemasRoot, map[string]map[string]string{"shop-db": sqliteDB(orderIDField, orderTotalField, orderNoteField)})
	service := filepath.Join(schemasRoot, "services", "shop-db")
	previous := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})

	for _, from := range [][]string{{"--from-ref", "main"}, {"--from", filepath.Join(previous, "shop-db")}} {
		_, _, err := runMigratePlanCmd(t, append([]string{service, "--dialect", "sqlite", "--format", "json"}, from...)...)
		require.Error(t, err, "%v", from)
		assert.Contains(t, err.Error(), "migrate plan: "+strings.Join(from, " ")+": "+refused)

		// Postgres plans from the same version.
		_, _, err = runMigratePlanCmd(t, append([]string{service, "--format", "json"}, from...)...)
		require.NoError(t, err, "%v", from)
	}

	// The model a SQLite database recorded plans, whatever its version
	// listed.
	stdout, _, err := runMigratePlanCmd(t, service, "--dialect", "sqlite", "--print-model")
	require.NoError(t, err)
	modelFile := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(modelFile, []byte(stdout), 0o644))
	stdout, _, err = runMigratePlanCmd(t, service, "--dialect", "sqlite", "--from", modelFile, "--format", "json")
	require.NoError(t, err)
	assert.Empty(t, decodePlan(t, stdout).Steps)
}

func TestMigratePlanRename(t *testing.T) {
	previous := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderTotalField),
	})
	current := writeSchemasRoot(t, filepath.Join(t.TempDir(), "schemas"), map[string]map[string]string{
		"shop-db": migrateDB(orderIDField, orderAmountField),
	})
	service := filepath.Join(current, "shop-db")
	from := filepath.Join(previous, "shop-db")

	stdout, _, err := runMigratePlanCmd(t, service, "--from", from, "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, hazardIDs(decodePlan(t, stdout)), "destructive:table/order/column/total", "without --rename the change is a drop and an add")

	stdout, _, err = runMigratePlanCmd(t, service, "--from", from, "--format", "json", "--rename", "order.total=order.amount", "--fail-on", "destructive")
	require.NoError(t, err)
	plan := decodePlan(t, stdout)
	assert.Equal(t, []string{"order.total=order.amount"}, plan.Renames)
	assert.NotContains(t, hazardIDs(plan), "destructive:table/order/column/total")
}
