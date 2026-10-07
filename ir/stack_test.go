package ir

import (
	"reflect"
	"testing"
)

// TestStackOfAssemblesTheDeclarations: the stack takes the schema's name,
// the @stack class's entry points and exposure, each @server and
// @database class as a declared deployable and each @environment class as
// an environment whose parent is the class it extends, in name order.
func TestStackOfAssemblesTheDeclarations(t *testing.T) {
	if StackOf(nil) != nil {
		t.Error("StackOf(nil) is a stack")
	}
	schema := NewSchema("shop-stack", SchemaKindStack)
	schema.Types["Staging"] = &TypeDef{Name: "Staging", Environment: &EnvironmentDecl{Target: "gcp"}}
	if StackOf(schema) != nil {
		t.Error("a schema with no @stack class has a stack")
	}

	api := ServiceRef{Name: "shop-api", Kind: SchemaKindAPI}
	db := ServiceRef{Name: "shop-db", Kind: SchemaKindDB}
	schema.Types["Shop"] = &TypeDef{Name: "Shop", Stack: &StackDecl{Deploy: []ServiceRef{api}, Expose: []DeployableRef{{Deployable: "Backend"}}}}
	schema.Types["Other"] = &TypeDef{Name: "Other", Stack: &StackDecl{}}
	schema.Types["Backend"] = &TypeDef{Name: "Backend", Server: &ServerDecl{Serves: []ServiceRef{api}}}
	schema.Types["Data"] = &TypeDef{Name: "Data", Database: &DatabaseDecl{Hosts: []ServiceRef{db}}}
	schema.Types["Preview"] = &TypeDef{Name: "Preview", Extends: "Staging", Environment: &EnvironmentDecl{Parameters: []string{"pr"}}}
	schema.Types["Plain"] = &TypeDef{Name: "Plain"}

	want := &Stack{
		Name: "shop-stack",
		// Other is the first @stack class by name; verification refuses
		// a schema with two.
		Deployables: []*DeployableDecl{
			{Name: "Backend", Kind: DeployableServer, Serves: []ServiceRef{api}},
			{Name: "Data", Kind: DeployableDatabase, Hosts: []ServiceRef{db}},
		},
		Environments: []*Environment{
			{Name: "Preview", Extends: "Staging", Parameters: []string{"pr"}},
			{Name: "Staging", Target: "gcp"},
		},
	}
	if got := StackOf(schema); !reflect.DeepEqual(got, want) {
		t.Errorf("StackOf = %+v, want %+v", got, want)
	}
	delete(schema.Types, "Other")
	want.Deploy, want.Expose = []ServiceRef{api}, []DeployableRef{{Deployable: "Backend"}}
	if got := StackOf(schema); !reflect.DeepEqual(got, want) {
		t.Errorf("StackOf = %+v, want %+v", got, want)
	}
}

// TestStackOfOrdersTheEnvironments: the environments come by their order,
// then those without one by name, whatever their names.
func TestStackOfOrdersTheEnvironments(t *testing.T) {
	schema := NewSchema("shop-stack", SchemaKindStack)
	schema.Types["Shop"] = &TypeDef{Name: "Shop", Stack: &StackDecl{}}
	environment := func(name string, order int) {
		schema.Types[name] = &TypeDef{Name: name, Environment: &EnvironmentDecl{Order: order}}
	}
	names := func() []string {
		var out []string
		for _, env := range StackOf(schema).Environments {
			out = append(out, env.Name)
		}
		return out
	}

	environment("Staging", 1)
	environment("Production", 2)
	environment("Preview", 3)
	if got, want := names(), []string{"Staging", "Production", "Preview"}; !reflect.DeepEqual(got, want) {
		t.Errorf("environments = %v, want %v", got, want)
	}

	// A gap changes nothing; an environment without an order comes after
	// those with one, by name.
	environment("Production", 7)
	environment("Dev", 0)
	environment("Canary", 0)
	if got, want := names(), []string{"Staging", "Preview", "Production", "Canary", "Dev"}; !reflect.DeepEqual(got, want) {
		t.Errorf("environments = %v, want %v", got, want)
	}

	// Equal orders, which verification refuses, fall back to name order,
	// so the stack stays deterministic.
	environment("Staging", 3)
	if got, want := names(), []string{"Preview", "Staging", "Production", "Canary", "Dev"}; !reflect.DeepEqual(got, want) {
		t.Errorf("environments = %v, want %v", got, want)
	}
}
