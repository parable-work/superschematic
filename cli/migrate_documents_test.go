package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// The migrations example's two versions of tasks-db, a DB service in
// TypeScript built for Postgres and SQLite, whose documents format
// --to=json writes for the tests below (D117).
const (
	tasksV1Service = "../examples/migrations/v1/schemas/services/tasks-db"
	tasksV2Service = "../examples/migrations/v2/schemas/services/tasks-db"
	tasksRename    = "task.title=task.summary"
)

// runnerVectorsDir holds the plans the runners' tests apply
// (runtime/migrate/README.md). Its cases documents and sqlite-documents
// are plans from documents, which TestPlansFromDocumentsAreRunnerVectors
// writes; internal/sqlmigrate writes the others.
const runnerVectorsDir = "../runtime/migrate/testdata/plans"

// tasksDocument is the document format --to=json writes from the
// migrations example's tasks.schema.ts of service, written to a file of
// dir whose path it returns.
func tasksDocument(t *testing.T, service, dir string) string {
	t.Helper()
	out, err := runFormatCommand(t, "--to=json", "--stdout", filepath.Join(service, "src", "tasks.schema.ts"))
	require.NoError(t, err)
	path := filepath.Join(dir, filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(service))))+".tasks.schema.json")
	require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
	return path
}

// runMigratePlanIn runs migrate plan with stdin.
func runMigratePlanIn(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root := New(Config{})
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"migrate", "plan"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// TestMigratePlanDocumentsFlagErrors: --documents takes documents as its
// arguments and no flag that reads a schemas root; --request takes the
// request alone. Each refusal comes before anything loads.
func TestMigratePlanDocumentsFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--documents"}, "--documents takes the schema documents of one DB service as its arguments, or - for stdin"},
		{[]string{"--documents", "a.schema.json", "--from-ref", "main"}, "--documents plans from the model a database recorded (--from <model.json>) or an empty database, with no schemas root beside the documents; drop --from-ref"},
		{[]string{"--documents", "a.schema.json", "--reader", "shop-api", "--database-url", "postgres://x"}, "drop --reader, --database-url"},
		{[]string{"--documents", "a.schema.json", "--dialect", "sqlite", "--from-database", "app.db", "--drop", "view:v"}, "drop --from-database, --drop"},
		{[]string{"--documents", "a.schema.json", "--format", "yaml"}, `--format "yaml": want json, sql or markdown`},
		{[]string{"--documents", "a.schema.json", "--fail-on", "oops"}, `--fail-on "oops" is not a hazard class`},
		{[]string{"--documents", "a.schema.json", "--print-model", "--from", "m.json"}, "--print-model prints the new version's model and plans nothing; drop --from"},
		{[]string{"--request", "-", "a.schema.json"}, "--request holds the documents; give no arguments with it"},
	} {
		_, _, err := runMigratePlanCmd(t, tc.args...)
		require.Error(t, err, "%v", tc.args)
		assert.Contains(t, err.Error(), tc.want, "%v", tc.args)
	}

	// --request answers a flag it does not take on stdout too.
	stdout, _, err := runMigratePlanIn(t, "", "--request", "-", "--dialect", "sqlite", "--out", "plan.json")
	require.Error(t, err)
	want := "the request holds the documents, the model and the plan's options; drop --dialect, --out"
	assert.Contains(t, err.Error(), want)
	assert.Equal(t, `{"version":1,"error":"`+want+`"}`+"\n", stdout)
}

// TestMigratePlanDocumentsPlanAsTheServiceDirectory: the documents format
// --to=json writes from a DB service resolve to the model its directory
// resolves to, in each dialect, and the plan from the model of the
// previous version's documents is the plan from its directory.
func TestMigratePlanDocumentsPlanAsTheServiceDirectory(t *testing.T) {
	dir := t.TempDir()
	v1, v2 := tasksDocument(t, tasksV1Service, dir), tasksDocument(t, tasksV2Service, dir)
	for _, dialect := range []string{"postgres", "sqlite"} {
		for _, version := range []struct{ service, document string }{{tasksV1Service, v1}, {tasksV2Service, v2}} {
			fromDirectory, _, err := runMigratePlanCmd(t, version.service, "--dialect", dialect, "--print-model")
			require.NoError(t, err)
			fromDocuments, _, err := runMigratePlanCmd(t, "--documents", version.document, "--dialect", dialect, "--print-model")
			require.NoError(t, err)
			assert.Equal(t, fromDirectory, fromDocuments, "%s, %s", dialect, version.service)
		}

		v1Model, _, err := runMigratePlanCmd(t, "--documents", v1, "--dialect", dialect, "--print-model")
		require.NoError(t, err)
		modelFile := filepath.Join(dir, "v1."+dialect+".model.json")
		require.NoError(t, os.WriteFile(modelFile, []byte(v1Model), 0o644))
		for _, format := range []string{"json", "sql", "markdown"} {
			fromDirectory, _, err := runMigratePlanCmd(t, tasksV2Service, "--from", tasksV1Service, "--rename", tasksRename, "--dialect", dialect, "--format", format)
			require.NoError(t, err)
			fromDocuments, stderr, err := runMigratePlanCmd(t, "--documents", v2, "--from", modelFile, "--rename", tasksRename, "--dialect", dialect, "--format", format)
			require.NoError(t, err, stderr)
			assert.Equal(t, fromDirectory, fromDocuments, "%s, %s", dialect, format)
		}

		// The plan from the documents' own model is empty.
		v2Model, _, err := runMigratePlanCmd(t, "--documents", v2, "--dialect", dialect, "--print-model")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(modelFile, []byte(v2Model), 0o644))
		stdout, _, err := runMigratePlanCmd(t, "--documents", v2, "--from", modelFile, "--dialect", dialect, "--format", "json", "--fail-on", "all")
		require.NoError(t, err)
		plan := decodePlan(t, stdout)
		assert.Empty(t, plan.Steps, dialect)
		assert.Equal(t, plan.From, plan.To, dialect)
	}
}

// TestMigratePlanDocumentsFromStdin: - reads documents from stdin, one
// JSON value after another, beside the files the other arguments name; a
// plan with no --from starts from an empty database and says so; --out,
// --fail-on and --allow work as they do for a directory.
func TestMigratePlanDocumentsFromStdin(t *testing.T) {
	orders := migrateDB(orderIDField, orderTotalField)["src/shop.schema.json"]
	customers := `{"name": "shop-db", "kind": "DB", "types": {"Customer": {"name": "Customer", "role": "DBTable", "fields": [` + orderIDField + `]}}}`
	file := filepath.Join(t.TempDir(), "customers.schema.json")
	require.NoError(t, os.WriteFile(file, []byte(customers), 0o644))

	out := filepath.Join(t.TempDir(), "plan.json")
	stdout, stderr, err := runMigratePlanIn(t, orders, "--documents", "-", file, "--format", "json", "--out", out)
	require.NoError(t, err)
	assert.Contains(t, stderr, "no previous version (--from); planning from an empty database")
	plan := decodePlan(t, stdout)
	assert.Empty(t, plan.From)
	written, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, stdout, string(written))
	var tables []string
	for _, step := range plan.Steps {
		if step.Op == "createTable" {
			tables = append(tables, step.Subject)
		}
	}
	assert.ElementsMatch(t, []string{"table/order", "table/customer"}, tables)

	both, _, err := runMigratePlanIn(t, orders+"\n"+customers, "--documents", "-", "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, stdout, both, "two documents on stdin plan as a file and stdin")

	// The model the plan ends at, and a plan from it that drops a column.
	modelFile := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(modelFile, append([]byte(plan.ToModel), '\n'), 0o644))
	next := migrateDB(orderIDField)["src/shop.schema.json"]
	const drop = "destructive:table/order/column/total"
	_, stderr, err = runMigratePlanIn(t, next, "--documents", "-", file, "--from", modelFile, "--fail-on", "destructive")
	require.Error(t, err)
	assert.Contains(t, stderr, "--allow '"+drop+"'")
	_, _, err = runMigratePlanIn(t, next, "--documents", "-", file, "--from", modelFile, "--fail-on", "destructive", "--allow", drop)
	require.NoError(t, err)
}

// TestMigratePlanDocumentsRefusals: a document that is missing, a
// directory or malformed, a previous version that is no model file, and a
// model of another service each stop the plan by name.
func TestMigratePlanDocumentsRefusals(t *testing.T) {
	dir := t.TempDir()
	orders := filepath.Join(dir, "shop.schema.json")
	require.NoError(t, os.WriteFile(orders, []byte(migrateDB(orderIDField, orderTotalField)["src/shop.schema.json"]), 0o644))
	malformed := filepath.Join(dir, "broken.schema.json")
	require.NoError(t, os.WriteFile(malformed, []byte(`{"name": "shop-db", "kind": "DB", "types": {"Order": {"name": "Order", "role": "Table"}}}`), 0o644))
	otherModel := filepath.Join(dir, "legacy.model.json")
	writeModelJSON(t, otherModel, &sqlmigrate.Model{Version: sqlmigrate.ModelVersion, Dialect: sqlmigrate.Postgres, Service: "legacy-db"})

	for _, tc := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{filepath.Join(dir, "missing.schema.json")}, "schema document not found: " + filepath.Join(dir, "missing.schema.json")},
		{"", []string{dir}, dir + " is a directory; --documents takes schema documents, each a file as format --to=json writes it, or - for stdin"},
		{"", []string{"-", "-"}, "--documents names - twice; stdin is read once"},
		{"", []string{"-"}, "stdin holds no schema document"},
		{"{", []string{"-"}, "stdin #1: not JSON: unexpected EOF"},
		{"", []string{malformed}, "migrate plan: not a schema document: " + malformed},
		{"", []string{orders, "--from", filepath.Join(dir, "missing.model.json")}, "--from: stat " + filepath.Join(dir, "missing.model.json")},
		{"", []string{orders, "--from", dir}, "--from " + dir + " is a directory; with --documents, --from takes the model the database recorded"},
		{"", []string{orders, "--from", otherModel}, "the model is legacy-db's, not shop-db's"},
		{"", []string{orders, "--from", otherModel, "--dialect", "sqlite"}, "the model is legacy-db's, not shop-db's"},
	} {
		_, _, err := runMigratePlanIn(t, tc.stdin, append([]string{"--documents"}, tc.args...)...)
		require.Error(t, err, "%v", tc.args)
		assert.Contains(t, err.Error(), tc.want, "%v", tc.args)
	}
}

// planRequest is a plan request's JSON for documents in dialect, from the
// model given (null for an empty database), with renames.
func planRequest(t *testing.T, dialect string, documents []string, from json.RawMessage, renames ...string) string {
	t.Helper()
	docs := make([]json.RawMessage, len(documents))
	for i, doc := range documents {
		docs[i] = json.RawMessage(doc)
	}
	if from == nil {
		from = json.RawMessage("null")
	}
	data, err := json.Marshal(map[string]any{"version": 1, "dialect": dialect, "documents": docs, "from": from, "renames": renames})
	require.NoError(t, err)
	return string(data)
}

// planResponse is the response --request prints when it plans.
type planResponse struct {
	Version int                  `json:"version"`
	Plan    json.RawMessage      `json:"plan"`
	Hazards []*sqlmigrate.Hazard `json:"hazards"`
	Refused []string             `json:"refused"`
}

// requestPlan sends body to migrate plan --request - and returns the
// response, which must hold a plan.
func requestPlan(t *testing.T, body string) planResponse {
	t.Helper()
	stdout, stderr, err := runMigratePlanIn(t, body, "--request", "-")
	require.NoError(t, err, stderr)
	require.Equal(t, 1, strings.Count(stdout, "\n"), "one line of JSON")
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var response planResponse
	require.NoError(t, decoder.Decode(&response))
	require.Equal(t, 1, response.Version)
	return response
}

// TestMigratePlanRequest: --request reads a plan request from stdin or a
// file and prints its plan as the documents plan with --documents, with
// the hazards and those it refuses; a request it cannot plan prints the
// error and exits 1.
func TestMigratePlanRequest(t *testing.T) {
	dir := t.TempDir()
	v1, v2 := tasksDocument(t, tasksV1Service, dir), tasksDocument(t, tasksV2Service, dir)
	v1Doc, err := os.ReadFile(v1)
	require.NoError(t, err)
	v2Doc, err := os.ReadFile(v2)
	require.NoError(t, err)

	for _, dialect := range []string{"postgres", "sqlite"} {
		v1Model, _, err := runMigratePlanCmd(t, "--documents", v1, "--dialect", dialect, "--print-model")
		require.NoError(t, err)
		modelFile := filepath.Join(dir, "v1."+dialect+".model.json")
		require.NoError(t, os.WriteFile(modelFile, []byte(v1Model), 0o644))
		want, _, err := runMigratePlanCmd(t, "--documents", v2, "--from", modelFile, "--rename", tasksRename, "--dialect", dialect, "--format", "json")
		require.NoError(t, err)

		body := planRequest(t, dialect, []string{string(v2Doc)}, json.RawMessage(v1Model), tasksRename)
		response := requestPlan(t, body)
		assert.Equal(t, want, string(response.Plan)+"\n", "%s: the plan --documents prints", dialect)
		assert.Equal(t, decodePlan(t, want).Hazards(), response.Hazards, dialect)
		assert.Empty(t, response.Refused, "%s: no failOn refuses nothing", dialect)

		// The same request from a file, refusing destructive hazards
		// unless allowed: v2 drops comment.legacy_id.
		var gated map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &gated))
		gated["failOn"] = []string{"destructive"}
		data, err := json.Marshal(gated)
		require.NoError(t, err)
		requestFile := filepath.Join(dir, "request."+dialect+".json")
		require.NoError(t, os.WriteFile(requestFile, data, 0o644))
		stdout, _, err := runMigratePlanCmd(t, "--request", requestFile)
		require.NoError(t, err, "a refused hazard is no error: the response lists it")
		var refused planResponse
		require.NoError(t, json.Unmarshal([]byte(stdout), &refused))
		assert.Contains(t, refused.Refused, "destructive:table/comment/column/legacy_id", dialect)
		assert.Equal(t, response.Plan, refused.Plan, dialect)
	}

	// The first plan of a database starts from null, an empty database.
	first := requestPlan(t, planRequest(t, "sqlite", []string{string(v1Doc)}, nil))
	assert.Empty(t, decodePlan(t, string(first.Plan)).From)

	// A request it cannot plan prints the error alone.
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"version": 1, "dialect": "sqlite", "documents": [` + string(v1Doc) + `]}`, "the request names no model to plan from (from)"},
		{planRequest(t, "sqlite", []string{`{"name": "tasks-db", "kind": "DB", "types": 3}`}, nil), "not a schema document: documents[0]"},
		{planRequest(t, "postgres", []string{string(v1Doc)}, json.RawMessage(`{"version": 1, "dialect": "sqlite", "service": "tasks-db"}`)), "from: the model is for sqlite, not postgres"},
		{`not json`, "not a plan request"},
	} {
		stdout, _, err := runMigratePlanIn(t, tc.body, "--request", "-")
		require.Error(t, err, tc.want)
		assert.Contains(t, err.Error(), "migrate plan --request: "+tc.want)
		var refusal struct {
			Version int    `json:"version"`
			Error   string `json:"error"`
		}
		decoder := json.NewDecoder(strings.NewReader(stdout))
		decoder.DisallowUnknownFields()
		require.NoError(t, decoder.Decode(&refusal), stdout)
		assert.Equal(t, 1, refusal.Version)
		assert.Contains(t, refusal.Error, tc.want)
	}
	_, _, err = runMigratePlanCmd(t, "--request", filepath.Join(dir, "missing.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.json: no such file or directory")
}

// documentVectorCases are the cases of the runners' plan vectors made from
// documents: the migrations example's v1 from an empty database, then v2
// from v1's model with its rename, each planned through --request from the
// documents format --to=json writes.
var documentVectorCases = map[string]string{"documents": "postgres", "sqlite-documents": "sqlite"}

// documentVectors plans each case of documentVectorCases and returns each
// plan's path under runnerVectorsDir and its contents: the plan's
// canonical JSON, indented, as internal/sqlmigrate writes its vectors.
func documentVectors(t *testing.T) map[string][]byte {
	t.Helper()
	dir := t.TempDir()
	var docs []string
	for _, service := range []string{tasksV1Service, tasksV2Service} {
		data, err := os.ReadFile(tasksDocument(t, service, dir))
		require.NoError(t, err)
		docs = append(docs, string(data))
	}
	files := map[string][]byte{}
	for name, dialect := range documentVectorCases {
		first := requestPlan(t, planRequest(t, dialect, docs[:1], nil))
		toModel := decodePlan(t, string(first.Plan)).ToModel
		second := requestPlan(t, planRequest(t, dialect, docs[1:], toModel, tasksRename))
		for i, plan := range []json.RawMessage{first.Plan, second.Plan} {
			var indented bytes.Buffer
			require.NoError(t, json.Indent(&indented, plan, "", "  "))
			indented.WriteByte('\n')
			files[filepath.Join(name, []string{"01-tasks-v1.plan.json", "02-tasks-v2.plan.json"}[i])] = indented.Bytes()
		}
	}
	return files
}

// TestPlansFromDocumentsAreRunnerVectors keeps the cases of
// documentVectorCases under runtime/migrate/testdata/plans equal to what a
// plan from documents writes: the Go runner's tests apply them on SQLite,
// D1 and Postgres, and the Rust runner's on SQLite, each refusing them on
// a database at another model. Regenerate with:
// go test ./cli -run TestPlansFromDocumentsAreRunnerVectors -update
func TestPlansFromDocumentsAreRunnerVectors(t *testing.T) {
	files := documentVectors(t)
	var existing []string
	for name := range documentVectorCases {
		paths, err := filepath.Glob(filepath.Join(runnerVectorsDir, name, "*"))
		require.NoError(t, err)
		for _, path := range paths {
			rel, err := filepath.Rel(runnerVectorsDir, path)
			require.NoError(t, err)
			existing = append(existing, rel)
		}
	}
	sort.Strings(existing)
	if *updateFormat {
		for _, rel := range existing {
			if _, ok := files[rel]; !ok {
				require.NoError(t, os.Remove(filepath.Join(runnerVectorsDir, rel)))
			}
		}
		for rel, data := range files {
			path := filepath.Join(runnerVectorsDir, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, data, 0o644))
		}
		return
	}
	for _, rel := range existing {
		if _, ok := files[rel]; !ok {
			t.Errorf("%s is not a plan from documents (run with -update to remove it)", rel)
		}
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(runnerVectorsDir, rel))
		if err != nil {
			t.Errorf("%s is missing (run with -update to write it)", rel)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale (run with -update to rewrite it)", rel)
		}
	}
}

// TestAPlanFromDocumentsIsHeldToTheRecordedModel: the publish D117 makes
// of a runtime database on a SQLite file, through the binaries. The first
// plan, from documents and an empty database, is applied by the runner;
// the next is planned from the model the file recorded, as status --model
// prints it, and applied; and a plan made from the model the file held
// before, as a plan made elsewhere would be, is refused, since its from is
// not the recorded model (D27).
func TestAPlanFromDocumentsIsHeldToTheRecordedModel(t *testing.T) {
	runner := migrationRunner(t)
	dir := t.TempDir()
	var docs []string
	for _, service := range []string{tasksV1Service, tasksV2Service} {
		data, err := os.ReadFile(tasksDocument(t, service, dir))
		require.NoError(t, err)
		docs = append(docs, string(data))
	}
	file := filepath.Join(dir, "tasks.db")
	writePlan := func(name string, response planResponse) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, response.Plan, 0o644))
		return path
	}
	recorded := func() json.RawMessage {
		out, err := runMigrationRunner(t, runner, "status", "--service", "tasks-db", "--model", "--database-url", file)
		require.NoError(t, err, out)
		return json.RawMessage(out)
	}

	first := requestPlan(t, planRequest(t, "sqlite", docs[:1], nil))
	out, err := runMigrationRunner(t, runner, "apply", "--plan", writePlan("v1.plan.json", first), "--database-url", file)
	require.NoError(t, err, out)
	v1 := recorded()
	assert.JSONEq(t, string(decodePlan(t, string(first.Plan)).ToModel), string(v1), "the file records the plan's to")

	second := requestPlan(t, planRequest(t, "sqlite", docs[1:], v1, tasksRename))
	out, err = runMigrationRunner(t, runner, "apply", "--plan", writePlan("v2.plan.json", second), "--database-url", file)
	require.NoError(t, err, out)
	v2Model, _, err := runMigratePlanIn(t, docs[1], "--documents", "-", "--dialect", "sqlite", "--print-model")
	require.NoError(t, err)
	assert.Equal(t, v2Model, string(recorded()), "the file holds the documents' model")

	// The plan the file took, applied again, does nothing, and a plan from
	// the model it records to the same documents is empty.
	out, err = runMigrationRunner(t, runner, "apply", "--plan", filepath.Join(dir, "v2.plan.json"), "--database-url", file)
	require.NoError(t, err, out)
	assert.Contains(t, out, " is already applied: ")
	again := requestPlan(t, planRequest(t, "sqlite", docs[1:], recorded()))
	assert.Empty(t, decodePlan(t, string(again.Plan)).Steps)

	// A plan made from v1's model, where the file was before the second
	// plan, as a plan made elsewhere from a stale model would be, is
	// refused.
	newer := strings.Replace(docs[1], `"name": "body",`, `"name": "text",`, 1)
	require.NotEqual(t, docs[1], newer)
	elsewhere := requestPlan(t, planRequest(t, "sqlite", []string{newer}, v1, tasksRename))
	out, err = runMigrationRunner(t, runner, "apply", "--plan", writePlan("elsewhere.plan.json", elsewhere), "--database-url", file)
	require.Error(t, err, out)
	assert.Contains(t, out, "the plan starts from model "+decodePlan(t, string(first.Plan)).To+", but service tasks-db is at model "+decodePlan(t, string(second.Plan)).To+"; plan again from the applied model")
}
