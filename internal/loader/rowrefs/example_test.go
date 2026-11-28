package rowrefs_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestReferencesFilesExampleLoads: examples/references-files's database
// loads (D154): its @references and @attachments tables each gain their
// sub-table, and its queues the fields the build fills.
func TestReferencesFilesExampleLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "references-files", "schemas", "services", "issues-db")
	db, err := loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatalf("load issues-db: %v", err)
	}
	for _, table := range []string{"Issue", "Note"} {
		for _, sub := range []string{ir.RowReferencesTypeName(table), ir.AttachmentsTypeName(table)} {
			if td := db.Types[sub]; td == nil || td.RowTable == nil || td.RowTable.Row != table {
				t.Errorf("issues-db has no sub-table %s of %s", sub, table)
			}
		}
	}
	if got := db.Types["Note"].RowReferences.Tables; len(got) != 2 {
		t.Errorf("Note's references point at %v, want Issue and Note", got)
	}
	for _, queue := range []string{"ReferenceCleared", "FileReleased"} {
		if td := db.Types[queue]; td == nil || td.Queue == nil || len(td.Fields) == 0 {
			t.Errorf("issues-db's %s is %+v", queue, td)
		}
	}

	// issues-api serves the issue's conversation and the references,
	// backlinks and files of issues and notes as generated operations
	// (#723), the files in its one bucket.
	api, err := loader.LoadService(filepath.Join(dir, "..", "issues-api"), loader.WithRegistry(registry.New(naming.Default())), loader.WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil }))
	if err != nil {
		t.Fatalf("load issues-api: %v", err)
	}
	served := map[string][]string{}
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.RowFeatureOperation; marker != nil {
				served[set.Name] = append(served[set.Name], op.Name)
				if ir.RowFeatureOperationTakesBucket(op.Name) && marker.Bucket != "issues-files" {
					t.Errorf("%s.%s reads the bucket %q, want issues-files", set.Name, op.Name, marker.Bucket)
				}
			}
		}
	}
	if len(served["IssueOperations"]) != len(ir.RowFeatureOperationNames) || len(served["NoteOperations"]) != len(ir.RowFeatureOperationNames)-1 {
		t.Errorf("issues-api serves %v", served)
	}
}
