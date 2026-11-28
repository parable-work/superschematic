package loader

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheEnvironmentsSearchIndexExpands (#740):
// outputs.sql.search.environmentIndex adds the environment's index to a
// database, whether it has a @searchable table or not, and
// outputs.sql.search.environment marks a source's index and gives it the
// sequence its copies are versioned by; each is refused on a database
// that lists sqlite, and environment on one with no @searchable table.
func TestTheEnvironmentsSearchIndexExpands(t *testing.T) {
	const searchable = `
import { Identity } from "superscalar";
import { AutoGenerate, key, queue, searchable } from "@superschematic/db";
@queue()
export abstract class Indexing {}
@searchable<Ticket>({ title: "title", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`
	const plain = `
import { Identity } from "superscalar";
import { AutoGenerate, key } from "@superschematic/db";
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`
	base := filepath.ToSlash(filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
	load := func(search, dialects, schema string) (*ir.Schema, error) {
		return LoadService(writeService(t, map[string]string{
			"schema.config.json":    `{"name": "tickets-db", "kind": "DB", "outputs": {"sql": {"dialects": [` + dialects + `], "search": ` + search + `}}}`,
			"tsconfig.json":         `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
			"src/tickets.schema.ts": schema,
		}))
	}

	schema, err := load(`{"environmentIndex": true}`, `"postgres"`, plain)
	if err != nil {
		t.Fatal(err)
	}
	index := schema.EnvironmentSearchIndex()
	if index == nil || schema.Types[ir.SearchIndexTypeName] != nil {
		t.Fatalf("a database with environmentIndex and no @searchable table has %v and its own index %v", index, schema.Types[ir.SearchIndexTypeName])
	}
	if index.Indexes[0].Unique != true || strings.Join(index.Indexes[0].Keys, ",") != "database,sourceTable,sourceKey,object" {
		t.Errorf("the environment's index is unique on %v", index.Indexes[0].Keys)
	}

	schema, err = load(`{"environment": true}`, `"postgres"`, searchable)
	if err != nil {
		t.Fatal(err)
	}
	own := schema.Types[ir.SearchIndexTypeName]
	if !schema.SearchesEnvironment() || schema.EnvironmentSearchIndex() != nil {
		t.Fatalf("a source searches its environment: %v, and holds its index: %v", schema.SearchesEnvironment(), schema.EnvironmentSearchIndex())
	}
	if len(own.RawSQL) != 1 || own.RawSQL[0].ID != ir.SearchEnvironmentPiece || !strings.Contains(own.RawSQL[0].Up, "CREATE SEQUENCE IF NOT EXISTS "+ir.SearchEnvironmentSequence) {
		t.Errorf("the source's index has the raw SQL %+v", own.RawSQL)
	}

	for _, tc := range []struct{ search, dialects, schema, want string }{
		{`{"environment": true}`, `"postgres", "sqlite"`, searchable, "outputs.sql.search.environment: tickets-db lists sqlite, and an environment's search index is Postgres's alone (#740)"},
		{`{"environmentIndex": true}`, `"postgres", "sqlite"`, plain, "outputs.sql.search.environmentIndex: tickets-db lists sqlite"},
		{`{"environment": true}`, `"postgres"`, plain, "outputs.sql.search.environment: tickets-db has no @searchable table"},
	} {
		if _, err := load(tc.search, tc.dialects, tc.schema); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("with %s on %s: LoadService = %v, want it to hold %q", tc.search, tc.dialects, err, tc.want)
		}
	}
}
