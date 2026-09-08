package jsonwriter

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

func TestWriteExtensionsRoundTrip(t *testing.T) {
	doc := &schemafile.Document{
		Name:       "fixture-db",
		Kind:       ir.SchemaKindDB,
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)},
		Documents:  map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":3}`)},
		Types: map[string]*ir.TypeDef{
			"Item": {
				Name:       "Item",
				Role:       ir.RoleDBTable,
				Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"tagged":true}`)},
				Fields: []*ir.FieldDef{{
					Name:       "slug",
					TypeRef:    ir.TypeRef{Name: "Identity.Slug"},
					Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"shelf":{"aisle":3}}`)},
				}},
			},
		},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := jsonreader.ReadWith(out, "writer-output", acmeRegistry(t))
	if err != nil {
		t.Fatalf("output does not read back: %v\n%s", err, out)
	}
	wantJSON, _ := json.Marshal(doc)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("document mismatch\nwant: %s\ngot:  %s\noutput:\n%s", wantJSON, gotJSON, out)
	}
}
