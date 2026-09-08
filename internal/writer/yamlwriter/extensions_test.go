package yamlwriter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

func extensionsDoc() *schemafile.Document {
	return &schemafile.Document{
		Name:       "fixture-db",
		Kind:       ir.SchemaKindDB,
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)},
		Documents:  map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"names":["a","b"],"shelves":3}`)},
		Types: map[string]*ir.TypeDef{
			"Item": {
				Name:       "Item",
				Role:       ir.RoleDBTable,
				Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"tagged":true}`)},
				Fields: []*ir.FieldDef{{
					Name:       "slug",
					TypeRef:    ir.TypeRef{Name: "Identity.Slug"},
					Unique:     true,
					Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"shelf":{"aisle":3}}`)},
				}},
			},
		},
		OperationSets: []*ir.OperationSet{{
			Name:       "ItemOps",
			Operations: []*ir.FieldDef{},
			Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"audited":true}`)},
		}},
	}
}

// Extension and document values are JSON objects held as json.RawMessage;
// the YAML writer must emit them as YAML mappings, not as the !!int
// sequences yaml.v3 produces for a []byte.
func TestWriteExtensionsAsMappings(t *testing.T) {
	doc := extensionsDoc()
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(string(out), "!!binary") {
		t.Fatalf("writer emitted binary scalars:\n%s", out)
	}
	for _, want := range []string{"aisle: 3", "catalog: true", "shelves: 3", "audited: true", "tagged: true"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got := readBack(t, out)
	requireEqualDocs(t, doc, got, out)
}

// Values inside an extension that YAML would re-type when written plain
// (strings that look like booleans, numbers or null, strings with colons
// and quotes) must read back as the same JSON. Nested arrays keep their
// order; numbers, booleans and null keep their type.
func TestWriteExtensionsRoundTripsTrickyValues(t *testing.T) {
	value := `{"meta":{"arr":[3,1,{"y":1,"z":2},[]],"bools":[true,false],"colon":"a: b","empty":"","float":1.5,"int":-7,"neg":"-1","null":null,"num":"1","obj":{"nested":{"deep":"x"}},"on":"on","quote":"she said \"hi\"","yes":"yes"}}`
	doc := &schemafile.Document{
		Name: "fixture-db",
		Kind: ir.SchemaKindDB,
		Types: map[string]*ir.TypeDef{
			"Item": {
				Name:       "Item",
				Role:       ir.RoleDBTable,
				Extensions: map[string]json.RawMessage{"acme": json.RawMessage(value)},
			},
		},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readBack(t, out)
	if gotValue := string(got.Types["Item"].Extensions["acme"]); gotValue != value {
		t.Errorf("extension changed across the YAML round trip\nwant: %s\ngot:  %s\noutput:\n%s", value, gotValue, out)
	}
}

// A one-type document with root extensions or documents is written in the
// document form: the single-definition form has nowhere to put them.
func TestWriteRootSlotsNeedTheDocumentForm(t *testing.T) {
	doc := &schemafile.Document{
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)},
		Documents:  map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{}`)},
		Types:      map[string]*ir.TypeDef{"Item": {Name: "Item", Role: ir.RoleDBTable}},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readBack(t, out)
	requireEqualDocs(t, doc, got, out)
}
