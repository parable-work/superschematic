package providerhooks_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestRoutesDeclarationsDeclareWhatTheRouteSnippetsCall: routes.go declares
// the marker steps the route snippets call, once each, at the top level.
func TestRoutesDeclarationsDeclareWhatTheRouteSnippetsCall(t *testing.T) {
	_, dir := writeFixtureAPI(t)
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "routes.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}
	declared := map[string]int{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			declared[fn.Name.Name]++
		}
	}
	for _, name := range []string{"firstStep", "permissionStep", "afterDecryptStep"} {
		if declared[name] != 1 {
			t.Errorf("routes.go declares %s %d times, want once", name, declared[name])
		}
	}
}
