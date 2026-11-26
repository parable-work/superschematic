package providerhooks_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"testing"
)

// configFields lists the field names of the Config struct in routes.go.
func configFields(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(dir, "routes.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}
	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Config" {
			return true
		}
		for _, field := range spec.Type.(*ast.StructType).Fields.List {
			for _, name := range field.Names {
				names = append(names, name.Name)
			}
		}
		return false
	})
	return names
}

// TestRoutesConfigFieldsReachEveryAPI: the provider's Config fields are in
// the Config of a public API and of one that is not, right before
// Implementations.
func TestRoutesConfigFieldsReachEveryAPI(t *testing.T) {
	for _, public := range []bool{true, false} {
		_, dir := writeFixtureAPIWith(t, public)
		fields := configFields(t, dir)
		at := slices.Index(fields, "StepGuard")
		if at < 0 || at+1 >= len(fields) || fields[at+1] != "Implementations" {
			t.Errorf("public %v: Config fields %v do not hold StepGuard right before Implementations", public, fields)
		}
	}
}
