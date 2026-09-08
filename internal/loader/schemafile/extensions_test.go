package schemafile

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite the JSON Schema golden")

// shelf is the fixture field decorator's argument, as in the design doc's
// acme-schematic example.
type shelf struct {
	Aisle int     `json:"aisle"`
	Z     float64 `json:"z,omitempty"`
}

type acmeField struct {
	Shelf *shelf `json:"shelf,omitempty"`
}

var shelfArgs = json.RawMessage(`{"type":"object","required":["aisle"],
  "properties":{"aisle":{"type":"integer","minimum":0},"z":{"type":"number"}},
  "additionalProperties":false}`)

// fixtureRegistry is the core registry plus the extensions the tests in
// this file author against: "acme" with one decorator per target and two
// documents, "other" and "empty" with root data only.
func fixtureRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	pkgs := []string{"@acme/schematic"}
	specs := []registry.DecoratorSpec{
		{Name: "tagged", Extension: "acme", Packages: pkgs, Target: registry.TargetType,
			Apply: func(registry.Node, []any, registry.Site) error { return nil }},
		{Name: "shelf", Extension: "acme", Packages: pkgs, Target: registry.TargetField, Args: shelfArgs,
			Apply: func(n registry.Node, args []any, _ registry.Site) error {
				var s shelf
				if err := registry.DecodeArgs(args, &s); err != nil {
					return err
				}
				return ir.UpdateExtension(n.Field, "acme", func(f *acmeField) { f.Shelf = &s })
			}},
		{Name: "audited", Extension: "acme", Packages: pkgs, Target: registry.TargetOperationSet,
			Apply: func(registry.Node, []any, registry.Site) error { return nil }},
		{Name: "audited", Extension: "acme", Packages: pkgs, Target: registry.TargetOperation,
			Apply: func(registry.Node, []any, registry.Site) error { return nil }},
	}
	for _, spec := range specs {
		if err := reg.RegisterDecorator(spec); err != nil {
			t.Fatal(err)
		}
	}
	docs := []registry.DocumentSpec{
		{Name: "catalogConfig", Extension: "acme", Schema: json.RawMessage(`{"type":"object","properties":{"shelves":{"type":"integer"}}}`)},
		{Name: "otherConfig", Extension: "other"},
		{Name: "cfg", Extension: "empty"},
	}
	for _, spec := range docs {
		if err := reg.RegisterDocument(spec); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// TestDefinitionGolden pins the emitted JSON Schema byte for byte. Regenerate
// with: go test ./internal/loader/schemafile -run TestDefinitionGolden -update
func TestDefinitionGolden(t *testing.T) {
	got, err := Definition()
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	goldenPath := filepath.Join("testdata", "schema-file.golden.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("JSON Schema output changed; run with -update and review the diff")
	}
}

// Every IR node that carries Extensions exposes an optional "extensions"
// object in the JSON Schema; the root Document also exposes "documents".
func TestDefinitionExtensionSlots(t *testing.T) {
	data, err := Definition()
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode: %v", err)
	}

	slots := map[string][]string{
		"Document":     {"extensions", "documents"},
		"TypeDef":      {"extensions"},
		"FieldDef":     {"extensions"},
		"OperationSet": {"extensions"},
	}
	for defName, props := range slots {
		def, ok := root.Defs[defName]
		if !ok {
			t.Fatalf("no $defs/%s", defName)
		}
		for _, prop := range props {
			p, ok := def.Properties[prop]
			if !ok {
				t.Errorf("$defs/%s has no %q property", defName, prop)
				continue
			}
			if p["type"] != "object" {
				t.Errorf("$defs/%s/%s type = %v, want object", defName, prop, p["type"])
			}
			for _, r := range def.Required {
				if r == prop {
					t.Errorf("$defs/%s requires %q; it must stay optional", defName, prop)
				}
			}
		}
	}
}

func TestDecodeAndMergeExtensions(t *testing.T) {
	reg := fixtureRegistry(t)
	docJSON := `{
		"name": "fixture",
		"kind": "DB",
		"extensions": {"acme": {"catalog": true}},
		"documents": {"catalogConfig": {"shelves": 3}},
		"types": {
			"Item": {
				"name": "Item",
				"role": "DBTable",
				"extensions": {"acme": {"tagged": true}},
				"fields": [{
					"name": "slug",
					"typeRef": {"name": "Identity.Slug"},
					"unique": true,
					"extensions": {"acme": {"shelf": {"aisle": 3}}}
				}]
			}
		},
		"operationSets": [{
			"name": "ItemOps",
			"operations": [],
			"extensions": {"acme": {"audited": true}}
		}]
	}`
	doc, err := DecodeWith([]byte(docJSON), "fixture.schema.json", reg)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	schema := ir.NewSchema("fixture", ir.SchemaKindDB)
	if errs := MergeWith(doc, schema, "fixture.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge: %v", errs)
	}

	if got := string(schema.Extensions["acme"]); got != `{"catalog":true}` {
		t.Errorf("Schema.Extensions[acme] = %s", got)
	}
	if got := string(schema.Documents["catalogConfig"]); got != `{"shelves":3}` {
		t.Errorf("Schema.Documents[catalogConfig] = %s", got)
	}
	item := schema.Types["Item"]
	if got := string(item.Extensions["acme"]); got != `{"tagged":true}` {
		t.Errorf("TypeDef.Extensions[acme] = %s", got)
	}
	if !item.Fields[0].Unique {
		t.Errorf("typed decorator lost beside extension: %+v", item.Fields[0])
	}
	if got := string(item.Fields[0].Extensions["acme"]); got != `{"shelf":{"aisle":3}}` {
		t.Errorf("FieldDef.Extensions[acme] = %s", got)
	}
	if got := string(schema.OperationSets[0].Extensions["acme"]); got != `{"audited":true}` {
		t.Errorf("OperationSet.Extensions[acme] = %s", got)
	}

	// The merged IR round-trips through JSON with the same bytes per slot.
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back ir.Schema
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(back.Types["Item"].Fields[0].Extensions["acme"]) != `{"shelf":{"aisle":3}}` {
		t.Errorf("round trip lost field extension: %s", data)
	}
}

func TestDecodeRootExtensionsOnlyIsADocument(t *testing.T) {
	reg := fixtureRegistry(t)
	doc, err := DecodeWith([]byte(`{"documents": {"catalogConfig": {"shelves": 3}}}`), "docs.schema.json", reg)
	if err != nil {
		t.Fatalf("Decode documents-only payload: %v", err)
	}
	if len(doc.Documents) != 1 {
		t.Errorf("documents = %v", doc.Documents)
	}
	doc, err = DecodeWith([]byte(`{"extensions": {"acme": {"catalog": true}}}`), "ext.schema.json", reg)
	if err != nil {
		t.Fatalf("Decode extensions-only payload: %v", err)
	}
	if len(doc.Extensions) != 1 {
		t.Errorf("extensions = %v", doc.Extensions)
	}
}

func TestMergeRootExtensionsAcrossDocuments(t *testing.T) {
	reg := fixtureRegistry(t)
	schema := ir.NewSchema("fixture", ir.SchemaKindDB)

	first := &Document{
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)},
		Documents:  map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":3}`)},
	}
	if errs := MergeWith(first, schema, "a.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge first: %v", errs)
	}

	// A second file adding a different extension and document is fine.
	second := &Document{
		Extensions: map[string]json.RawMessage{"other": json.RawMessage(`{"x":1}`)},
		Documents:  map[string]json.RawMessage{"otherConfig": json.RawMessage(`{"y":2}`)},
	}
	if errs := MergeWith(second, schema, "b.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge second: %v", errs)
	}
	if len(schema.Extensions) != 2 || len(schema.Documents) != 2 {
		t.Errorf("merged extensions=%v documents=%v", schema.Extensions, schema.Documents)
	}

	// Restating the same key with the same bytes is idempotent.
	if errs := MergeWith(first, schema, "a-again.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge restated: %v", errs)
	}

	// Restating a key with different content is an error naming the key.
	conflict := &Document{
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":false}`)},
		Documents:  map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":4}`)},
	}
	errs := MergeWith(conflict, schema, "c.schema.json", reg)
	if len(errs) != 2 {
		t.Fatalf("Merge conflict errs = %v, want 2", errs)
	}
	joined := errs[0].Error() + errs[1].Error()
	for _, want := range []string{`extension "acme"`, `document "catalogConfig"`, "c.schema.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors %q do not mention %q", joined, want)
		}
	}
	if string(schema.Extensions["acme"]) != `{"catalog":true}` {
		t.Errorf("conflicting merge overwrote extension: %s", schema.Extensions["acme"])
	}
}

// The data forms follow the SetExtension rule: an extension object with no
// decorators is dropped, so `acme: {}` and no `acme` key persist identically.
// A document that is `{}` stays, as with SetDocument.
func TestDecodeCanonicalizesAndDropsEmptyExtensions(t *testing.T) {
	reg := fixtureRegistry(t)
	docJSON := `{
		"name": "fixture",
		"kind": "DB",
		"extensions": {"acme": {}, "other": {"b": 1, "a": [2, 1]}},
		"documents": {"catalogConfig": {}},
		"types": {
			"Item": {
				"name": "Item",
				"role": "DBTable",
				"extensions": {"acme": { }},
				"fields": [{
					"name": "slug",
					"typeRef": {"name": "Identity.Slug"},
					"extensions": {"acme": {"shelf": {"z": 1.0, "aisle": 3}}}
				}]
			}
		},
		"operationSets": [{
			"name": "ItemOps",
			"operations": [{"name": "list", "typeRef": {"name": "Item"}, "extensions": {"acme": {}}}],
			"extensions": {"acme": {}}
		}]
	}`
	doc, err := DecodeWith([]byte(docJSON), "fixture.schema.json", reg)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := doc.Extensions["acme"]; ok || len(doc.Extensions) != 1 {
		t.Errorf("root extensions = %v", doc.Extensions)
	}
	if got := string(doc.Extensions["other"]); got != `{"a":[2,1],"b":1}` {
		t.Errorf("root extensions[other] = %s", got)
	}
	if got := string(doc.Documents["catalogConfig"]); got != `{}` {
		t.Errorf("documents = %v", doc.Documents)
	}
	item := doc.Types["Item"]
	if len(item.Extensions) != 0 {
		t.Errorf("type extensions = %v", item.Extensions)
	}
	if got := string(item.Fields[0].Extensions["acme"]); got != `{"shelf":{"aisle":3,"z":1.0}}` {
		t.Errorf("field extensions[acme] = %s", got)
	}
	set := doc.OperationSets[0]
	if len(set.Extensions) != 0 || len(set.Operations[0].Extensions) != 0 {
		t.Errorf("operation set extensions = %v, operation extensions = %v", set.Extensions, set.Operations[0].Extensions)
	}

	schema := ir.NewSchema("fixture", ir.SchemaKindDB)
	if errs := MergeWith(doc, schema, "fixture.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge: %v", errs)
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if n := strings.Count(string(data), `"extensions"`); n != 2 {
		t.Errorf("persisted IR carries %d extensions keys, want 2 (root other, field acme):\n%s", n, data)
	}
}

// Merge canonicalizes a hand-built document the same way Decode does, so a
// caller that skips Decode (the writer's tests, a future walker) cannot
// persist non-canonical bytes.
func TestMergeCanonicalizesRootSlots(t *testing.T) {
	reg := fixtureRegistry(t)
	schema := ir.NewSchema("fixture", ir.SchemaKindDB)
	doc := &Document{
		Extensions: map[string]json.RawMessage{
			"acme":  json.RawMessage(`{ "b": 1, "a": 2 }`),
			"empty": json.RawMessage(`{}`),
		},
		Documents: map[string]json.RawMessage{"cfg": json.RawMessage(`{ "b": 1, "a": 2 }`)},
	}
	if errs := MergeWith(doc, schema, "a.schema.json", reg); len(errs) != 0 {
		t.Fatalf("Merge: %v", errs)
	}
	if len(schema.Extensions) != 1 || string(schema.Extensions["acme"]) != `{"a":2,"b":1}` {
		t.Errorf("extensions = %v", schema.Extensions)
	}
	if string(schema.Documents["cfg"]) != `{"a":2,"b":1}` {
		t.Errorf("documents = %v", schema.Documents)
	}

	// Restating with different whitespace and key order is not a conflict.
	restated := &Document{
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"a":2,"b":1}`)},
		Documents:  map[string]json.RawMessage{"cfg": json.RawMessage(`{"a": 2, "b": 1}`)},
	}
	if errs := MergeWith(restated, schema, "b.schema.json", reg); len(errs) != 0 {
		t.Errorf("Merge restated: %v", errs)
	}

	malformed := &Document{Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{`)}}
	errs := MergeWith(malformed, schema, "c.schema.json", reg)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `extension "acme"`) {
		t.Errorf("Merge malformed: %v", errs)
	}
}

// A one-definition document that also carries root extensions or documents
// has no single-definition file form; writing it as one would drop them.
func TestSingleDefinitionKeepsRootSlotsInDocumentForm(t *testing.T) {
	doc := &Document{Types: map[string]*ir.TypeDef{"Item": {Name: "Item", Role: ir.RoleDBTable}}}
	if _, _, ok := SingleDefinition(doc); !ok {
		t.Fatal("one type with no root data is a single definition")
	}
	doc.Extensions = map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)}
	if _, _, ok := SingleDefinition(doc); ok {
		t.Error("root extensions must force the document form")
	}
	doc.Extensions = nil
	doc.Documents = map[string]json.RawMessage{"cfg": json.RawMessage(`{}`)}
	if _, _, ok := SingleDefinition(doc); ok {
		t.Error("root documents must force the document form")
	}
}

// The extension slots close to the registry: an unregistered extension or
// document name is rejected by name, at the node that carries it, before
// JSON Schema validation gets to it.
func TestDecodeRejectsUnregisteredSlotNames(t *testing.T) {
	reg := fixtureRegistry(t)
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"root extension", `{"extensions": {"nope": {"a": 1}}}`,
			`ext.schema.json: extensions key "nope" on the document is not a registered extension (registered: acme, empty, other)`},
		{"root document", `{"documents": {"nope": {}}}`,
			`ext.schema.json: documents key "nope" on the document is not a registered document (registered: catalogConfig, cfg, otherConfig)`},
		{"type", `{"types": {"Item": {"name": "Item", "role": "DBTable", "extensions": {"nope": {}}}}}`,
			`ext.schema.json: extensions key "nope" on type "Item" is not a registered extension (registered: acme, empty, other)`},
		{"field", `{"name": "Item", "role": "DBTable", "fields": [{"name": "slug", "typeRef": {"name": "Identity.Slug"}, "extensions": {"nope": {}}}]}`,
			`ext.schema.json: extensions key "nope" on type "Item" field "slug" is not a registered extension (registered: acme, empty, other)`},
		{"operation", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "list", "typeRef": {"name": "Item"}, "extensions": {"nope": {}}}]}`,
			`ext.schema.json: extensions key "nope" on operation set "Ops" operation "list" is not a registered extension (registered: acme, empty, other)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeWith([]byte(tc.payload), "ext.schema.json", reg)
			if err == nil || err.Error() != tc.want {
				t.Errorf("DecodeWith error = %v\nwant %s", err, tc.want)
			}
		})
	}

	// Against the core registry nothing is registered, and Merge says so for
	// a hand-built document too.
	_, err := Decode([]byte(`{"extensions": {"acme": {}}}`), "core.schema.json")
	want := `core.schema.json: extensions key "acme" on the document is not a registered extension (none are registered)`
	if err == nil || err.Error() != want {
		t.Errorf("Decode error = %v\nwant %s", err, want)
	}
	errs := Merge(&Document{Documents: map[string]json.RawMessage{"cfg": json.RawMessage(`{}`)}}, ir.NewSchema("s", ir.SchemaKindDB), "core.schema.json")
	want = `core.schema.json: documents key "cfg" on the document is not a registered document (none are registered)`
	if len(errs) != 1 || errs[0].Error() != want {
		t.Errorf("Merge errs = %v\nwant %s", errs, want)
	}
}

// A data-form extension decorator runs through the same DecoratorSpec as the
// TS form: the key must be a decorator of that extension for that target,
// its value is validated against Args, and Apply runs against the node.
func TestMergeRunsExtensionDecorators(t *testing.T) {
	reg := fixtureRegistry(t)
	decode := func(t *testing.T, fields string) (*Document, []error) {
		t.Helper()
		doc, err := DecodeWith([]byte(`{"name": "fixture", "kind": "DB", "types": {"Item": {
			"name": "Item", "role": "DBTable", "extensions": {"acme": {"tagged": true}},
			"fields": [{"name": "slug", "typeRef": {"name": "Identity.Slug"}, "extensions": {"acme": `+fields+`}}]}}}`), "fixture.schema.json", reg)
		if err != nil {
			return nil, []error{err}
		}
		schema := ir.NewSchema("fixture", ir.SchemaKindDB)
		return doc, MergeWith(doc, schema, "fixture.schema.json", reg)
	}

	doc, errs := decode(t, `{"shelf": {"aisle": 3}}`)
	if len(errs) != 0 {
		t.Fatalf("Merge: %v", errs)
	}
	fd := doc.Types["Item"].Fields[0]
	got, ok, err := ir.GetExtension[acmeField](fd, "acme")
	if err != nil || !ok || got.Shelf == nil || got.Shelf.Aisle != 3 {
		t.Errorf("Apply did not land: %+v ok=%v err=%v", got, ok, err)
	}
	if string(fd.Extensions["acme"]) != `{"shelf":{"aisle":3}}` {
		t.Errorf("field extension bytes = %s", fd.Extensions["acme"])
	}

	// The JSON Schema closes the decorator vocabulary per extension.
	_, errs = decode(t, `{"nope": {"aisle": 3}}`)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "fixture.schema.json") || !strings.Contains(errs[0].Error(), "additional properties") {
		t.Errorf("unknown decorator errs = %v", errs)
	}
	_, errs = decode(t, `{"shelf": {"aisle": -1}}`)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "minimum") {
		t.Errorf("bad args errs = %v", errs)
	}

	// Merge alone (no Decode) reaches the same DecoratorSpec checks.
	schema := ir.NewSchema("fixture", ir.SchemaKindAPI)
	hand := &Document{Types: map[string]*ir.TypeDef{"Item": {Name: "Item", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "slug", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"audited":true}`)}},
	}}}}
	errs = MergeWith(hand, schema, "hand.schema.json", reg)
	want := `hand.schema.json: type "Item" field "slug": extension "acme" has no decorator "audited" for a field`
	if len(errs) != 1 || errs[0].Error() != want {
		t.Errorf("Merge errs = %v\nwant %s", errs, want)
	}
	hand.Types["Item"].Fields[0].Extensions["acme"] = json.RawMessage(`{"shelf":{"aisle":"x"}}`)
	errs = MergeWith(hand, ir.NewSchema("fixture", ir.SchemaKindAPI), "hand2.schema.json", reg)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `@shelf argument`) {
		t.Errorf("Merge errs = %v", errs)
	}
}

// DefinitionFor composes the registry into the JSON Schema: kinds, closed
// extension slots with each decorator's Args, closed documents with each
// document's Schema. Two registries in one process get two schemas.
func TestDefinitionForComposesRegistry(t *testing.T) {
	reg := fixtureRegistry(t)
	if err := reg.RegisterKind(registry.KindSpec{Name: "Catalog", Extension: "acme", StructRole: ir.RoleEmbeddedStruct}); err != nil {
		t.Fatal(err)
	}
	data, err := DefinitionFor(reg)
	if err != nil {
		t.Fatalf("DefinitionFor: %v", err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	compact := func(raw json.RawMessage) string {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	kind := compact(root.Defs["Document"].Properties["kind"])
	if !strings.Contains(kind, `"Catalog"`) || !strings.Contains(kind, `"DB"`) {
		t.Errorf("Document.kind = %s", kind)
	}
	fieldExt := compact(root.Defs["FieldDef"].Properties["extensions"])
	var wantArgs any
	if err := json.Unmarshal(shelfArgs, &wantArgs); err != nil {
		t.Fatal(err)
	}
	wantArgsJSON, _ := json.Marshal(wantArgs)
	if !strings.Contains(fieldExt, `"shelf":`+string(wantArgsJSON)) {
		t.Errorf("FieldDef.extensions does not carry shelfArgs: %s", fieldExt)
	}
	if !strings.Contains(fieldExt, `"audited":{"const":true}`) {
		t.Errorf("FieldDef.extensions does not carry the operation decorator: %s", fieldExt)
	}
	if !strings.Contains(fieldExt, `"other":{"additionalProperties":false,"type":"object"}`) {
		t.Errorf("FieldDef.extensions.other is not a closed empty object: %s", fieldExt)
	}
	typeExt := compact(root.Defs["TypeDef"].Properties["extensions"])
	if !strings.Contains(typeExt, `"tagged":{"const":true}`) || strings.Contains(typeExt, "shelf") {
		t.Errorf("TypeDef.extensions = %s", typeExt)
	}
	docs := compact(root.Defs["Document"].Properties["documents"])
	if !strings.Contains(docs, `"catalogConfig":{"properties":{"shelves":{"type":"integer"}},"type":"object"}`) ||
		!strings.Contains(docs, `"cfg":{"type":"object"}`) || !strings.Contains(docs, `"additionalProperties":false`) {
		t.Errorf("Document.documents = %s", docs)
	}

	coreData, err := Definition()
	if err != nil {
		t.Fatal(err)
	}
	if string(coreData) == string(data) {
		t.Error("core and fixture registries produced the same JSON Schema")
	}
	again, _ := DefinitionFor(reg)
	if string(again) != string(data) {
		t.Error("DefinitionFor is not cached per registry")
	}

	// A second registry with its own decorator composes independently.
	broken := registry.New(naming.Default())
	if err := broken.RegisterDecorator(registry.DecoratorSpec{Name: "x", Extension: "b", Packages: []string{"@b/x"}, Target: registry.TargetField,
		Args: json.RawMessage(`{"type":"object"}`), Apply: func(registry.Node, []any, registry.Site) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if _, err := DefinitionFor(broken); err != nil {
		t.Errorf("valid Args rejected: %v", err)
	}

	// A registry compiled core-only and then given an extension recompiles
	// instead of validating against the stale schema.
	late := registry.New(naming.Default())
	before, err := DefinitionFor(late)
	if err != nil {
		t.Fatal(err)
	}
	if err := late.RegisterDocument(registry.DocumentSpec{Name: "lateConfig", Extension: "late"}); err != nil {
		t.Fatal(err)
	}
	after, err := DefinitionFor(late)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) || !strings.Contains(string(after), `"lateConfig"`) {
		t.Error("DefinitionFor served the core-only schema after an extension was registered")
	}
}
