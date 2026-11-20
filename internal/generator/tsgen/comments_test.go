package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// commentsFixture is the DB service whose tables implement Comments (D77,
// D103's step 4): a Ticket a caller holding tickets.moderate moderates,
// and a Review of refs and code blocks.
const commentsFixture = "fixture-comments-db"

// commentsCheck is the file tsc checks the fixture's repositories against:
// Comments' operations under comments, taking the row's key, post its
// body and react a message and an emoji; a table whose behaviors keep
// sub-tables has no upserts; and the sub-tables and the shared table in
// the layout.
const commentsCheck = `import type { FixtureCommentsDbOrm, TicketRow } from './orm';
import { fixtureCommentsDbRules } from './orm';

export async function talk(orm: FixtureCommentsDbOrm, id: TicketRow['id']): Promise<unknown> {
  await orm.ticket.comments.post(id, { body: 'Seen on Safari.' });
  await orm.ticket.comments.react(id, { message: 1, emoji: 'eyes' });
  return orm.review.comments.listMessages(id, {});
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureCommentsDbOrm) => orm.ticket.upsertOne;
export const tables: readonly string[] = fixtureCommentsDbRules.types.Ticket.uses[0]!.tables!.map(table => table.name);
export const shared: readonly string[] = fixtureCommentsDbRules.types.Ticket.uses[0]!.shared!.map(table => table.name);
`

// commentsCases are the cases comments.test.ts runs on each dialect, by
// name without the dialect's suffix.
var commentsCases = []string{
	"a post is a row of the use's sub-table and moves no version of the row",
	"a reply and a reaction show on the thread's record",
	"another's edit is refused unless its caller moderates, whose edit keeps what it replaced",
}

// TestCommentsTablesEndToEnd holds the generator's layout and Comments'
// sub-tables and shared table to the HTTP runtime's adapter (D103's step
// 4). It loads fixture-comments-db through the TypeScript reader,
// generates its TypeScript package with the core's registry, type-checks
// it against commentsCheck, and runs testdata/orm/comments.test.ts in it
// with bun on each dialect: on bun:sqlite, over the SQLite create.sql D27
// plans, the adapter run on the layout the generated orm.ts embeds; on
// Postgres, over the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, whose
// writes of a conversation move no version of the ticket. Every case must
// pass on bun:sqlite, and on Postgres when the database is set; it skips
// without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must
// run.
func TestCommentsTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, commentsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commentsFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: commentsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", commentsFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, commentsFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "comments.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     commentsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"comments.check.ts": commentsCheck},
	}})
	dir := filepath.Join(typesRoot, commentsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "comments.junit.xml")
	run := exec.Command(bunPath, "test", "./comments.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	for _, dialect := range []string{"bun:sqlite", "Postgres"} {
		for _, name := range commentsCases {
			name += ", on " + dialect
			tc, ok := cases[name]
			switch {
			case !ok:
				t.Fatalf("bun did not run %q:\n%s", name, junit)
			case tc.Failure != nil:
				t.Fatalf("%q failed:\n%s", name, junit)
			case tc.Skipped != nil && (dialect != "Postgres" || postgres):
				t.Fatalf("%q was skipped:\n%s", name, junit)
			}
		}
	}
	if want := 2 * len(commentsCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of comments.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
