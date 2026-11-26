package sqlgen

import (
	"os"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// searchFixture is fixture-search-db (D155): six @searchable tables, one
// with an audience it inherits, one with a conversation and files, one
// soft-deleting.
const searchFixture = "../../loader/tsreader/testdata/services/fixture-search-db"

// TestSearchableDDL: the search index, with its GIN index over the weighed
// words and its audience; each @searchable table's trigger, which deletes
// an index row with its row, copies an audience into it and enqueues a
// row whose text changed; and the triggers of a conversation's messages
// and of the files, which enqueue the row they belong to (D155).
func TestSearchableDDL(t *testing.T) {
	schema, err := loader.LoadService(searchFixture)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-search-db"})
	if err != nil {
		t.Fatal(err)
	}
	createBytes, err := renderTemplate("create.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	dropBytes, err := renderTemplate("drop.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	create, drop := string(createBytes), string(dropBytes)
	if os.Getenv("SEARCH_PRINT") != "" {
		t.Logf("%s\n----\n%s", create, drop)
	}
	for _, want := range []string{
		"CREATE TABLE search_index (",
		"CREATE UNIQUE INDEX uq_search_index_source ON search_index USING BTREE (source_table, source_key);",
		"CREATE INDEX idx_search_index_words ON search_index USING GIN (((setweight(to_tsvector('simple'::regconfig, COALESCE(title, '')), 'A') || setweight(to_tsvector('simple'::regconfig, COALESCE(body, '')), 'B') || setweight(to_tsvector('simple'::regconfig, COALESCE(comments, '')), 'C') || setweight(to_tsvector('simple'::regconfig, COALESCE(files, '')), 'D'))));",
		"CREATE INDEX idx_search_index_audience ON search_index USING GIN (audience);",
		"CREATE OR REPLACE FUNCTION document_search()",
		"CREATE TRIGGER trg_document_search\n  AFTER INSERT OR UPDATE OR DELETE ON document\n  FOR EACH ROW",
		"DELETE FROM search_index WHERE source_table = 'Document' AND source_key = OLD.id::text;",
		"UPDATE search_index SET audience = NEW.effective_audience, agents = NEW.effective_agents",
		"IF NEW.deleted_at IS NOT NULL THEN\n    DELETE FROM search_index WHERE source_table = 'Note' AND source_key = NEW.id::text;",
		"ROW(OLD.title, OLD.details, OLD.resolution, OLD.unit_id) IS DISTINCT FROM ROW(NEW.title, NEW.details, NEW.resolution, NEW.unit_id)",
		"INSERT INTO search_indexing_queue (source_table, source_key)\n    VALUES ('Request', NEW.id::text) ON CONFLICT",
		"CREATE TRIGGER trg_unit_attachments_search",
		"CREATE TRIGGER trg_unit_comments_messages_search",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %s", want)
		}
	}
	if strings.Contains(create, "terms") && strings.Contains(create, "OLD.terms") {
		t.Errorf("a trigger watches the encrypted terms")
	}
	for _, want := range []string{
		"DROP TRIGGER IF EXISTS trg_document_search ON document;",
		"DROP FUNCTION IF EXISTS unit_attachments_search();",
	} {
		if !strings.Contains(drop, want) {
			t.Errorf("drop.sql lacks %s", want)
		}
	}
}
