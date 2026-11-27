package migrateplan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// WriteExportTSConfig writes the tsconfig.json a service directory Export
// wrote needs to compile here: the authoring packages resolve to the
// repository's, as a schemas root's base config resolves them.
func writeExportTSConfig(t *testing.T, dir string) {
	t.Helper()
	packages, err := filepath.Abs("../../packages")
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

// TestExportWritesTheCompiledServiceOfARuntimeDatabase: a runtime
// database's record exports as a compiled DB service (D117): its config,
// a TypeScript schema file per document in the table form, and the
// recorded model; reading the TypeScript back resolves to the recorded
// model, so the build's first plan from it is empty.
func TestExportWritesTheCompiledServiceOfARuntimeDatabase(t *testing.T) {
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.SQLite, sqlmigrate.Postgres} {
		t.Run(string(dialect), func(t *testing.T) {
			sources := developmentSources(t, "batches", "jobs", "workers")
			_, model, err := Documents(sources, dialect, Env{Service: "jobs-db"})
			require.NoError(t, err)
			recorded := &Recorded{Service: "jobs-db", Model: model}
			for _, source := range sources {
				recorded.Documents = append(recorded.Documents, source.Data)
			}
			files, err := Export(recorded, Env{})
			require.NoError(t, err)
			var names []string
			for name := range files {
				names = append(names, name)
			}
			sort.Strings(names)
			assert.Equal(t, []string{"model.json", "schema.config.ts", "src/batch.schema.ts", "src/job.schema.ts", "src/worker.schema.ts"}, names)
			config := string(files["schema.config.ts"])
			assert.Contains(t, config, `name: "jobs-db",`)
			assert.Contains(t, config, "kind: SchemaKind.DB,")
			assert.Equal(t, dialect == sqlmigrate.SQLite, strings.Contains(config, `sql: { dialects: ["postgres", "sqlite"] }`), config)
			job := string(files["src/job.schema.ts"])
			assert.Contains(t, job, "export abstract class Job implements Lease<Job,")
			assert.Contains(t, job, "  status: Workflow<{")
			assert.Contains(t, job, `import { Batch } from "./batch.schema";`)
			assert.NotContains(t, job, "@behavior")
			read, err := ReadModel(files["model.json"])
			require.NoError(t, err)
			assert.Equal(t, hashOf(model), hashOf(read))

			dir := t.TempDir()
			for name, data := range files {
				path := filepath.Join(dir, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, data, 0o644))
			}
			writeExportTSConfig(t, dir)
			built, err := loader.LoadService(dir)
			require.NoError(t, err)
			again, err := Model(built, dialect, Env{})
			require.NoError(t, err)
			if !assert.Equal(t, hashOf(model), hashOf(again), "the TypeScript resolves to the recorded model") {
				plan, err := sqlmigrate.Diff(model, again, sqlmigrate.Options{})
				require.NoError(t, err)
				for _, step := range plan.Steps {
					t.Logf("the build plans %s %s", step.Op, step.Subject)
				}
			}
		})
	}
}

// TestExportRefusesARecordThisCompilerPlansOtherwise: the recorded model
// must be the one the documents resolve to, of the record's service.
func TestExportRefusesARecordThisCompilerPlansOtherwise(t *testing.T) {
	sources := developmentSources(t, "notes")
	_, model, err := Documents(sources, sqlmigrate.SQLite, Env{})
	require.NoError(t, err)
	other := *model
	other.Tables = other.Tables[1:]
	_, err = Export(&Recorded{Service: "notes", Documents: []json.RawMessage{sources[0].Data}, Model: &other}, Env{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this compiler plans them otherwise than the one that published them")

	_, err = Export(&Recorded{Service: "tasks-db", Documents: []json.RawMessage{sources[0].Data}, Model: model}, Env{})
	require.Error(t, err)
	assert.Equal(t, "the model is notes's, not tasks-db's", err.Error())
}

// TestKebab names a document's file after its type.
func TestKebab(t *testing.T) {
	for name, want := range map[string]string{"Note": "note", "OrderLine": "order-line", "HTTPRoute": "http-route", "Line2Item": "line2-item", "shop-db": "shop-db"} {
		assert.Equal(t, want, kebab(name), name)
	}
}
