package writer

import (
	"encoding/json"
	"testing"

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
