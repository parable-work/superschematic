package yamlwriter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/yamlreader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A behavior's config is a json.RawMessage; the YAML writer must write it
// as the YAML value it holds, and the reader must give back the same
// canonical bytes, whatever values YAML would re-type when written plain.
func TestWriteBehaviorConfigsRoundTrip(t *testing.T) {
	reg := acmeRegistry(t)
	// An open config schema, so the tricky values below are a valid config.
	if err := reg.RegisterBehavior(registry.BehaviorSpec{Declaration: json.RawMessage(`{"name":"Labelled","configSchema":{"type":"object"}}`)}); err != nil {
		t.Fatal(err)
	}
	tricky := `{"arr":[3,1,{"y":1,"z":2},[]],"colon":"a: b","empty":"","float":1.5,"int":-7,"null":null,"num":"1","on":"on","yes":"yes"}`
	doc := &schemafile.Document{
		Types: map[string]*ir.TypeDef{
			"Item": {
				Name: "Item",
				Role: ir.RoleEmbeddedStruct,
				Behaviors: []ir.BehaviorRef{
					{Name: "acme.Stock", Config: json.RawMessage(`{"aisles":3,"unit":"box"}`)},
					{Name: "acme.Audited"},
					{Name: "Labelled", Config: json.RawMessage(tricky)},
				},
			},
		},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(string(out), "!!binary") || strings.Contains(string(out), "- 123") {
		t.Fatalf("writer emitted a config as bytes:\n%s", out)
	}
	for _, want := range []string{"behaviors:\n  - name: acme.Stock\n    config:\n      aisles: 3\n      unit: box\n  - name: acme.Audited\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got, err := yamlreader.ReadWith(out, "writer-output", reg)
	if err != nil {
		t.Fatalf("writer output does not read back: %v\n%s", err, out)
	}
	requireEqualDocs(t, doc, got, out)
	if config := string(got.Types["Item"].Behaviors[2].Config); config != tricky {
		t.Errorf("config changed across the YAML round trip\nwant: %s\ngot:  %s\noutput:\n%s", tricky, config, out)
	}
}
