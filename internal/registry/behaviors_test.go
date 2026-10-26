package registry

import (
	"encoding/json"
	"fmt"
	"maps"
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

	if got := reg.BehaviorNames(); !slices.Equal(got, []string{"Approvals", "Assignment", "Blueprint", "Branches", "Budget", "Calendar", "Comments", "Constants", "Dependencies", "Derived", "Holds", "Lease", "Links", "Numbering", "Payments", "Pinned", "Presence", "Provenance", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "StateRules", "Stock", "Timers", "Variants", "Workflow", "acme.Flag", "acme.Rating"}) {
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

// A field may say it is volatile: it moves at a read, with no event of
// its own instance. The registered field carries the flag, and a field
// that does not say so is not.
func TestRegisterBehaviorVolatileField(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Tally", map[string]any{
		"fields": []any{map[string]any{"name": "total", "volatile": true}, map[string]any{"name": "lastAt"}},
	})}); err != nil {
		t.Fatal(err)
	}
	tally, _ := reg.Behavior("acme.Tally")
	if want := []BehaviorField{{Name: "total", Volatile: true}, {Name: "lastAt"}}; !slices.Equal(tally.Fields, want) {
		t.Fatalf("acme.Tally fields = %+v, want %+v", tally.Fields, want)
	}
}

// An instance operation that writes may say it is beside the instance,
// and with "log": false that it appends no event; a behavior with one
// declares every field volatile (D77).
func TestRegisterBehaviorBesideOperations(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Chat", map[string]any{
		"fields": []any{map[string]any{"name": "messages", "volatile": true}},
		"operations": []any{
			operation("post", map[string]any{"writes": true, "beside": true}),
			operation("markRead", map[string]any{"writes": true, "beside": true, "log": false}),
			operation("rename", map[string]any{"writes": true, "log": true}),
		},
	})}); err != nil {
		t.Fatal(err)
	}
	chat, _ := reg.Behavior("acme.Chat")
	got := make([][2]bool, len(chat.Operations))
	for i, op := range chat.Operations {
		got[i] = [2]bool{op.Beside, op.Logged()}
	}
	if want := [][2]bool{{true, true}, {true, false}, {false, true}}; !slices.Equal(got, want) {
		t.Fatalf("acme.Chat operations beside and logged = %v, want %v", got, want)
	}
}

// A declaration may give signals (D77), each a camelCase name declared
// once, a paramsSchema that compiles and an access of read, write or
// operation.
func TestRegisterBehaviorSignals(t *testing.T) {
	typing := map[string]any{"name": "typing", "description": "Who is typing.", "access": "read", "paramsSchema": map[string]any{"type": "object"}}
	reg := New(naming.Default())
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Chat", map[string]any{
		"signals": []any{
			typing,
			map[string]any{"name": "nudge", "access": "write", "paramsSchema": true},
			map[string]any{"name": "draft", "access": "operation", "paramsSchema": map[string]any{"type": "object", "required": []any{"text"}}},
		},
	})}); err != nil {
		t.Fatal(err)
	}
	chat, _ := reg.Behavior("acme.Chat")
	var got []string
	for _, signal := range chat.Signals {
		got = append(got, signal.Name+" "+signal.Access)
	}
	if want := []string{"typing read", "nudge write", "draft operation"}; !slices.Equal(got, want) {
		t.Fatalf("acme.Chat signals = %v, want %v", got, want)
	}
	for _, tc := range []struct {
		signals []any
		want    string
	}{
		{[]any{map[string]any{"name": "Typing", "access": "read", "paramsSchema": true}}, `signal name "Typing" is not camelCase`},
		{[]any{typing, typing}, `declares signal "typing" twice`},
		{[]any{map[string]any{"name": "typing", "access": "read"}}, "signal typing has no paramsSchema"},
		{[]any{map[string]any{"name": "typing", "access": "read", "paramsSchema": map[string]any{"type": 3}}}, "signal typing paramsSchema"},
		{[]any{map[string]any{"name": "typing", "access": "everyone", "paramsSchema": true}}, `signal typing access "everyone" is not "read", "write" or "operation"`},
		{[]any{map[string]any{"name": "typing", "access": "read", "paramsSchema": true, "colour": 1}}, `unknown field "colour"`},
	} {
		reg := New(naming.Default())
		err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Chat", map[string]any{"signals": tc.signals})})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("signals %v: got %v, want an error containing %q", tc.signals, err, tc.want)
		}
	}
}

// A declaration may give the schema of the precondition a caller sends
// for the behavior, held to a paramsSchema's rule, and the codes its
// vetoes carry, each lowercase snake case and listed once, beside the
// schema of the parameters a create gives it.
func TestRegisterBehaviorPreconditionAndVetoes(t *testing.T) {
	precondition := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"token"}, "properties": map[string]any{"token": map[string]any{"type": "integer"}}}
	createParams := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"holder": map[string]any{"type": "string"}}}
	reg := New(naming.Default())
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Hold", map[string]any{
		"createParamsSchema": createParams,
		"preconditionSchema": precondition,
		"vetoes":             []any{map[string]any{"code": "held", "description": "Another holds it."}, map[string]any{"code": "token_stale"}},
	})}); err != nil {
		t.Fatal(err)
	}
	hold, _ := reg.Behavior("acme.Hold")
	if len(hold.CreateParamsSchema) == 0 || len(hold.PreconditionSchema) == 0 || len(hold.Vetoes) != 2 || hold.Vetoes[0] != (BehaviorVeto{Code: "held", Description: "Another holds it."}) || hold.Vetoes[1].Code != "token_stale" {
		t.Fatalf("acme.Hold = %+v", hold)
	}

	for _, test := range []struct {
		name    string
		members map[string]any
		want    string
	}{
		{"precondition does not compile", map[string]any{"preconditionSchema": map[string]any{"type": 7}}, "registry: behavior acme.Hold preconditionSchema: "},
		{"precondition not an object", map[string]any{"preconditionSchema": map[string]any{"type": "integer"}}, `registry: behavior acme.Hold preconditionSchema must be an object schema ("type": "object")`},
		{"precondition open", map[string]any{"preconditionSchema": map[string]any{"type": "object"}}, `registry: behavior acme.Hold preconditionSchema must set "additionalProperties": false, so its members are exactly the ones it declares`},
		{"code camelCase", map[string]any{"vetoes": []any{map[string]any{"code": "tokenStale"}}}, `registry: behavior acme.Hold veto code "tokenStale" is not lowercase snake case of at most 64 characters`},
		{"code empty", map[string]any{"vetoes": []any{map[string]any{"code": ""}}}, `registry: behavior acme.Hold veto code "" is not lowercase snake case`},
		{"code double underscore", map[string]any{"vetoes": []any{map[string]any{"code": "token__stale"}}}, `veto code "token__stale" is not lowercase snake case`},
		{"code too long", map[string]any{"vetoes": []any{map[string]any{"code": strings.Repeat("a", 65)}}}, "is not lowercase snake case of at most 64 characters"},
		{"code twice", map[string]any{"vetoes": []any{map[string]any{"code": "held"}, map[string]any{"code": "held"}}}, `registry: behavior acme.Hold declares veto code "held" twice`},
		{"veto unknown key", map[string]any{"vetoes": []any{map[string]any{"code": "held", "status": 409}}}, `unknown field "status"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("acme.Hold", test.members)})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
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
		{"field volatile not a boolean", "acme", declaration("acme.Rating", map[string]any{"fields": []any{map[string]any{"name": "stars", "volatile": "yes"}}}), "behavior declaration of extension acme: "},
		{"operation not camelCase", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("Rate", nil)}}), `behavior acme.Rating operation name "Rate" is not camelCase`},
		{"operation snake_case", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate_item", nil)}}), `operation name "rate_item" is not camelCase`},
		{"operation twice", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", nil), operation("rate", nil)}}), `behavior acme.Rating declares operation "rate" twice`},
		{"builtin operation", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("update", nil)}}), `behavior acme.Rating operation "update" has the name of an operation the engine serves on a schema (create, get, list, update, delete, lookup)`},
		{"no params schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{map[string]any{"name": "rate", "resultSchema": objectSchema}}}), "behavior acme.Rating operation rate has no paramsSchema"},
		{"no result schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{map[string]any{"name": "rate", "paramsSchema": closedObject}}}), "behavior acme.Rating operation rate has no resultSchema"},
		{"params schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": map[string]any{"minimum": "one"}})}}), "behavior acme.Rating operation rate paramsSchema: "},
		{"params not an object", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": map[string]any{"type": "integer"}})}}), `behavior acme.Rating operation rate paramsSchema must be an object schema`},
		{"params a boolean schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"paramsSchema": false})}}), `behavior acme.Rating operation rate paramsSchema must be an object schema`},
		{"result schema", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"resultSchema": map[string]any{"required": "stars"}})}}), "behavior acme.Rating operation rate resultSchema: "},
		{"scope", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"scope": "type"})}}), `behavior acme.Rating operation rate scope "type" is not "instance", "schema" or "namespace"`},
		{"scope case", "acme", declaration("acme.Rating", map[string]any{"operations": []any{operation("rate", map[string]any{"scope": "Schema"})}}), `behavior acme.Rating operation rate scope "Schema" is not "instance", "schema" or "namespace"`},
		{"beside reads", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("post", map[string]any{"beside": true})}}), `behavior acme.Chat operation post is beside the instance and does not write: a beside operation declares "writes": true`},
		{"beside on the schema", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("post", map[string]any{"beside": true, "writes": true, "scope": "schema"})}}), `behavior acme.Chat operation post is beside the instance and has scope "schema": a beside operation runs on an instance`},
		{"beside field not volatile", "acme", declaration("acme.Chat", map[string]any{"fields": []any{map[string]any{"name": "count", "volatile": true}, map[string]any{"name": "last"}}, "operations": []any{operation("post", map[string]any{"beside": true, "writes": true})}}), `behavior acme.Chat field last is not volatile, and operation post is beside the instance: a behavior with a beside operation declares every field volatile, since no event records its moves`},
		{"per reader not volatile", "acme", declaration("acme.Chat", map[string]any{"fields": []any{map[string]any{"name": "unseen", "perReader": true}}}), `behavior acme.Chat field unseen is perReader but not volatile: a value each reader reads its own of is computed at a read`},
		{"per reader not a boolean", "acme", declaration("acme.Chat", map[string]any{"fields": []any{map[string]any{"name": "unseen", "volatile": true, "perReader": "yes"}}}), "behavior declaration of extension acme: "},
		{"unlogged not beside", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("mark", map[string]any{"writes": true, "log": false})}}), `behavior acme.Chat operation mark sets "log": false and is not beside the instance: only a beside operation appends no event`},
		{"beside not a boolean", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("post", map[string]any{"beside": "yes"})}}), "behavior declaration of extension acme: "},
		{"log not a boolean", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("post", map[string]any{"beside": true, "writes": true, "log": "no"})}}), "behavior declaration of extension acme: "},
		{"namespace beside", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("inbox", map[string]any{"scope": "namespace", "beside": true, "writes": true})}}), `behavior acme.Chat operation inbox has scope "namespace" and is beside the instance: a namespace operation runs on no instance`},
		{"namespace log", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("inbox", map[string]any{"scope": "namespace", "log": true})}}), `behavior acme.Chat operation inbox has scope "namespace" and sets "log": a namespace operation appends no event`},
		{"asks not read", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("mark", map[string]any{"beside": true, "writes": true, "log": false, "asks": "write"})}}), `behavior acme.Chat operation mark asks "write": the only value is "read"`},
		{"asks logged", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("mark", map[string]any{"beside": true, "writes": true, "asks": "read"})}}), `behavior acme.Chat operation mark asks "read" and is not beside the instance with "log": false: only an operation that writes its caller's own state, and appends no event, asks read`},
		{"asks not beside", "acme", declaration("acme.Chat", map[string]any{"operations": []any{operation("mark", map[string]any{"writes": true, "asks": "read"})}}), `behavior acme.Chat operation mark asks "read" and is not beside the instance`},
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

// An extension's spec may name the Rust crate that implements its
// behavior (D58), which the registered behavior carries for the
// projectengine generator; a core behavior names none, and a crate name,
// path or register expression the project's engine crate could not use is
// refused.
func TestRegisterBehaviorRust(t *testing.T) {
	rating := BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Rating"}
	reg := New(naming.Default())
	for _, spec := range []BehaviorSpec{
		{Extension: "acme", Rust: rating, Declaration: ratingDeclaration()},
		{Extension: "acme", Rust: BehaviorRust{Crate: "acme_flags", Path: "../shared/flags", Register: "acme_flags::flag::Flag::new()"}, Declaration: declaration("acme.Flag", nil)},
		{Extension: "acme", Declaration: declaration("acme.Pin", nil)},
	} {
		if err := reg.RegisterBehavior(spec); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]BehaviorRust{
		"acme.Rating": rating,
		"acme.Flag":   {Crate: "acme_flags", Path: "../shared/flags", Register: "acme_flags::flag::Flag::new()"},
		"acme.Pin":    {},
	} {
		if b, _ := reg.Behavior(name); b.Rust != want || b.Rust.Declared() != (want != BehaviorRust{}) {
			t.Errorf("%s Rust = %+v, want %+v", name, b.Rust, want)
		}
	}
	if got := rating.Ident(); got != "acme_behaviors" {
		t.Errorf("Ident() = %q", got)
	}

	for _, test := range []struct {
		extension string
		rust      BehaviorRust
		want      string
	}{
		{"", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Rating"},
			`registry: core behavior Rating names Rust crate "acme-behaviors": the core's behaviors are the engine's own crates`},
		{"acme", BehaviorRust{Crate: "1acme", Path: "crates/behaviors", Register: "acme::Rating"},
			`registry: behavior acme.Rating Rust crate "1acme" is not a Cargo package name: a letter, then letters, digits, '-' and '_', at most 64 characters`},
		{"acme", BehaviorRust{Crate: "acme behaviors", Path: "crates/behaviors", Register: "acme::Rating"},
			`registry: behavior acme.Rating Rust crate "acme behaviors" is not a Cargo package name`},
		{"acme", BehaviorRust{Crate: strings.Repeat("a", 65), Path: "crates/behaviors", Register: "a::Rating"},
			`is not a Cargo package name`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Register: "acme_behaviors::Rating"},
			`registry: behavior acme.Rating Rust crate acme-behaviors has no path: the crate's directory relative to the project, the parent of the schemas root`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "/srv/crates/behaviors", Register: "acme_behaviors::Rating"},
			`registry: behavior acme.Rating Rust crate acme-behaviors path "/srv/crates/behaviors" is an absolute path: it is relative to the project, the parent of the schemas root`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/./behaviors/", Register: "acme_behaviors::Rating"},
			`registry: behavior acme.Rating Rust crate acme-behaviors path "crates/./behaviors/" is not a clean slash-separated path (crates/behaviors)`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: `crates\behaviors`, Register: "acme_behaviors::Rating"},
			`is not a clean slash-separated path (crates/behaviors)`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors"},
			`registry: behavior acme.Rating Rust register "" is not a path into crate acme-behaviors: acme_behaviors::<Name>, or a call of one with no arguments`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme-behaviors::Rating"},
			`Rust register "acme-behaviors::Rating" is not a path into crate acme-behaviors`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "other::Rating"},
			`Rust register "other::Rating" is not a path into crate acme-behaviors`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors"},
			`Rust register "acme_behaviors" is not a path into crate acme-behaviors`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Rating::with(5)"},
			`Rust register "acme_behaviors::Rating::with(5)" is not a path into crate acme-behaviors`},
		{"acme", BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Rating;"},
			`Rust register "acme_behaviors::Rating;" is not a path into crate acme-behaviors`},
	} {
		decl := ratingDeclaration()
		if test.extension == "" {
			decl = declaration("Rating", nil)
		}
		err := New(naming.Default()).RegisterBehavior(BehaviorSpec{Extension: test.extension, Rust: test.rust, Declaration: decl})
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%+v: err = %v, want %q", test.rust, err, test.want)
		}
	}
}

// Two behaviors may share a crate, at one path: the project's engine crate
// depends on it once.
func TestFinalizeHoldsARustCrateToOnePath(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{"one path", "crates/behaviors", ""},
		{"two paths", "crates/other", `registry: behaviors acme.Flag and acme.Rating name Rust crate acme-behaviors at two paths, "crates/behaviors" and "crates/other"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := New(naming.Default())
			for _, spec := range []BehaviorSpec{
				{Extension: "acme", Rust: BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Flag"}, Declaration: declaration("acme.Flag", nil)},
				{Extension: "acme", Rust: BehaviorRust{Crate: "acme-behaviors", Path: test.path, Register: "acme_behaviors::Rating"}, Declaration: ratingDeclaration()},
			} {
				if err := reg.RegisterBehavior(spec); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig", "enginedoc", "stack", "site", "memorystore"} {
				if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
					t.Fatal(err)
				}
			}
			err := reg.Finalize()
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Finalize() = %v, want %q", err, test.want)
			}
		})
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
			`behavior acme.Review requires "acme.Rating", which is not a registered behavior (registered: Approvals, Assignment, Blueprint, Branches, Budget, Calendar, Comments, Constants, Dependencies, Derived, Holds, Lease, Links, Numbering, Payments, Presence, Provenance, Queue, Reactions, Retries, Revisions, Rollups, Search, StateRules, Stock, Timers, Variants, Workflow, acme.Review)`},
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
			for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig", "enginedoc", "stack", "site", "memorystore"} {
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

// A namespace operation (D77) registers, read-only or writing, and so does
// a beside operation with "log": false that asks read.
func TestRegisterBehaviorNamespaceOperationsAndAsksRead(t *testing.T) {
	reg := New(naming.Default())
	decl := declaration("acme.Chat", map[string]any{"operations": []any{
		operation("inbox", map[string]any{"scope": "namespace"}),
		operation("claim", map[string]any{"scope": "namespace", "writes": true}),
		operation("markRead", map[string]any{"beside": true, "writes": true, "log": false, "asks": "read"}),
	}})
	if err := reg.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: decl}); err != nil {
		t.Fatal(err)
	}
	beh, ok := reg.Behavior("acme.Chat")
	if !ok {
		t.Fatal("acme.Chat is not registered")
	}
	got := map[string][2]any{}
	for _, op := range beh.Operations {
		got[op.Name] = [2]any{op.Scope, op.AsksRead()}
	}
	want := map[string][2]any{
		"inbox":    {OperationScopeNamespace, false},
		"claim":    {OperationScopeNamespace, false},
		"markRead": {"", true},
	}
	if !maps.Equal(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}
