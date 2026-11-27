package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// audienceBehaviorsFixture is the DB service whose @audience tables
// compose behaviors (D156, D103): a Task its owner keeps private or shows
// to the principals it names, whose status is a Workflow and which holds a
// conversation, and a Note its author keeps private unless it says
// otherwise, whose summary a deriver writes (D146).
const audienceBehaviorsFixture = "fixture-audience-behaviors-db"

// audienceBehaviorsCheck is the file tsc checks the fixture's package
// against: the Task's audience ids are a list in its row, and the layout
// marks the column that holds them as one.
const audienceBehaviorsCheck = `import type { TaskRow } from './orm';
import { fixtureAudienceBehaviorsDbRules } from './orm';

export const named = (row: TaskRow): readonly string[] | null | undefined => row.audienceNamed;
export const lists: readonly string[] = fixtureAudienceBehaviorsDbRules.types.Task.own.filter(column => column.list).map(column => column.field);
`

// audienceBehaviorsCases are the cases audience_behaviors.test.ts runs:
// on bun:sqlite the adapter over the layout and the generated repositories
// on SQLite, always; on Postgres the generated repository, when the
// database is set.
var audienceBehaviorsCases = []string{
	"the core reads a private row's audience ids as a list of UUIDs, on bun:sqlite",
	"a private row's behaviors answer its owner, and are not found for anyone else, on bun:sqlite",
	"a named row's behaviors answer the principals it names, and are not found for anyone else, on bun:sqlite",
	"a create that names no audience takes the table's defaults, on bun:sqlite",
	"a private row's behaviors answer its owner, and are not found for anyone else, on Postgres",
	"a named row's behaviors answer the principals it names, and are not found for anyone else, on Postgres",
	"a create that names no audience takes the table's defaults, on Postgres",
}

// TestAudienceBehaviorsEndToEnd holds behaviors on an @audience table to
// the caller's reach (D156): it generates fixture-audience-behaviors-db's
// TypeScript package, type-checks it against audienceBehaviorsCheck, and
// runs testdata/orm/audience_behaviors.test.ts in it with bun. On
// bun:sqlite, the adapter over the layout reads a private row, whose
// audience ids are SQLite's JSON text, as the core reads it, and the
// generated repositories on SQLite (orm-sqlite.ts, whose behaviors run
// through the core over sqliteRulesStore, D182) answer as on Postgres
// below. On Postgres,
// over the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, a Workflow transition, a
// Comments post and list, an update and a delete of a private row, and of
// a named one, answer the principals the row is visible to, and are not
// found for anyone else: the row's audience ids, a uuid[], are no longer
// refused as no UUID. On each, a create through the core that names
// neither audience nor agentAccess takes the table's defaults, as Go's
// repositories do (ormgen's TestHeldBehaviorTablesOnPostgres). The
// Postgres cases skip without the database.
func TestAudienceBehaviorsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, audienceBehaviorsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", audienceBehaviorsFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: audienceBehaviorsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", audienceBehaviorsFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, audienceBehaviorsFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "audience_behaviors.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     audienceBehaviorsFixture,
		schema:   schema,
		registry: reg,
		sqlite:   true,
		extra:    map[string]string{"audience_behaviors.check.ts": audienceBehaviorsCheck},
	}})
	dir := filepath.Join(typesRoot, audienceBehaviorsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "audience_behaviors.junit.xml")
	run := exec.Command(bunPath, "test", "./audience_behaviors.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	postgres := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != ""
	if !postgres {
		delete(cases, "(unnamed)")
	}
	for _, name := range audienceBehaviorsCases {
		tc, ok := cases[name]
		onPostgres := strings.HasSuffix(name, ", on Postgres")
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil:
			t.Errorf("%q failed:\n%s", name, junit)
		case tc.Skipped != nil && (!onPostgres || postgres):
			t.Errorf("%q was skipped:\n%s", name, junit)
		}
	}
	if len(cases) != len(audienceBehaviorsCases) {
		t.Errorf("bun ran %d cases, and audienceBehaviorsCases names %d:\n%s", len(cases), len(audienceBehaviorsCases), junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
}
