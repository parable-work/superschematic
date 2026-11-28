package tswriter

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite testdata/tables")

// developmentDocuments holds development mode's General documents of the
// engine examples, copied into migrateplan's testdata (D117).
const developmentDocuments = "../../migrateplan/testdata/development"

func readDevelopment(t *testing.T, name string) *schemafile.Document {
	t.Helper()
	path := filepath.Join(developmentDocuments, name+".schema.json")
	doc, err := jsonreader.ReadFileWith(path, filepath.Base(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestTablesWritesTheTableFormOfAGeneralSchema: with Tables, as format
// --to=ts writes a file, development mode's General documents are written
// as the tables they publish as (D117): a key the database makes, the
// Workflow as the field status: Workflow<{ ... }>, the other behaviors as
// ones the class implements, and descriptions as comments. Without it the
// type keeps @behavior, as a General service holds it.
func TestTablesWritesTheTableFormOfAGeneralSchema(t *testing.T) {
	for _, name := range []string{"notes", "batches", "jobs", "workers"} {
		doc := readDevelopment(t, name)
		got, err := WriteContext(doc, &Context{Tables: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		golden := filepath.Join("testdata", "tables", name+".schema.ts")
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: the table form differs from %s (run with -update)\ngot:\n%s", name, golden, got)
		}
		if strings.Contains(string(got), "@behavior") {
			t.Errorf("%s: the table form writes @behavior:\n%s", name, got)
		}
	}

	doc := readDevelopment(t, "notes")
	described := doc.Description
	kept, err := WriteContext(doc, nil)
	if err == nil || !strings.Contains(err.Error(), "schema-level comments and descriptions have no TypeScript form") {
		t.Fatalf("a General document written whole keeps its description, which TypeScript has no form of: %v\n%s", err, kept)
	}
	doc.Description = ""
	note := *doc.Types["Note"]
	note.Fields = nil
	doc.Types["Note"] = &note
	kept, err = WriteContext(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), `@behavior("Comments")`) || strings.Contains(string(kept), "implements") {
		t.Errorf("without Tables a General type keeps @behavior:\n%s", kept)
	}
	if doc.Description != "" || readDevelopment(t, "notes").Description != described {
		t.Errorf("the writer changed the document it was given")
	}
}

// TestTablesRefusesAFieldTheWorkflowLowersTo: a type with a field status
// cannot take its Workflow as status: Workflow<{ ... }>, so the writer
// refuses it, naming the field (D117).
func TestTablesRefusesAFieldTheWorkflowLowersTo(t *testing.T) {
	doc := &schemafile.Document{Name: "tickets", Kind: ir.SchemaKindGeneral, Types: map[string]*ir.TypeDef{
		"Ticket": {
			Name: "Ticket", Role: ir.RoleEmbeddedStruct,
			Behaviors: []ir.BehaviorRef{{Name: "Workflow"}},
			Fields:    []*ir.FieldDef{{Name: "status", TypeRef: ir.TypeRef{Name: "string"}}},
		},
	}}
	_, err := WriteContext(doc, &Context{Tables: true})
	want := "type Ticket has a field status, and its Workflow lowers to the field form status: Workflow<{ ... }>"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("WriteContext = %v, want %q", err, want)
	}
}
