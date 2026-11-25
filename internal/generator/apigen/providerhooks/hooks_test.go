// Package providerhooks_test holds a test auth provider whose route
// snippets write marker calls, and checks where the api generator puts
// each one in a route's middleware list. The provider needs its templates
// under a templates/ directory of its own, so it lives in this directory.
package providerhooks_test

import (
	"bytes"
	"embed"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

const fixturesDir = "../../../loader/tsreader/testdata/services"

type provider struct{}

func (provider) Name() string { return "route-hooks" }
func (provider) Analyze(_, upstream *ir.Schema) (apigen.AuthModel, error) {
	model := apigen.AnalyzeSessionStores(upstream)
	model.Identity = false
	return model, nil
}
func (provider) Endpoint(*ir.FieldDef, *ir.OperationSet, *apigen.EndpointInfo) error { return nil }
func (provider) Templates() embed.FS                                                 { return templatesFS }
func (provider) Funcs() template.FuncMap                                             { return nil }
func (provider) Files(*apigen.APIOutput) []codegen.ConditionalFile                   { return nil }
func (provider) OpenAPIParameters(*apigen.APIOutput) []map[string]any                { return nil }

// writeFixtureAPI writes fixture-api with the test provider and returns its
// output and its directory.
func writeFixtureAPI(t *testing.T) (*apigen.APIOutput, string) {
	t.Helper()
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       provider{},
		SchemaName:     "fixture-api",
		ModulePath:     "example.com/schemas/api/fixture-api",
		TypesModule:    "example.com/schemas/types/go/fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/fixture-api"); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	dir := t.TempDir()
	if err := apigen.WriteAPI(output, dir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	return output, dir
}

// routeSteps maps each route that publicAPIRoutes or protectedAPIRoutes
// lists, "METHOD path", to the function each of its middlewares calls, in
// order.
func routeSteps(t *testing.T, dir string) map[string][]string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(dir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "routes.go", src, 0)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}
	text := func(n ast.Node) string {
		var b bytes.Buffer
		if err := format.Node(&b, fset, n); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	steps := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "publicAPIRoutes" && fn.Name.Name != "protectedAPIRoutes") {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var method, path string
			var calls []string
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				switch text(kv.Key) {
				case "Method":
					method = strings.Trim(text(kv.Value), `"`)
				case "Path":
					path = strings.Trim(text(kv.Value), `"`)
				case "Middlewares":
					for _, call := range kv.Value.(*ast.CompositeLit).Elts {
						calls = append(calls, text(call.(*ast.CallExpr).Fun))
					}
				}
			}
			if method == "" {
				return true
			}
			steps[method+" "+path] = calls
			return false
		})
	}
	return steps
}

// TestRouteMiddlewaresFirstRunsBeforeEveryOtherStep: the provider's first
// snippet is the first middleware of each route it writes for, before the
// rate limit and body limit, and a route with no other middleware gets a
// list for it.
func TestRouteMiddlewaresFirstRunsBeforeEveryOtherStep(t *testing.T) {
	output, dir := writeFixtureAPI(t)
	steps := routeSteps(t, dir)
	checked, alone := 0, 0
	for _, ep := range output.Endpoints {
		if ep.ManualRouteRegistration || ep.IdentityOperation != "" {
			continue
		}
		got, mounted := steps[ep.Method+" "+ep.RoutePath]
		if !mounted {
			continue
		}
		hasFirst := len(got) > 0 && got[0] == "firstStep"
		if ep.RequiresAuth != hasFirst {
			t.Errorf("%s %s (auth %v): steps %v", ep.Method, ep.RoutePath, ep.RequiresAuth, got)
		}
		for i, step := range got {
			if step == "firstStep" && i != 0 {
				t.Errorf("%s %s: firstStep is step %d of %v", ep.Method, ep.RoutePath, i, got)
			}
		}
		if hasFirst {
			checked++
			if len(got) == 1 {
				alone++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no route carries the first snippet")
	}
	if alone == 0 {
		t.Error("no route has the first snippet alone, so the test does not show that it opens a list")
	}
}
