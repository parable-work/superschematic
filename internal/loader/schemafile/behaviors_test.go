package schemafile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

const ratingConfigSchema = `{"type":"object","required":["maxStars"],"additionalProperties":false,"properties":{"maxStars":{"type":"integer","minimum":3,"maximum":10}}}`

// behaviorRegistry is the core registry plus two behaviors of an "acme"
// extension: acme.Rating takes a required config, acme.Flag takes none.
func behaviorRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	for _, decl := range []string{
		`{"name":"acme.Rating","configSchema":` + ratingConfigSchema + `}`,
		`{"name":"acme.Flag"}`,
	} {
		if err := reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: json.RawMessage(decl)}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func typeWithBehaviors(behaviors string) []byte {
	return []byte(`{"name": "Product", "role": "EmbeddedStruct", "behaviors": ` + behaviors + `}`)
}

// DefinitionFor closes BehaviorRef to the registered behaviors: the name is
// one of them, and each behavior's config is held to its config schema, as
// a decorator's argument is held to its Args.
func TestDefinitionForComposesBehaviors(t *testing.T) {
	reg := behaviorRegistry(t)
	data, err := DefinitionFor(reg)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var def any
	if err := json.Unmarshal(root.Defs["BehaviorRef"], &def); err != nil {
		t.Fatal(err)
	}
	compact, _ := json.Marshal(def)
	for _, want := range []string{
		`"name":{"enum":["Assignment","Comments","Dependencies","Lease","Links","Queue","Reactions","Revisions","Rollups","Search","Workflow","acme.Flag","acme.Rating"],"type":"string"}`,
		`{"if":{"properties":{"name":{"const":"acme.Flag"}}},"then":{"properties":{"config":{"additionalProperties":false,"type":"object"}}}}`,
		`{"if":{"properties":{"name":{"const":"acme.Rating"}}},"then":{"properties":{"config":{"additionalProperties":false,"properties":{"maxStars":{"maximum":10,"minimum":3,"type":"integer"}},"required":["maxStars"],"type":"object"}},"required":["config"]}}`,
	} {
		if !strings.Contains(string(compact), want) {
			t.Errorf("BehaviorRef = %s\nwant it to contain %s", compact, want)
		}
	}

	// The composed schema itself, without the readers' named checks, is
	// what an editor validates against.
	for _, test := range []struct {
		behaviors string
		ok        bool
	}{
		{`[{"name": "acme.Rating", "config": {"maxStars": 5}}, {"name": "acme.Flag"}]`, true},
		{`[{"name": "acme.Flag", "config": {}}]`, true},
		{`[{"name": "acme.Ghost"}]`, false},
		{`[{"name": "acme.Rating", "config": {"maxStars": 2}}]`, false},
		{`[{"name": "acme.Rating"}]`, false},
		{`[{"name": "acme.Flag", "config": {"on": true}}]`, false},
		{`[{"name": "acme.Flag", "extra": 1}]`, false},
	} {
		err := validateAgainstDef(reg, typeWithBehaviors(test.behaviors), "TypeDef", "product.schema.json")
		if (err == nil) != test.ok {
			t.Errorf("%s: err = %v, want ok = %v", test.behaviors, err, test.ok)
		}
	}
	// The core's own schema admits the core's behaviors, each config held
	// to its declaration, and no extension's.
	for _, test := range []struct {
		behaviors string
		ok        bool
	}{
		{`[{"name": "Workflow", "config": {"states": ["open", "done"], "transitions": [{"from": "open", "to": "done"}]}}, {"name": "Comments"}, {"name": "Revisions"}]`, true},
		{`[{"name": "Revisions", "config": {"review": {"permission": "documents.review"}}}]`, true},
		{`[{"name": "Workflow"}]`, false},
		{`[{"name": "Workflow", "config": {"states": [], "transitions": []}}]`, false},
		{`[{"name": "Comments", "config": {"maxLength": 10}}]`, false},
		{`[{"name": "acme.Flag"}]`, false},
	} {
		err := validateAgainstDef(core(), typeWithBehaviors(test.behaviors), "TypeDef", "product.schema.json")
		if (err == nil) != test.ok {
			t.Errorf("core %s: err = %v, want ok = %v", test.behaviors, err, test.ok)
		}
	}
}

// The readers name the type and the behavior when an entry is refused, and
// store each config canonically.
func TestDecodeBehaviors(t *testing.T) {
	reg := behaviorRegistry(t)
	doc, err := DecodeWith(typeWithBehaviors(`[{"name": "acme.Rating", "config": { "maxStars" : 5.0e0 }}, {"name": "acme.Flag", "config": {}}]`), "product.schema.json", reg)
	if err != nil {
		t.Fatal(err)
	}
	refs := doc.Types["Product"].Behaviors
	if len(refs) != 2 || string(refs[0].Config) != `{"maxStars":5.0e0}` || refs[1].Config != nil {
		t.Fatalf("behaviors = %+v", refs)
	}

	for _, test := range []struct {
		reg       *registry.Registry
		behaviors string
		want      string
	}{
		{reg, `[{"name": "acme.Ghost"}]`, `product.schema.json: behavior "acme.Ghost" on type "Product" is not a registered behavior (registered: Assignment, Comments, Dependencies, Lease, Links, Queue, Reactions, Revisions, Rollups, Search, Workflow, acme.Flag, acme.Rating)`},
		{core(), `[{"name": "acme.Flag"}]`, `product.schema.json: behavior "acme.Flag" on type "Product" is not a registered behavior (registered: Assignment, Comments, Dependencies, Lease, Links, Queue, Reactions, Revisions, Rollups, Search, Workflow)`},
		{reg, `[{"name": "acme.Rating", "config": {"maxStars": 12}}]`, `product.schema.json: type "Product": behavior acme.Rating config: `},
		{reg, `[{"name": "acme.Rating"}]`, `product.schema.json: type "Product": behavior acme.Rating config: `},
		{reg, `[{"name": "acme.Flag", "config": {"on": true}}]`, `product.schema.json: type "Product": behavior acme.Flag takes no config`},
	} {
		_, err := DecodeWith(typeWithBehaviors(test.behaviors), "product.schema.json", test.reg)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", test.behaviors, err, test.want)
		}
	}

	// The document form checks each type's entries the same way.
	_, err = DecodeWith([]byte(`{"types": {"Product": {"name": "Product", "role": "EmbeddedStruct", "behaviors": [{"name": "acme.Ghost"}]}}}`), "doc.schema.json", reg)
	if err == nil || !strings.Contains(err.Error(), `behavior "acme.Ghost" on type "Product" is not a registered behavior`) {
		t.Errorf("document form: err = %v", err)
	}
}
