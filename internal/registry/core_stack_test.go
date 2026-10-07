package registry

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// applyStackDecorator validates and applies one Stack decorator to td, as
// the TypeScript walker does with the evaluated argument.
func applyStackDecorator(t *testing.T, name string, td *ir.TypeDef, arg any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator(name, TargetType)
	if !ok {
		t.Fatalf("@%s is not registered", name)
	}
	if !spec.AllowsKind(string(ir.SchemaKindStack)) || spec.AllowsKind(string(ir.SchemaKindAPI)) {
		t.Fatalf("@%s must be allowed in Stack schemas only", name)
	}
	if err := spec.ValidateArgs([]any{arg}); err != nil {
		return err
	}
	return spec.Apply(Node{Type: td}, []any{arg}, Site{})
}

func handle(name, kind string) map[string]any { return map[string]any{"name": name, "kind": kind} }

func class(name string) map[string]any { return map[string]any{"class": name} }

// TestStackDecoratorsWriteTheirDeclarations: each decorator writes its
// class's declaration from the value every form gives Apply: a handle as
// {name, kind}, a class as a class reference, an environment's values
// under its target's name and an env value as a literal or a parameter.
func TestStackDecoratorsWriteTheirDeclarations(t *testing.T) {
	shop := &ir.TypeDef{Name: "Shop"}
	if err := applyStackDecorator(t, "stack", shop, map[string]any{
		"deploy": []any{handle("shop-api", "API")},
		"expose": []any{handle("shop-api", "API"), class("Backend")},
	}); err != nil {
		t.Fatal(err)
	}
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	want := &ir.StackDecl{Deploy: []ir.ServiceRef{api}, Expose: []ir.DeployableRef{{Service: &api}, {Deployable: "Backend"}}}
	if !reflect.DeepEqual(shop.Stack, want) {
		t.Errorf("@stack wrote %+v, want %+v", shop.Stack, want)
	}

	backend := &ir.TypeDef{Name: "Backend"}
	if err := applyStackDecorator(t, "server", backend, map[string]any{"serves": []any{handle("shop-api", "API")}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.Server, &ir.ServerDecl{Serves: []ir.ServiceRef{api}}) {
		t.Errorf("@server wrote %+v", backend.Server)
	}
	data := &ir.TypeDef{Name: "Data"}
	if err := applyStackDecorator(t, "database", data, map[string]any{"hosts": []any{handle("shop-db", "DB")}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.Database, &ir.DatabaseDecl{Hosts: []ir.ServiceRef{{Name: "shop-db", Kind: ir.SchemaKindDB}}}) {
		t.Errorf("@database wrote %+v", data.Database)
	}

	prod := &ir.TypeDef{Name: "Production"}
	if err := applyStackDecorator(t, "environment", prod, map[string]any{
		"target":     "gcp",
		"gcp":        map[string]any{"project": "acme-prod"},
		"domain":     "acme.dev",
		"dns":        map[string]any{"cloudflare": map[string]any{"zone": "acme.dev"}},
		"parameters": []any{"pr"},
		"settings": []any{
			map[string]any{"of": handle("shop-db", "DB"), "tier": "large"},
			map[string]any{"of": class("Backend"), "platform": "gcp.run", "env": map[string]any{
				"LOG_LEVEL": "warn", "MAX": float64(3), "PREVIEW_ID": map[string]any{"parameter": "pr"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	db := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	wantEnv := &ir.EnvironmentDecl{
		Target:     "gcp",
		Values:     map[string]any{"project": "acme-prod"},
		Domain:     "acme.dev",
		DNS:        &ir.DNSPlacement{Platform: "cloudflare", Values: map[string]any{"zone": "acme.dev"}},
		Parameters: []string{"pr"},
		Settings: []*ir.DeployableSettings{
			{Of: ir.DeployableRef{Service: &db}, Values: map[string]any{"tier": "large"}},
			{Of: ir.DeployableRef{Deployable: "Backend"}, Platform: "gcp.run", Env: map[string]ir.EnvValue{
				"LOG_LEVEL": {Value: "warn"}, "MAX": {Value: float64(3)}, "PREVIEW_ID": {Parameter: "pr"},
			}},
		},
	}
	if !reflect.DeepEqual(prod.Environment, wantEnv) {
		t.Errorf("@environment wrote %+v, want %+v", prod.Environment, wantEnv)
	}

	// A DNS platform with no values and an environment with no target
	// values leave both out, as the data forms do.
	manual := &ir.TypeDef{Name: "Manual"}
	if err := applyStackDecorator(t, "environment", manual, map[string]any{"target": "gcp", "gcp": map[string]any{}, "dns": map[string]any{"manual": map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if manual.Environment.Values != nil || !reflect.DeepEqual(manual.Environment.DNS, &ir.DNSPlacement{Platform: "manual"}) {
		t.Errorf("@environment wrote %+v", manual.Environment)
	}
}

// TestStackDecoratorsRefuseWhatTheyCannotDeclare: each refusal names what
// is wrong.
func TestStackDecoratorsRefuseWhatTheyCannotDeclare(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decorator string
		arg       any
		want      string
	}{
		{"a name in a string where a handle goes", "stack", map[string]any{"deploy": []any{"shop-api"}}, "@stack argument"},
		{"a server that serves nothing", "server", map[string]any{"serves": []any{}}, "@server argument"},
		{"a class where a handle goes", "database", map[string]any{"hosts": []any{class("Data")}}, "@database argument"},
		{"values under another name", "environment", map[string]any{"target": "gcp", "aws": map[string]any{}}, `holds values under "aws", but its target is gcp`},
		{"values without a target", "environment", map[string]any{"gcp": map[string]any{}}, `holds values under "gcp" but names no target`},
		{"two DNS platforms", "environment", map[string]any{"dns": map[string]any{"a": map[string]any{}, "b": map[string]any{}}}, "@environment argument"},
		{"a settings element without of", "environment", map[string]any{"settings": []any{map[string]any{"tier": "large"}}}, "@environment argument"},
		{"an env value that is an object", "environment", map[string]any{"settings": []any{map[string]any{"of": class("B"), "env": map[string]any{"X": map[string]any{"value": 1}}}}}, "@environment argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applyStackDecorator(t, tc.decorator, &ir.TypeDef{Name: "C"}, tc.arg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}

	td := &ir.TypeDef{Name: "Both"}
	if err := applyStackDecorator(t, "server", td, map[string]any{"serves": []any{handle("a", "API")}}); err != nil {
		t.Fatal(err)
	}
	err := applyStackDecorator(t, "environment", td, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "class Both is already an @server class") {
		t.Errorf("a second declaration on one class: %v", err)
	}
}

// findings records what a Verify reports.
type findings struct{ errs []string }

func (f *findings) Errorf(file, format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *findings) Warnf(string, string, ...any) {}

// TestVerifyStack: the Stack kind's rules over an assembled schema, which
// hold for every form.
func TestVerifyStack(t *testing.T) {
	spec, _ := New(naming.Naming{}).Kind(string(ir.SchemaKindStack))
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	valid := func() *ir.Schema {
		s := ir.NewSchema("shop-stack", ir.SchemaKindStack)
		s.Types["Shop"] = &ir.TypeDef{Name: "Shop", Stack: &ir.StackDecl{Deploy: []ir.ServiceRef{api}, Expose: []ir.DeployableRef{{Deployable: "Backend"}}}}
		s.Types["Backend"] = &ir.TypeDef{Name: "Backend", Server: &ir.ServerDecl{Serves: []ir.ServiceRef{api}}}
		// An environment with an order and one without, as a data form
		// may write them.
		s.Types["Staging"] = &ir.TypeDef{Name: "Staging", Environment: &ir.EnvironmentDecl{Target: "fake", Settings: []*ir.DeployableSettings{{Of: ir.DeployableRef{Deployable: "Backend"}}}, Order: 1}}
		s.Types["Preview"] = &ir.TypeDef{Name: "Preview", Extends: "Staging", Environment: &ir.EnvironmentDecl{}}
		return s
	}
	var f findings
	spec.Verify(valid(), &f)
	if len(f.errs) > 0 {
		t.Fatalf("a valid stack: %v", f.errs)
	}

	for _, tc := range []struct {
		name   string
		change func(*ir.Schema)
		want   string
	}{
		{"no @stack class", func(s *ir.Schema) { delete(s.Types, "Shop") }, "declares no @stack class"},
		{"two @stack classes", func(s *ir.Schema) {
			s.Types["Other"] = &ir.TypeDef{Name: "Other", Stack: &ir.StackDecl{}}
		}, "declares @stack on Other and Shop"},
		{"a class that declares nothing", func(s *ir.Schema) { s.Types["Plain"] = &ir.TypeDef{Name: "Plain"} }, "class Plain declares nothing"},
		{"a class with two declarations", func(s *ir.Schema) { s.Types["Backend"].Database = &ir.DatabaseDecl{} }, "class Backend is @server and @database"},
		{"a class with fields", func(s *ir.Schema) {
			s.Types["Staging"].Fields = []*ir.FieldDef{{Name: "region"}}
		}, "class Staging has fields"},
		{"a server that extends", func(s *ir.Schema) { s.Types["Backend"].Extends = "Shop" }, "@server class Backend extends Shop; only an @environment class extends"},
		{"an environment that extends a server", func(s *ir.Schema) { s.Types["Preview"].Extends = "Backend" }, "@environment class Preview extends Backend, which is not an @environment class"},
		{"an expose of a class that is no deployable", func(s *ir.Schema) {
			s.Types["Shop"].Stack.Expose = []ir.DeployableRef{{Deployable: "Staging"}}
		}, "@stack class Shop expose[0] names class Staging, which is not an @server or @database class"},
		{"settings of a class the schema lacks", func(s *ir.Schema) {
			s.Types["Staging"].Environment.Settings[0].Of = ir.DeployableRef{Deployable: "Missing"}
		}, "@environment class Staging settings[0] of names class Missing"},
		{"settings of nothing", func(s *ir.Schema) {
			s.Types["Staging"].Environment.Settings[0].Of = ir.DeployableRef{}
		}, "names no service and no deployable"},
		{"settings of both", func(s *ir.Schema) {
			s.Types["Staging"].Environment.Settings[0].Of = ir.DeployableRef{Service: &api, Deployable: "Backend"}
		}, "names both service shop-api and deployable Backend"},
		{"two environments with one order", func(s *ir.Schema) { s.Types["Preview"].Environment.Order = 1 }, "@environment classes Preview and Staging both have order 1"},
		{"a negative order", func(s *ir.Schema) { s.Types["Preview"].Environment.Order = -1 }, "@environment class Preview has order -1; an order counts from 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(s)
			var f findings
			spec.Verify(s, &f)
			if !strings.Contains(strings.Join(f.errs, "\n"), tc.want) {
				t.Errorf("findings = %q, want one containing %q", f.errs, tc.want)
			}
		})
	}
}
