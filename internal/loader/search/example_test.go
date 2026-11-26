package search_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSearchAcrossTablesExampleLoads: examples/search-across-tables's
// database loads (D155): its three @searchable tables share the search
// index the build adds, and its queue gains the fields the build fills.
func TestSearchAcrossTablesExampleLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "search-across-tables", "schemas", "services", "workspace-db")
	db, err := loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatalf("load workspace-db: %v", err)
	}
	var names []string
	for _, td := range db.SearchableTables() {
		names = append(names, td.Name)
	}
	if want := []string{"Page", "Project", "Task"}; !slices.Equal(names, want) {
		t.Errorf("searchable tables = %v, want %v", names, want)
	}
	if index := db.Types[ir.SearchIndexTypeName]; index == nil || !index.SearchIndex {
		t.Errorf("workspace-db has no search index: %+v", index)
	}
	if q := db.Types["SearchIndexing"]; q == nil || len(q.Fields) != len(ir.SearchIndexingFields) {
		t.Errorf("workspace-db's SearchIndexing is %+v", q)
	}
}
