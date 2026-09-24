package writer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// Root-level extensions and documents have no file attribution in the IR;
// SplitSchema places them on the first document, next to the schema
// description and comment.
func TestSplitSchemaCarriesRootExtensionsAndDocuments(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindDB)
	schema.Extensions = map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)}
	schema.Documents = map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":3}`)}
	schema.Types["Item"] = &ir.TypeDef{
		Name:       "Item",
		Role:       ir.RoleDBTable,
		Owner:      "src/item.schema.json",
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"tagged":true}`)},
	}
	schema.Types["Shelf"] = &ir.TypeDef{Name: "Shelf", Role: ir.RoleDBTable, Owner: "src/shelf.schema.json"}

	docs := SplitSchema(schema)
	if len(docs) != 2 {
		t.Fatalf("documents = %v", sortedKeys(docs))
	}
	first := docs["src/item.schema"]
	if string(first.Extensions["acme"]) != `{"catalog":true}` {
		t.Errorf("first document extensions = %v", first.Extensions)
	}
	if string(first.Documents["catalogConfig"]) != `{"shelves":3}` {
		t.Errorf("first document documents = %v", first.Documents)
	}
	if string(first.Types["Item"].Extensions["acme"]) != `{"tagged":true}` {
		t.Errorf("type extensions lost: %v", first.Types["Item"].Extensions)
	}
	second := docs["src/shelf.schema"]
	if second.Extensions != nil || second.Documents != nil {
		t.Errorf("second document restates root data: %v %v", second.Extensions, second.Documents)
	}
}

// Extension slots have no form the TypeScript writer can render: the
// extension's decorators live in its own authoring package. Writing TS
// fails and names the slot instead of dropping it; JSON and YAML keep it.
func TestTSWriterRefusesExtensionData(t *testing.T) {
	acme := map[string]json.RawMessage{"acme": json.RawMessage(`{"tagged":true}`)}
	for name, doc := range map[string]*schemafile.Document{
		"schema-level extension data": {Name: "fixture", Kind: ir.SchemaKindGeneral, Extensions: acme},
		"schema-level extension data and documents": {Name: "fixture", Kind: ir.SchemaKindGeneral,
			Documents: map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":3}`)}},
		"type Item: extension data (acme)": {Name: "fixture", Kind: ir.SchemaKindGeneral,
			Types: map[string]*ir.TypeDef{"Item": {Name: "Item", Role: ir.RoleEmbeddedStruct, Extensions: acme}}},
		"operation set Items: extension data (acme)": {Name: "fixture", Kind: ir.SchemaKindAPI,
			OperationSets: []*ir.OperationSet{{Name: "Items", Extensions: acme}}},
	} {
		if _, err := Write(doc, FormatTS); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: Write(ts) = %v", name, err)
		}
		if _, err := Write(doc, FormatYAML); err != nil {
			t.Errorf("%s: Write(yaml) = %v", name, err)
		}
	}
}
