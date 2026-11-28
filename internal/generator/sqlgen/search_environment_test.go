package sqlgen

import (
	"os"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// The fixtures of one search index per environment (#740): a source
// database, whose index rows its worker copies into the environment's
// index, and the search database, which holds that index.
const (
	searchEnvironmentSourceFixture = "../../loader/tsreader/testdata/services/fixture-search-env-db"
	searchEnvironmentIndexFixture  = "../../loader/tsreader/testdata/services/fixture-search-index-db"
)

func searchCreateSQL(t *testing.T, dir, name string) string {
	t.Helper()
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: name})
	if err != nil {
		t.Fatal(err)
	}
	create, err := renderTemplate("create.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SEARCH_PRINT") != "" {
		t.Logf("%s", create)
	}
	return string(create)
}

// TestASourceOfTheEnvironmentsIndexEnqueuesEveryWrite: a source's
// @searchable tables' triggers enqueue a delete, a soft delete, a change of
// key and every change of a row, its grants table's trigger every write of
// a grant, and its raw SQL makes the sequence its copies are versioned by.
func TestASourceOfTheEnvironmentsIndexEnqueuesEveryWrite(t *testing.T) {
	create := searchCreateSQL(t, searchEnvironmentSourceFixture, "fixture-search-env-db")
	for _, want := range []string{
		"CREATE SEQUENCE IF NOT EXISTS search_environment_version;",
		"  IF TG_OP = 'DELETE' THEN\n    DELETE FROM search_index WHERE source_table = 'Unit' AND source_key = OLD.id::text;\n    INSERT INTO search_indexing_queue (source_table, source_key)\n    VALUES ('Unit', OLD.id::text) ON CONFLICT",
		"  IF NEW.deleted_at IS NOT NULL THEN\n    DELETE FROM search_index WHERE source_table = 'Note' AND source_key = NEW.id::text;\n    IF TG_OP = 'INSERT' OR OLD.deleted_at IS NULL THEN\n      INSERT INTO search_indexing_queue (source_table, source_key)\n      VALUES ('Note', NEW.id::text) ON CONFLICT",
		"  IF TG_OP = 'INSERT'\n     OR ROW(OLD.*) IS DISTINCT FROM ROW(NEW.*) THEN\n    INSERT INTO search_indexing_queue (source_table, source_key)\n    VALUES ('Folder', NEW.id::text) ON CONFLICT",
		"CREATE OR REPLACE FUNCTION grant_search()",
		"-- The grant a write of Grant changed (#740), enqueued for its reach",
		"VALUES ('Grant', NEW.id::text) ON CONFLICT",
		"CREATE TRIGGER trg_grant_search\n  AFTER INSERT OR UPDATE OR DELETE ON \"grant\"",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %s", want)
		}
	}

	// fixture-search-db keeps its own index: its triggers enqueue a change
	// of the indexed text alone, and it has no grants trigger.
	own := searchCreateSQL(t, "../../loader/tsreader/testdata/services/fixture-search-db", "fixture-search-db")
	for _, unwanted := range []string{"ROW(OLD.*) IS DISTINCT FROM ROW(NEW.*)", "trg_grant_search", "search_environment_version"} {
		if strings.Contains(own, unwanted) {
			t.Errorf("fixture-search-db's create.sql holds %s", unwanted)
		}
	}
}

// TestTheSearchDatabaseHoldsTheEnvironmentsIndex: the environment's index,
// unique on its database, table, key and object, its words in the
// configuration that folds diacritics, and its audience and reach GIN
// indexed.
func TestTheSearchDatabaseHoldsTheEnvironmentsIndex(t *testing.T) {
	create := searchCreateSQL(t, searchEnvironmentIndexFixture, "fixture-search-index-db")
	for _, want := range []string{
		"CREATE TABLE environment_search_index (",
		"CREATE UNIQUE INDEX uq_environment_search_index_source ON environment_search_index USING BTREE (\"database\", source_table, source_key, object);",
		"CREATE INDEX idx_environment_search_index_words ON environment_search_index USING GIN (((setweight(to_tsvector('search_unaccent'::regconfig, COALESCE(title, '')), 'A')",
		"CREATE INDEX idx_environment_search_index_audience ON environment_search_index USING GIN (audience);",
		"CREATE INDEX idx_environment_search_index_reach ON environment_search_index USING GIN (reach);",
		"CREATE TEXT SEARCH CONFIGURATION search_unaccent (COPY = pg_catalog.simple);",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %s", want)
		}
	}
	if strings.Index(create, "CREATE TEXT SEARCH CONFIGURATION") > strings.Index(create, "CREATE INDEX idx_environment_search_index_words") {
		t.Errorf("create.sql builds the index before the configuration it is in")
	}
}
