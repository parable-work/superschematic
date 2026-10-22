package enginedoc_test

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// stubMetaSchema stands in for the binary's json-schema output, which
// TestBuildReadsTheRegistrysMetaSchema checks Build takes by default.
var stubMetaSchema = json.RawMessage(`{"$comment":"the meta-schema"}`)

func coreRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	return registry.New(naming.Default())
}

func build(t *testing.T, schema *ir.Schema, opts enginedoc.Options) *enginedoc.Document {
	t.Helper()
	if opts.Registry == nil {
		opts.Registry = coreRegistry(t)
	}
	if opts.MetaSchema == nil {
		opts.MetaSchema = stubMetaSchema
	}
	if opts.SchemaName == "" {
		opts.SchemaName = schema.Name
	}
	doc, err := enginedoc.Build(schema, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return doc
}

// schemaFile decodes the document's schema-file document named name.
func schemaFile(t *testing.T, doc *enginedoc.Document, name string) map[string]any {
	t.Helper()
	for _, raw := range doc.Schemas {
		var file map[string]any
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		if file["name"] == name {
			return file
		}
	}
	t.Fatalf("the document holds no schema %s", name)
	return nil
}

// Build writes one General document per engine type, named after it and
// sorted by name: the type, the value types it reaches, the enums and the
// scalars their fields use, and nothing else. The type is a General
// document's struct whose fields name no base or trait; a scalar the
// core's catalog holds is its name and language primitive.
func TestBuildWritesAGeneralDocumentPerEngineType(t *testing.T) {
	doc := build(t, desk(), enginedoc.Options{})
	var names []string
	for _, raw := range doc.Schemas {
		var file struct{ Name string }
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		names = append(names, file.Name)
	}
	if !reflect.DeepEqual(names, []string{"Account", "Ticket"}) {
		t.Fatalf("schemas = %v, want Account, Ticket", names)
	}

	ticket := schemaFile(t, doc, "Ticket")
	if ticket["kind"] != "General" {
		t.Errorf("kind = %v, want General", ticket["kind"])
	}
	for _, absent := range []string{"imports", "unions", "operationSets"} {
		if _, ok := ticket[absent]; ok {
			t.Errorf("Ticket's document has %s", absent)
		}
	}
	types := ticket["types"].(map[string]any)
	if got := keys(types); !reflect.DeepEqual(got, []string{"Address", "Ticket"}) {
		t.Errorf("types = %v, want Address, Ticket", got)
	}
	want := map[string]any{
		"name": "Ticket", "role": "EmbeddedStruct",
		"behaviors": []any{
			map[string]any{"name": "Workflow", "config": map[string]any{"states": []any{"open", "done"}, "transitions": []any{map[string]any{"from": "open", "to": "done"}}}},
			map[string]any{"name": "Links", "config": map[string]any{"links": map[string]any{"account": map[string]any{"required": true, "schema": "Account"}}}},
			map[string]any{"name": "Comments"},
		},
		"fields": []any{
			map[string]any{"name": "title", "typeRef": map[string]any{"name": "string"}, "required": true},
			map[string]any{"name": "priority", "typeRef": map[string]any{"name": "Priority"}, "required": true},
			map[string]any{"name": "shipTo", "typeRef": map[string]any{"name": "Address"}, "required": true},
			map[string]any{"name": "dueAt", "typeRef": map[string]any{"name": "Temporal.DateTime"}, "required": true},
		},
	}
	if !reflect.DeepEqual(types["Ticket"], want) {
		got, _ := json.MarshalIndent(types["Ticket"], "", "  ")
		t.Errorf("Ticket =\n%s", got)
	}
	if address := types["Address"].(map[string]any); address["role"] != "EmbeddedStruct" || address["jsonField"] != nil {
		t.Errorf("Address = %v, want a struct with no jsonField", address)
	}
	if got := ticket["enums"]; !reflect.DeepEqual(got, map[string]any{"Priority": map[string]any{"name": "Priority", "values": []any{map[string]any{"name": "Low"}, map[string]any{"name": "High"}}}}) {
		t.Errorf("enums = %v", got)
	}
	if got := ticket["scalars"]; !reflect.DeepEqual(got, map[string]any{"Temporal.DateTime": map[string]any{"name": "Temporal.DateTime", "languagePrimitive": "string"}}) {
		t.Errorf("scalars = %v", got)
	}

	account := schemaFile(t, doc, "Account")
	if _, ok := account["enums"]; ok {
		t.Errorf("Account's document holds an enum no field of it uses: %v", account["enums"])
	}
}

// A scalar the core's catalog does not hold, the schema's own or an
// extension's, is the whole definition the loader read: the engine knows
// only its own catalog.
func TestBuildDeclaresAScalarTheEngineDoesNotKnowInFull(t *testing.T) {
	s := desk()
	s.Scalars["Desk.Code"] = &ir.ScalarDef{Name: "Desk.Code", LanguagePrimitive: ir.LanguageString, Pattern: "^[A-Z]{3}$", MaxLength: 3}
	s.Types["Account"].Fields = append(s.Types["Account"].Fields, field("code", str("Desk.Code")))
	scalars := schemaFile(t, build(t, s, enginedoc.Options{}), "Account")["scalars"].(map[string]any)
	if want := (map[string]any{"name": "Desk.Code", "languagePrimitive": "string", "pattern": "^[A-Z]{3}$", "maxLength": float64(3)}); !reflect.DeepEqual(scalars["Desk.Code"], want) {
		t.Errorf("Desk.Code = %v, want %v", scalars["Desk.Code"], want)
	}
}

// A type the schema imports from a General dependency is read from it,
// with what it reaches there, so the document needs no imports.
func TestBuildInlinesAnImportedType(t *testing.T) {
	s := desk()
	s.Imports = []ir.Import{{Package: "@schemas/geo", Types: []string{"Point"}}}
	s.Types["Account"].Fields = append(s.Types["Account"].Fields, field("location", str("Point")))
	geo := ir.NewSchema("geo", ir.SchemaKindGeneral)
	geo.Types["Point"] = &ir.TypeDef{Name: "Point", Role: ir.RoleEmbeddedStruct, Owner: "src/geo.schema.ts", Fields: []*ir.FieldDef{field("lat", str("number")), field("datum", str("Datum"))}}
	geo.Enums["Datum"] = &ir.EnumDef{Name: "Datum", Values: []ir.EnumValueDef{{Name: "WGS84"}}}
	account := schemaFile(t, build(t, s, enginedoc.Options{Dependencies: map[string]*ir.Schema{"geo": geo}}), "Account")
	if got := keys(account["types"].(map[string]any)); !reflect.DeepEqual(got, []string{"Account", "Point"}) {
		t.Errorf("types = %v, want Account, Point", got)
	}
	if got := keys(account["enums"].(map[string]any)); !reflect.DeepEqual(got, []string{"Datum"}) {
		t.Errorf("enums = %v, want Datum", got)
	}

	if _, err := enginedoc.Build(s, enginedoc.Options{SchemaName: "desk-db", Registry: coreRegistry(t), MetaSchema: stubMetaSchema}); err == nil ||
		!strings.Contains(err.Error(), "engine type Account: field Account.location has type Point, which is not a primitive, nor a type, an enum or a scalar of desk-db or of a dependency the build loaded") {
		t.Errorf("Build without the dependency: err = %v", err)
	}
}

// The namespace is outputs.engine.namespace, else the service's name, and
// either must be an engine namespace name.
func TestNamespace(t *testing.T) {
	for _, tc := range []struct{ service, configured, want, err string }{
		{"desk-db", "", "desk-db", ""},
		{"desk-db", "support", "support", ""},
		{"Desk_DB", "", "", `the engine namespace is the service's name, "Desk_DB", which is not an engine namespace name (lowercase letters, digits and hyphens, starting with a letter, at most 63 characters): set outputs.engine.namespace`},
		{"Desk_DB", "desk", "desk", ""},
		{"desk-db", "Support", "", `outputs.engine.namespace "Support" is not an engine namespace name`},
		{"desk-db", strings.Repeat("a", 64), "", "is not an engine namespace name"},
	} {
		got, err := enginedoc.Namespace(tc.service, tc.configured)
		switch {
		case tc.err == "" && (err != nil || got != tc.want):
			t.Errorf("Namespace(%q, %q) = %q, %v, want %q", tc.service, tc.configured, got, err, tc.want)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("Namespace(%q, %q): err = %v, want %q", tc.service, tc.configured, err, tc.err)
		}
	}
	if doc := build(t, desk(), enginedoc.Options{Namespace: "support"}); doc.Namespace != "support" {
		t.Errorf("Namespace = %q, want support", doc.Namespace)
	}
}

// helpdesk is an extension that declares a behavior with its npm package
// and its Rust crate, its own invocation policy and a tool hook that renames the scalar key
// and adds an argument-schema key.
type helpdesk struct{}

func (helpdesk) Name() string { return "helpdesk" }

func (helpdesk) Register(r *registry.Registry) error {
	return errors.Join(
		r.RegisterBehavior(registry.BehaviorSpec{
			Extension: "helpdesk", Package: "@helpdesk/behaviors",
			Rust:        registry.BehaviorRust{Crate: "helpdesk-behaviors", Path: "crates/behaviors", Register: "helpdesk_behaviors::Triage"},
			Declaration: json.RawMessage(`{"name":"helpdesk.Triage"}`),
		}),
		r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{Extension: "helpdesk", Key: "confirm", Values: []string{"never", "always"}, Default: "never"}),
		r.RegisterToolHook(registry.ToolHook{Name: "keys", Extension: "helpdesk", Edit: func(_ *ir.Schema, set *apigen.ToolSet) error {
			set.Keys.Scalar = "x-helpdesk-scalar"
			set.Keys.Parameters = append(set.Keys.Parameters, apigen.ToolKeyValue{Key: "x-helpdesk-arguments", Value: 1})
			return nil
		}}),
	)
}

// The document carries the D11 options this binary registers, the core's
// by default, and each behavior with what implements it: the core engine,
// the work queue or the extension's package and crate.
func TestBuildCarriesTheToolOptionsAndTheBehaviors(t *testing.T) {
	doc := build(t, desk(), enginedoc.Options{})
	want := enginedoc.Tools{
		InvocationPolicy: enginedoc.InvocationPolicy{Key: "invocationPolicy", Values: []string{"auto", "ask"}, Default: "auto"},
		Keys:             enginedoc.ToolKeys{Scalar: "x-superschematic-scalar", Guidance: "superschematic/operation-guidance", Parameters: []enginedoc.ToolParameter{}},
	}
	if !reflect.DeepEqual(doc.Tools, want) {
		t.Errorf("Tools = %+v, want %+v", doc.Tools, want)
	}
	if want := []enginedoc.Behavior{{Name: "Comments", Package: "@superschematic/engine"}, {Name: "Links", Package: "@superschematic/engine"}, {Name: "Workflow", Package: "@superschematic/engine"}}; !reflect.DeepEqual(doc.Behaviors, want) {
		t.Errorf("Behaviors = %+v, want %+v", doc.Behaviors, want)
	}

	reg := coreRegistry(t)
	if err := reg.Use(helpdesk{}); err != nil {
		t.Fatal(err)
	}
	s := desk()
	s.Types["Account"].Behaviors = []ir.BehaviorRef{{Name: "helpdesk.Triage"}, {Name: "Lease"}}
	doc = build(t, s, enginedoc.Options{Registry: reg})
	want = enginedoc.Tools{
		InvocationPolicy: enginedoc.InvocationPolicy{Key: "confirm", Values: []string{"never", "always"}, Default: "never"},
		Keys:             enginedoc.ToolKeys{Scalar: "x-helpdesk-scalar", Guidance: "superschematic/operation-guidance", Parameters: []enginedoc.ToolParameter{{Key: "x-helpdesk-arguments", Value: 1}}},
	}
	if !reflect.DeepEqual(doc.Tools, want) {
		t.Errorf("Tools = %+v, want %+v", doc.Tools, want)
	}
	if want := []enginedoc.Behavior{
		{Name: "Comments", Package: "@superschematic/engine"},
		{Name: "Lease", Package: "@superschematic/engine-workqueue"},
		{Name: "Links", Package: "@superschematic/engine"},
		{Name: "Workflow", Package: "@superschematic/engine"},
		{Name: "helpdesk.Triage", Extension: "helpdesk", Package: "@helpdesk/behaviors", Crate: "helpdesk-behaviors"},
	}; !reflect.DeepEqual(doc.Behaviors, want) {
		t.Errorf("Behaviors = %+v, want %+v", doc.Behaviors, want)
	}
}

// outputs.engine.tools.invocation reaches the document's tools when it
// names engine tools and operations of the behaviors the engine types
// compose, each with a value of the binary's policy (D70); Build refuses
// one that does not, naming every problem.
func TestBuildCarriesTheDeploymentsInvocationPolicies(t *testing.T) {
	s := desk()
	s.Types["Account"].Behaviors = append(s.Types["Account"].Behaviors,
		ir.BehaviorRef{Name: "Payments", Config: json.RawMessage(`{"amount":{"field":"due"},"currency":"GBP","record":{"permission":"payments.record"}}`)},
		ir.BehaviorRef{Name: "Stock", Config: json.RawMessage(`{"permission":"stock.manage"}`)})
	invocation := map[string]string{"Payments.capturePayment": "ask", "Payments.refundPayment": "ask", "Stock.adjustStock": "ask", "delete": "ask"}
	doc := build(t, s, enginedoc.Options{Invocation: invocation})
	if !reflect.DeepEqual(doc.Tools.Invocation, invocation) {
		t.Errorf("Tools.Invocation = %v, want %v", doc.Tools.Invocation, invocation)
	}
	data, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `"invocation": {
      "Payments.capturePayment": "ask",`; !strings.Contains(string(data), want) {
		t.Errorf("the document's tools lack the invocation:\n%s", data)
	}

	_, err = enginedoc.Build(s, enginedoc.Options{SchemaName: "desk-db", Registry: coreRegistry(t), MetaSchema: stubMetaSchema, Invocation: map[string]string{
		"Payments.capturePay":  "ask",
		"Holds.retakeHolds":    "ask",
		"Payments.voidPayment": "confirm",
		"publish":              "ask",
		"acme.Rating.rate":     "ask",
		"Comments.comment":     "ask",
	}})
	for _, want := range []string{
		"outputs.engine.tools.invocation: ",
		"Holds.retakeHolds names behavior Holds, which no engine type composes",
		"Payments.capturePay names no operation of Payments (openPayment, recordPaymentEvent, capturePayment, voidPayment, refundPayment, listPaymentActions, refundFor, settleRefundFor)",
		`the policy of Payments.voidPayment, "confirm", is not one of invocationPolicy's values (auto, ask)`,
		"acme.Rating.rate names behavior acme.Rating, which no engine type composes",
		"publish is neither an engine tool (create, get, list",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Build = %v, want it refused saying %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "Comments.comment") {
		t.Errorf("Build refused Comments.comment, an operation of a composed behavior: %v", err)
	}
}

// Without a meta-schema of its own, Build takes the registry's: what
// superschematic json-schema prints.
func TestBuildReadsTheRegistrysMetaSchema(t *testing.T) {
	doc, err := enginedoc.Build(desk(), enginedoc.Options{SchemaName: "desk-db", Registry: coreRegistry(t)})
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(doc.MetaSchema, &meta); err != nil || meta.Defs["BehaviorRef"] == nil {
		t.Errorf("MetaSchema is not the schema-file definition: %v", err)
	}
	data, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") || !strings.HasPrefix(string(data), "{\n  \"version\": 1,\n  \"namespace\": \"desk-db\",\n  \"tools\": {") {
		t.Errorf("JSON() does not start with version, namespace and tools:\n%.200s", data)
	}
}

// A schema with no engine type has no document.
func TestBuildWithoutEngineTypes(t *testing.T) {
	s := desk()
	delete(s.Types, "Account")
	delete(s.Types, "Ticket")
	doc, err := enginedoc.Build(s, enginedoc.Options{SchemaName: "desk-db", Registry: coreRegistry(t)})
	if doc != nil || err != nil {
		t.Fatalf("Build = %v, %v, want nil, nil", doc, err)
	}
	if problems := enginedoc.Problems(s); problems != nil {
		t.Fatalf("Problems = %v", problems)
	}
}

// Build refuses what Problems finds, naming each problem.
func TestBuildRefusesWhatProblemsFinds(t *testing.T) {
	s := desk()
	s.Types["Customer"].Fields = append(s.Types["Customer"].Fields, field("account", str("Account")))
	s.Types["Ticket"].Fields = append(s.Types["Ticket"].Fields, field("customer", str("Customer")))
	_, err := enginedoc.Build(s, enginedoc.Options{SchemaName: "desk-db", Registry: coreRegistry(t), MetaSchema: stubMetaSchema})
	for _, problem := range enginedoc.Problems(s) {
		if err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("Build: err = %v, want it to name %q", err, problem)
		}
	}
	if len(enginedoc.Problems(s)) != 2 {
		t.Errorf("Problems = %q, want two", enginedoc.Problems(s))
	}
}

func keys(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}
