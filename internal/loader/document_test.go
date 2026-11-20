package loader

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/jsonreader"
)

// shelfDocuments are the documents of temp-db's two schema files, as
// format --to=json writes them: Shelf, @optimistic with a fence that
// covers nothing, in one, and Bin, which refers to it, in the other.
var shelfDocuments = []string{
	`{
		"name": "temp-db", "kind": "DB",
		"scalars": {"Generic.Int64": {"name": "Generic.Int64", "languagePrimitive": "number"}},
		"types": {"Shelf": {"name": "Shelf", "role": "DBTable", "optimistic": true, "fences": [{"field": "revision"}], "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "revision", "typeRef": {"name": "Generic.Int64"}, "required": true, "default": "0"}
		]}}
	}`,
	`{
		"name": "temp-db", "kind": "DB",
		"types": {"Bin": {"name": "Bin", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "shelf", "typeRef": {"name": "Shelf"}, "required": true, "relation": {"type": "Shelf"}}
		]}}
	}`,
}

func sourcedDocuments(t *testing.T, docs ...string) []SourcedDocument {
	t.Helper()
	var out []SourcedDocument
	for i, data := range docs {
		source := []string{"shelf.schema.json", "bin.schema.json"}[i]
		doc, err := jsonreader.Read([]byte(data), source)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, SourcedDocument{Source: source, Document: doc})
	}
	return out
}

// TestLoadDocumentsMergesOneSchemasFiles: the documents of one schema's
// files merge into it, a type of one file naming a type of another, as a
// service load merges its data files (D117).
func TestLoadDocumentsMergesOneSchemasFiles(t *testing.T) {
	schema, err := LoadDocuments(sourcedDocuments(t, shelfDocuments...))
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != "temp-db" || schema.Types["Shelf"] == nil || schema.Types["Bin"] == nil {
		t.Fatalf("schema = %s, types %v", schema.Name, schema.Types)
	}
	if owner := schema.Types["Bin"].Owner; owner != "bin.schema.json" {
		t.Errorf("Bin's owner = %q", owner)
	}

	docs := sourcedDocuments(t, shelfDocuments...)
	docs[1].Document.Name = "other-db"
	_, err = LoadDocuments(docs)
	if want := "shelf.schema.json is a document of temp-db, and bin.schema.json of other-db: the documents are one schema's"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	if _, err := LoadDocuments(nil); err == nil {
		t.Error("no documents loaded")
	}
}

// TestLoadDocumentsChecksTheDialects: a document has no config to read
// outputs.sql.dialects from, so the caller names the dialect, and its
// rules hold: the fence of an @optimistic table that covers nothing is
// refused on SQLite (D119).
func TestLoadDocumentsChecksTheDialects(t *testing.T) {
	for _, dialects := range [][]string{nil, {"postgres"}} {
		if _, err := LoadDocuments(sourcedDocuments(t, shelfDocuments...), WithDialects(dialects...)); err != nil {
			t.Errorf("%v: %v", dialects, err)
		}
	}
	_, err := LoadDocuments(sourcedDocuments(t, shelfDocuments...), WithDialects("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "Shelf is @optimistic and temp-db lists the sqlite dialect") {
		t.Errorf("err = %v", err)
	}
}
