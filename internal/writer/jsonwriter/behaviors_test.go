package jsonwriter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// The JSON writer indents a behavior's config with the rest of the file;
// the reader stores it canonically again, so the document is unchanged.
func TestWriteBehaviorsRoundTrip(t *testing.T) {
	doc := &schemafile.Document{
		Types: map[string]*ir.TypeDef{
			"Item": {
				Name:       "Item",
				Role:       ir.RoleEmbeddedStruct,
				Implements: []ir.TraitRef{{Name: "Stamped"}},
				Behaviors: []ir.BehaviorRef{
					{Name: "acme.Stock", Config: json.RawMessage(`{"aisles":3,"unit":"box"}`)},
					{Name: "acme.Audited"},
				},
			},
		},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := `"behaviors": [
    {
      "name": "acme.Stock",
      "config": {
        "aisles": 3,
        "unit": "box"
      }
    },
    {
      "name": "acme.Audited"
    }
  ]`
	if !strings.Contains(string(out), want) {
		t.Errorf("output lacks\n%s\n%s", want, out)
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
