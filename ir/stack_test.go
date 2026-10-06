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
