package registry

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// declaration builds a behavior declaration from a name and the members a
// test sets, with one well-formed operation unless the test gives its own.
func declaration(name string, members map[string]any) json.RawMessage {
	decl := map[string]any{"name": name}
	for key, value := range members {
		decl[key] = value
	}
	raw, err := json.Marshal(decl)
	if err != nil {
		panic(err)
	}
	return raw
}

func operation(name string, members map[string]any) map[string]any {
	op := map[string]any{
		"name":         name,
		"paramsSchema": closedObject,
		"resultSchema": map[string]any{"type": "object"},
	}
	for key, value := range members {
		op[key] = value
	}
	return op
}

// closedObject is the smallest params schema RegisterBehavior takes: an
// object schema that admits no key it does not declare.
var closedObject = map[string]any{"type": "object", "additionalProperties": false}

const ratingConfig = `{"type":"object","required":["maxStars"],"additionalProperties":false,"properties":{"maxStars":{"type":"integer","minimum":3,"maximum":10}}}`

func ratingDeclaration() json.RawMessage {
	return declaration("acme.Rating", map[string]any{
		"description":  "Shoppers rate an item.",
		"configSchema": json.RawMessage(ratingConfig),
		"fields":       []any{map[string]any{"name": "ratingCount"}, map[string]any{"name": "ratingAverage", "description": "Mean of the ratings."}},
		"operations": []any{
			operation("rate", map[string]any{"writes": true, "invocationPolicy": "ask"}),
			operation("ratingSummary", nil),
			operation("topRated", map[string]any{"scope": "schema"}),
		},
	})
}

func TestRegisterBehavior(t *testing.T) {
	reg := New(naming.Default())
	ext := fakeExtension{name: "acme", register: func(r *Registry) error {
		if err := r.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: ratingDeclaration()}); err != nil {
			return err
		}
		return r.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Flag", nil)})
	}}
	if err := reg.Use(ext); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBehavior(BehaviorSpec{Declaration: declaration("Pinned", nil)}); err != nil {
		t.Fatalf("a core behavior with a bare name: %v", err)
	}
	finalizeWithCoreGenerators(t, reg)

	if got := reg.BehaviorNames(); !slices.Equal(got, []string{"Assignment", "Blueprint", "Budget", "Comments", "Dependencies", "Lease", "Links", "Pinned", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "Workflow", "acme.Flag", "acme.Rating"}) {
		t.Fatalf("BehaviorNames() = %v", got)
	}
	rating, ok := reg.Behavior("acme.Rating")
	if !ok {
		t.Fatal("acme.Rating is not registered")
	}
	if rating.Extension != "acme" || rating.Description != "Shoppers rate an item." || len(rating.Fields) != 2 ||
		rating.Fields[1].Description != "Mean of the ratings." || len(rating.Operations) != 3 ||
		!rating.Operations[0].Writes || rating.Operations[0].InvocationPolicy != "ask" || rating.Operations[1].Writes ||
		rating.Operations[0].Scope != "" || rating.Operations[2].Scope != OperationScopeSchema {
		t.Fatalf("acme.Rating = %+v", rating)
	}
	if !rating.ConfigRequired() {
		t.Error("acme.Rating's config schema rejects {}, so its config is required")
	}
	if flag, _ := reg.Behavior("acme.Flag"); flag.ConfigRequired() {
		t.Error("acme.Flag takes no config, so none is required")
	}
	if !slices.Contains(reg.Extensions(), "acme") {
		t.Errorf("Extensions() = %v", reg.Extensions())
	}
	if err := reg.RegisterBehavior(BehaviorSpec{Declaration: declaration("Late", nil)}); err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v", err)
	}
}

func TestBehaviorValidateConfig(t *testing.T) {
	reg := New(naming.Default())
	for _, decl := range []json.RawMessage{ratingDeclaration(), declaration("acme.Flag", nil)} {
		if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: decl}); err != nil {
			t.Fatal(err)
		}
	}
	rating, _ := reg.Behavior("acme.Rating")
	flag, _ := reg.Behavior("acme.Flag")
	for _, test := range []struct {
		behavior Behavior
		config   string
		want     string
	}{
		{rating, `{"maxStars":5}`, ""},
		{rating, `{"maxStars":11}`, "behavior acme.Rating config: "},
		{rating, `{"maxStars":5,"colour":"gold"}`, "behavior acme.Rating config: "},
		{rating, ``, "behavior acme.Rating config: "},
		{flag, ``, ""},
		{flag, `{}`, ""},
		{flag, `{"x":1}`, "behavior acme.Flag takes no config"},
		{flag, `[]`, "behavior acme.Flag takes no config"},
	} {
		err := test.behavior.ValidateConfig(json.RawMessage(test.config))
		switch {
		case test.want == "" && err != nil:
			t.Errorf("%s %s: %v", test.behavior.Name, test.config, err)
		case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s %s: err = %v, want %q", test.behavior.Name, test.config, err, test.want)
		}
	}
}

func TestRegisterBehaviorRejects(t *testing.T) {
	objectSchema := map[string]any{"type": "object"}
	for _, test := range []struct {
		name      string
		extension string
		decl      json.RawMessage
		want      string
	}{
		{"not JSON", "acme", json.RawMessage(`{"name":`), "behavior declaration of extension acme: "},
		{"unknown key", "acme", declaration("acme.Rating", map[string]any{"config": objectSchema}), `unknown field "config"`},
		{"trailing data", "", json.RawMessage(`{"name":"Pinned"} {}`), "trailing data"},
		{"no name", "", declaration("", nil), "behavior declaration of the core has no name"},
		{"core name not bare", "", declaration("pinned", nil), `core behavior name "pinned" is malformed`},
		{"core name qualified", "", declaration("acme.Pinned", nil), `core behavior name "acme.Pinned" is malformed`},
		{"extension name bare", "acme", declaration("Rating", nil), `behavior name "Rating" of extension acme must be acme.<Name>`},
		{"extension prefix", "acme", declaration("other.Rating", nil), `behavior name "other.Rating" of extension acme must be acme.<Name>`},
		{"extension name malformed", "acme", declaration("acme.rating", nil), `behavior name "acme.rating" is malformed`},
		{"extension name nested", "acme", declaration("acme.Rating.Stars", nil), `behavior name "acme.Rating.Stars" is malformed`},
		{"config schema", "acme", declaration("acme.Rating", map[string]any{"configSchema": map[string]any{"type": 7}}), "behavior acme.Rating configSchema: "},
		{"create params schema", "acme", declaration("acme.Rating", map[string]any{"createParamsSchema": map[string]any{"type": 7}}), "behavior acme.Rating createParamsSchema: "},
		{"create params not an object", "acme", declaration("acme.Rating", map[string]any{"createParamsSchema": map[string]any{"type": "array", "additionalProperties": false}}), `behavior acme.Rating createParamsSchema must be an object schema ("type": "object")`},
		{"create params a boolean schema", "acme", declaration("acme.Rating", map[string]any{"createParamsSchema": true}), `behavior acme.Rating createParamsSchema must be an object schema ("type": "object")`},
		{"create params open", "acme", declaration("acme.Rating", map[string]any{"createParamsSchema": objectSchema}), `behavior acme.Rating createParamsSchema must set "additionalProperties": false or a schema, so no create parameter goes unchecked`},
		{"create params open by true", "acme", declaration("acme.Rating", map[string]any{"createParamsSchema": map[string]any{"type": "object", "additionalProperties": true}}), `createParamsSchema must set "additionalProperties": false or a schema`},
		{"field name", "acme", declaration("acme.Rating", map[string]any{"fields": []any{map[string]any{"name": "rating count"}}}), `behavior acme.Rating field name "rating count" is not an identifier`},
		{"field twice", "acme", declaration("acme.Rating", map[string]any{"fields": []any{map[string]any{"name": "stars"}, map[string]any{"name": "stars"}}}), `behavior acme.Rating declares field "stars" twice`},
		{"operation not camelCase", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("Rate", nil)}}), `behavior acme.Rating operation name "Rate" is not camelCase`},
		{"operation snake_case", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate_item", nil)}}), `operation name "rate_item" is not camelCase`},
		{"operation twice", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", nil), operation("rate", nil)}}), `behavior acme.Rating declares operation "rate" twice`},
		{"builtin operation", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("update", nil)}}), `behavior acme.Rating operation "update" has the name of an operation every schema has (create, get, list, update, delete)`},
		{"no params schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{map[string]any{"name": "rate", "resultSchema": objectSchema}}}), "behavior acme.Rating operation rate has no paramsSchema"},
		{"no result schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{map[string]any{"name": "rate", "paramsSchema": closedObject}}}), "behavior acme.Rating operation rate has no resultSchema"},
		{"params schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": map[string]any{"minimum": "one"}})}}), "behavior acme.Rating operation rate paramsSchema: "},
		{"params not an object", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": map[string]any{"type": "integer"}})}}), `behavior acme.Rating operation rate paramsSchema must be an object schema`},
		{"params a boolean schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": false})}}), `behavior acme.Rating operation rate paramsSchema must be an object schema`},
		{"result schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"resultSchema": map[string]any{"required": "stars"}})}}), "behavior acme.Rating operation rate resultSchema: "},
		{"scope", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"scope": "type"})}}), `behavior acme.Rating operation rate scope "type" is not "instance" or "schema"`},
		{"scope case", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"scope": "Schema"})}}), `behavior acme.Rating operation rate scope "Schema" is not "instance" or "schema"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := New(naming.Default())
			err := reg.RegisterBehavior(BehaviorSpec{Extension: test.extension, Declaration: test.decl})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if names := reg.BehaviorNames(); !slices.Equal(names, coreBehaviorNames) {
				t.Fatalf("a refused declaration registered %v", names)
			}
		})
	}

	reg := New(naming.Default())
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: ratingDeclaration()}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: ratingDeclaration()}); err == nil ||
		!strings.Contains(err.Error(), `behavior "acme.Rating" is already registered`) {
		t.Fatalf("duplicate: err = %v", err)
	}
}

// A spec may name the npm package that implements its behavior, which the
// registered behavior carries for the behaviors command; a name npm would
// refuse is refused.
func TestRegisterBehaviorPackage(t *testing.T) {
	reg := New(naming.Default())
	for _, spec := range []BehaviorSpec{
		{Extension: "acme", Package: "@acme/behaviors", Declaration: ratingDeclaration()},
		{Extension: "acme", Package: "acme-behaviors", Declaration: declaration("acme.Flag", nil)},
		{Extension: "acme", Declaration: declaration("acme.Pin", nil)},
	} {
		if err := reg.RegisterBehavior(spec); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"acme.Rating": "@acme/behaviors", "acme.Flag": "acme-behaviors", "acme.Pin": ""} {
		if b, _ := reg.Behavior(name); b.Package != want {
			t.Errorf("%s Package = %q, want %q", name, b.Package, want)
		}
	}
	for _, pkg := range []string{"@Acme/behaviors", "acme behaviors", "@acme", "@/behaviors", "acme/behaviors", ".acme", strings.Repeat("a", 215)} {
		err := New(naming.Default()).RegisterBehavior(BehaviorSpec{Extension: "acme", Package: pkg, Declaration: ratingDeclaration()})
		if want := fmt.Sprintf("registry: behavior acme.Rating package %q is not an npm package name", pkg); err == nil || err.Error() != want {
			t.Errorf("package %q: err = %v, want %q", pkg, err, want)
		}
	}
}

// An operation's params schema sets "additionalProperties": false, the
// engine's rule (D16): an operation's parameters are exactly the ones it
// declares, so no alias of one reaches the handler without its guards
// seeing it. The value must be false itself; a schema that closes the
// object another way is refused as the engine refuses it.
func TestRegisterBehaviorRefusesOpenParams(t *testing.T) {
	for _, params := range []string{
		`{"type":"object"}`,
		`{"type":"object","properties":{"stars":{"type":"integer"}}}`,
		`{"type":"object","additionalProperties":true}`,
		`{"type":"object","additionalProperties":{"type":"integer"}}`,
		`{"type":"object","additionalProperties":{"not":{}}}`,
		`{"type":"object","properties":{"stars":{"type":"integer"}},"unevaluatedProperties":false}`,
	} {
		reg := New(naming.Default())
		err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Rating", map[string]any{
			"operations": []any{operation("rate", nil), operation("ratingSummary", map[string]any{"paramsSchema": json.RawMessage(params)})},
		})})
		want := `registry: behavior acme.Rating operation ratingSummary paramsSchema must set "additionalProperties": false, so its parameters are exactly the ones it declares`
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", params, err, want)
		}
		if names := reg.BehaviorNames(); !slices.Equal(names, coreBehaviorNames) {
			t.Errorf("%s: a refused declaration registered %v", params, names)
		}
	}
}

// A create's parameters for a behavior may be keyed by names its config
// gives (a link's name), so createParamsSchema may admit further keys, but
// only through "additionalProperties" set to a schema that checks them.
func TestRegisterBehaviorTakesCreateParams(t *testing.T) {
	for _, params := range []string{
		`{"type":"object","additionalProperties":false,"properties":{"blockers":{"type":"array"}}}`,
		`{"type":"object","propertyNames":{"pattern":"^[a-z]+$"},"additionalProperties":{"type":"string"}}`,
	} {
		reg := New(naming.Default())
		if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Rating", map[string]any{
			"createParamsSchema": json.RawMessage(params),
		})}); err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		rating, _ := reg.Behavior("acme.Rating")
		if string(rating.CreateParamsSchema) != params {
			t.Errorf("CreateParamsSchema = %s, want %s", rating.CreateParamsSchema, params)
		}
	}
}

// An extension registers its behaviors under its own name only: Use holds
// the spec's Extension to the extension whose Register is running, so an
// extension cannot declare a bare core name or another extension's.
func TestRegisterBehaviorHoldsTheRegisteringExtension(t *testing.T) {
	for _, test := range []struct {
		spec BehaviorSpec
		want string
	}{
		{BehaviorSpec{Declaration: declaration("Rating", nil)}, `behavior "Rating" names extension "", but extension acme is registering it`},
		{BehaviorSpec{Extension: "other", Declaration: declaration("other.Rating", nil)}, `behavior "other.Rating" names extension "other", but extension acme is registering it`},
	} {
		reg := New(naming.Default())
		err := reg.Use(fakeExtension{name: "acme", register: func(r *Registry) error { return r.RegisterBehavior(test.spec) }})
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("err = %v, want %q", err, test.want)
		}
	}
}

func TestUseRejectsADottedExtensionName(t *testing.T) {
	reg := New(naming.Default())
	registered := false
	err := reg.Use(fakeExtension{name: "acme.shop", register: func(*Registry) error { registered = true; return nil }})
	want := `extension name "acme.shop" contains a dot`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Use() = %v, want %q", err, want)
	}
	if registered {
		t.Fatal("Use ran the Register of an extension it refused")
	}
	if ferr := reg.Finalize(); ferr == nil || ferr.Error() != err.Error() {
		t.Fatalf("Finalize() = %v, want the Use error", ferr)
	}
}

// Finalize checks what a declaration names outside itself: requires and
// conflicts name registered behaviors, and each operation's invocation
// policy is a value of the policy in force, whichever extension registers
// the policy and in whatever order.
func TestFinalizeChecksBehaviorReferences(t *testing.T) {
	for _, test := range []struct {
		name  string
		decls []json.RawMessage
		want  string
	}{
		{"requires unregistered", []json.RawMessage{declaration("acme.Review", map[string]any{"requires": []string{"acme.Rating"}})},
			`behavior acme.Review requires "acme.Rating", which is not a registered behavior (registered: Assignment, Blueprint, Budget, Comments, Dependencies, Lease, Links, Presence, Queue, Reactions, Retries, Revisions, Rollups, Search, Workflow, acme.Review)`},
		{"conflicts unregistered", []json.RawMessage{declaration("acme.Review", map[string]any{"conflicts": []string{"acme.Hidden"}})},
			`behavior acme.Review conflicts "acme.Hidden", which is not a registered behavior`},
		{"requires itself", []json.RawMessage{declaration("acme.Review", map[string]any{"requires": []string{"acme.Review"}})},
			"behavior acme.Review requires itself"},
		{"requires and conflicts", []json.RawMessage{ratingDeclaration(), declaration("acme.Review", map[string]any{"requires": []string{"acme.Rating"}, "conflicts": []string{"acme.Rating"}})},
			"behavior acme.Review both requires and conflicts with acme.Rating"},
		{"policy value", []json.RawMessage{declaration("acme.Review", map[string]any{"operations": []any{operation("review", map[string]any{"invocationPolicy": "always"})}})},
			`behavior acme.Review operation review invocationPolicy "always" is not a value of the invocationPolicy policy (auto, ask)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := New(naming.Default())
			for _, decl := range test.decls {
				if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: decl}); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig"} {
				if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
					t.Fatal(err)
				}
			}
			if err := reg.Finalize(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Finalize() = %v, want %q", err, test.want)
			}
		})
	}

	// The policy another extension registers after the behavior is the one
	// the operation's value is held to.
	reg := New(naming.Default())
	behaviors := fakeExtension{name: "acme", register: func(r *Registry) error {
		return r.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Review", map[string]any{
			"operations": []any{operation("review", map[string]any{"invocationPolicy": "always"})},
		})})
	}}
	if err := reg.Use(behaviors, policyExtension{"vendor", ToolInvocationPolicy{Extension: "vendor", Key: "confirm", Values: []string{"never", "always"}, Default: "never"}}); err != nil {
		t.Fatal(err)
	}
	finalizeWithCoreGenerators(t, reg)
}

// A behavior's name after the extension's dot follows the bare-name rule,
// so the names D16 gives as examples register.
func TestBehaviorNamesFollowOneRule(t *testing.T) {
	for i, name := range []string{"StateMachine", "Links2", "X"} {
		reg := New(naming.Default())
		if err := reg.RegisterBehavior(BehaviorSpec{Declaration: declaration(name, nil)}); err != nil {
			t.Errorf("core %s: %v", name, err)
		}
		qualified := fmt.Sprintf("ext%d.%s", i, name)
		if err := reg.RegisterBehavior(BehaviorSpec{Extension: fmt.Sprintf("ext%d", i), Declaration: declaration(qualified, nil)}); err != nil {
			t.Errorf("extension %s: %v", qualified, err)
		}
	}
}
