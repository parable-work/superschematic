package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// developmentDocuments holds development mode's General documents of the
// engine examples (D117), copied into the planner's testdata.
const developmentDocuments = "../internal/migrateplan/testdata/development"

func developmentDocument(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(developmentDocuments, name+".schema.json"))
	require.NoError(t, err)
	return data
}

// TestMigratePlanDocumentsLowersGeneralDocuments: --documents plans
// development mode's General documents as the tables of the DB service
// --service names, or of the name they share (D117); --service is for
// --documents alone, and --request names the service in the request.
func TestMigratePlanDocumentsLowersGeneralDocuments(t *testing.T) {
	jobs := []string{
		filepath.Join(developmentDocuments, "batches.schema.json"),
		filepath.Join(developmentDocuments, "jobs.schema.json"),
		filepath.Join(developmentDocuments, "workers.schema.json"),
	}
	_, _, err := runMigratePlanIn(t, "", append([]string{"--documents", "--dialect", "sqlite", "--format", "json"}, jobs...)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "General schemas Batch, Job and Worker, which lower to the tables of one DB service (D117): name the service (service, --service)")

	out, _, err := runMigratePlanIn(t, "", append([]string{"--documents", "--service", "jobs-db", "--dialect", "sqlite", "--format", "json"}, jobs...)...)
	require.NoError(t, err)
	var plan struct {
		Service string            `json:"service"`
		Steps   []json.RawMessage `json:"steps"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &plan), out)
	assert.Equal(t, "jobs-db", plan.Service)
	assert.NotEmpty(t, plan.Steps)
	assert.Contains(t, out, `\"status_entered_at\"`)

	out, _, err = runMigratePlanIn(t, "", "--documents", "--dialect", "sqlite", "--format", "json", filepath.Join(developmentDocuments, "notes.schema.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &plan), out)
	assert.Equal(t, "notes", plan.Service, "one General document's service is its name")

	_, _, err = runMigratePlanIn(t, "", "./svc", "--service", "jobs-db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--service names the DB service development mode's General documents lower into; give --documents with it")
	_, _, err = runMigratePlanIn(t, "{}", "--request", "-", "--service", "jobs-db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drop --service")
}

// writeServiceTSConfig writes the tsconfig.json a service directory needs
// to compile here: the authoring packages resolve to the repository's, as
// a schemas root's base config resolves them.
func writeServiceTSConfig(t *testing.T, dir string) {
	t.Helper()
	packages, err := filepath.Abs("../packages")
	require.NoError(t, err)
	paths := map[string][]string{
		"superscalar": {filepath.ToSlash(filepath.Join(testpaths.Local(t).ScalarTypeScript, "src", "index.ts"))},
	}
	for _, name := range []string{"db", "schema", "schema-config"} {
		paths["@superschematic/"+name] = []string{filepath.ToSlash(filepath.Join(packages, name, "src", "index.ts"))}
	}
	config, err := json.MarshalIndent(map[string]any{
		"compilerOptions": map[string]any{
			"target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": []string{"ES2022"},
			"strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
			"noEmit": true, "skipLibCheck": true, "baseUrl": ".", "paths": paths,
		},
		"include": []string{"schema.config.ts", "src/**/*.ts"},
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"), config, 0o644))
}

// TestMigrateExportTakesARuntimeDatabaseToCompiled: a runtime database
// that a publish of development mode's General documents took to its
// tables, its plan and record applied by the migration runner as runtime
// mode applies them, exports as a compiled DB service (D117): its config,
// its TypeScript schema files and the model it recorded, whose build's
// first plan from that model is empty. An export over the files is
// refused without --force, and a database with no record is refused.
func TestMigrateExportTakesARuntimeDatabaseToCompiled(t *testing.T) {
	runner := migrationRunner(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "runtime.db")

	// The publish: the plan and its layout, then the plan applied with
	// its record.
	documents := []json.RawMessage{developmentDocument(t, "batches"), developmentDocument(t, "jobs"), developmentDocument(t, "workers")}
	request, err := json.Marshal(map[string]any{"version": 1, "dialect": "sqlite", "documents": documents, "from": nil, "service": "jobs-db", "layout": true})
	require.NoError(t, err)
	out, _, err := runMigratePlanIn(t, string(request), "--request", "-")
	require.NoError(t, err, out)
	var response struct {
		Plan   json.RawMessage `json:"plan"`
		Layout json.RawMessage `json:"layout"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	var plan struct {
		To string `json:"to"`
	}
	require.NoError(t, json.Unmarshal(response.Plan, &plan))
	record, err := json.Marshal(map[string]any{
		"version": 1, "service": "jobs-db", "modelHash": plan.To, "documents": documents,
		"layout": response.Layout, "versions": map[string]int{"Batch": 1, "Job": 1, "Worker": 1},
	})
	require.NoError(t, err)
	planFile, recordFile := filepath.Join(dir, "plan.json"), filepath.Join(dir, "record.json")
	require.NoError(t, os.WriteFile(planFile, response.Plan, 0o644))
	require.NoError(t, os.WriteFile(recordFile, record, 0o644))
	applied, err := runMigrationRunner(t, runner, "apply", "--plan", planFile, "--record", recordFile, "--database-url", db)
	require.NoError(t, err, applied)

	service := filepath.Join(dir, "jobs-db")
	exported, err := runMigrateCommand(t, "export", "--database-url", db, "--out", service)
	require.NoError(t, err, exported)
	for _, written := range []string{"schema.config.ts", "model.json", "src/batch.schema.ts", "src/job.schema.ts", "src/worker.schema.ts"} {
		assert.Contains(t, exported, "Wrote "+filepath.Join(service, filepath.FromSlash(written)))
		assert.FileExists(t, filepath.Join(service, filepath.FromSlash(written)))
	}
	job, err := os.ReadFile(filepath.Join(service, "src", "job.schema.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(job), "status: Workflow<{")

	// The build's first plan, from the recorded model, is empty.
	writeServiceTSConfig(t, service)
	out, _, err = runMigratePlanIn(t, "", service, "--dialect", "sqlite", "--from", filepath.Join(service, "model.json"), "--format", "json")
	require.NoError(t, err, out)
	var first struct {
		From  string            `json:"from"`
		To    string            `json:"to"`
		Steps []json.RawMessage `json:"steps"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &first), out)
	assert.Empty(t, first.Steps, out)
	assert.Equal(t, plan.To, first.From)
	assert.Equal(t, first.From, first.To)

	_, err = runMigrateCommand(t, "export", "--database-url", db, "--out", service)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exist in")
	_, err = runMigrateCommand(t, "export", "--database-url", db, "--out", service, "--force")
	require.NoError(t, err)

	empty := filepath.Join(dir, "empty.db")
	buildFileByHand(t, runner, empty, "CREATE TABLE note (id TEXT PRIMARY KEY)")
	_, err = runMigrateCommand(t, "export", "--database-url", empty, "--out", filepath.Join(dir, "none"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds no record of runtime mode")
}

// runMigrateCommand runs a migrate subcommand and returns its stdout.
func runMigrateCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(append([]string{"migrate"}, args...))
	err := root.Execute()
	if err != nil {
		t.Logf("migrate %s: %s", strings.Join(args, " "), errOut.String())
	}
	return out.String(), err
}
