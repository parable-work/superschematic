package sqlmigrate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// unfolded is fixture-search-db's schema as it is without
// outputs.sql.search.foldDiacritics: its index's words in simple, and no
// raw SQL of the folding configuration.
func unfolded(s *ir.Schema) {
	index := s.Types[ir.SearchIndexTypeName]
	index.SearchFolds = false
	index.RawSQL = nil
	for i := range index.Indexes {
		if index.Indexes[i].Name == "words" {
			index.Indexes[i].Expression = ir.SearchDocument("")
		}
	}
}

// TestFoldingTheSearchIndexMigrates (#740): turning foldDiacritics on in
// a database that searches already makes the configuration before the
// index over it is built again in it, on Postgres, and fills the FTS5
// table again with the folding tokenizer, on SQLite; turning it off builds
// the index in simple again before the configuration is dropped.
func TestFoldingTheSearchIndexMigrates(t *testing.T) {
	dir := filepath.Join(sqlgenFixtures, "fixture-search-db")
	model := func(t *testing.T, dialect Dialect, change func(*ir.Schema)) *Model {
		t.Helper()
		schema, err := loader.LoadService(dir)
		if err != nil {
			t.Fatal(err)
		}
		if schema.SearchConfig() != ir.SearchFoldedConfig {
			t.Fatalf("fixture-search-db's index is in %s, want it folding", schema.SearchConfig())
		}
		if change != nil {
			change(schema)
		}
		m, err := BuildModel(schema, sqlgen.Options{SchemaName: "fixture-search-db"}, dialect)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// at is the place of the first step whose subject holds subject and
	// whose statements hold text, -1 for none.
	at := func(plan *Plan, subject, text string) int {
		for i, step := range plan.Steps {
			if strings.Contains(step.Subject, subject) && strings.Contains(strings.Join(step.Statements, "\n"), text) {
				return i
			}
		}
		return -1
	}

	t.Run("postgres", func(t *testing.T) {
		off, on := model(t, Postgres, unfolded), model(t, Postgres, nil)
		up, err := Diff(off, on, Options{})
		if err != nil {
			t.Fatal(err)
		}
		config := at(up, "rawSql/"+ir.SearchUnaccentPiece, "CREATE TEXT SEARCH CONFIGURATION search_unaccent")
		index := at(up, "search_index_words", "'search_unaccent'::regconfig")
		if config < 0 || index < 0 || config > index {
			t.Fatalf("the configuration is made at step %d and the index over it at %d:\n%s", config, index, up.Markdown())
		}
		down, err := Diff(on, off, Options{})
		if err != nil {
			t.Fatal(err)
		}
		simple := at(down, "search_index_words", "'simple'::regconfig")
		dropped := at(down, "rawSql/"+ir.SearchUnaccentPiece, "DROP TEXT SEARCH CONFIGURATION IF EXISTS search_unaccent")
		if simple < 0 || dropped < 0 || simple > dropped {
			t.Fatalf("the index is built in simple at step %d and the configuration dropped at %d:\n%s", simple, dropped, down.Markdown())
		}
	})

	t.Run("sqlite", func(t *testing.T) {
		off, on := model(t, SQLite, unfolded), model(t, SQLite, nil)
		up, err := Diff(off, on, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if at(up, "rawSql/"+SearchFTSPiece, "remove_diacritics 2") < 0 || at(up, "rawSql/"+SearchFTSPiece, `INSERT INTO "search_index_fts"`) < 0 {
			t.Fatalf("the FTS5 table is not made again folding and filled:\n%s", up.Markdown())
		}
	})
}
