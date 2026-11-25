// Package replacefiles_test holds a test auth provider whose whole files
// replace core files of the api generator, and whose caller snippets bind
// a principal of its own. The provider needs its templates under a
// templates/ directory of its own, so it lives in this directory, away from
// the core's templates.
package replacefiles_test

import (
	"embed"
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

// provider writes context.go always, and middleware.go when replaceMiddleware
// is set.
type provider struct {
	replaceMiddleware bool
}

func (provider) Name() string { return "replacing" }
func (provider) Analyze(_, upstream *ir.Schema) (apigen.AuthModel, error) {
	model := apigen.AnalyzeSessionStores(upstream)
	model.Identity = false
	return model, nil
}
func (provider) Endpoint(*ir.FieldDef, *ir.OperationSet, *apigen.EndpointInfo) error { return nil }
func (provider) Templates() embed.FS                                                 { return templatesFS }
func (provider) Funcs() template.FuncMap                                             { return nil }
func (p provider) Files(*apigen.APIOutput) []codegen.ConditionalFile {
	return []codegen.ConditionalFile{
		{Condition: true, Template: "context.tmpl", Filename: "context.go"},
		{Condition: p.replaceMiddleware, Template: "middleware.tmpl", Filename: "middleware.go"},
	}
}
func (provider) OpenAPIParameters(*apigen.APIOutput) []map[string]any { return nil }

func writeFixtureAPI(t *testing.T, p apigen.AuthProvider) string {
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
		Provider:       p,
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
	return dir
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// TestProviderFileReplacesTheCoreFileOfItsName: a provider file whose
// condition holds and whose name is a core file's is the only copy written;
// one whose condition does not hold leaves the core file in place.
func TestProviderFileReplacesTheCoreFileOfItsName(t *testing.T) {
	dir := writeFixtureAPI(t, provider{})

	context := read(t, dir, "context.go")
	if !strings.Contains(context, "replacing test provider") || strings.Contains(context, "LoggerFromContext") {
		t.Errorf("context.go is not the provider's file alone:\n%s", context)
	}
	middleware := read(t, dir, "middleware.go")
	if !strings.Contains(middleware, "func LoggerMiddleware(") || strings.Contains(middleware, "replacedMiddleware") {
		t.Errorf("middleware.go is not the core's file while the provider's condition is false:\n%s", middleware)
	}

	dir = writeFixtureAPI(t, provider{replaceMiddleware: true})
	middleware = read(t, dir, "middleware.go")
	if !strings.Contains(middleware, "replacedMiddleware") || strings.Contains(middleware, "LoggerMiddleware") {
		t.Errorf("middleware.go is not the provider's file alone:\n%s", middleware)
	}
}

// TestCallerBindsTheProvidersPrincipal: caller.go imports and calls what
// the provider's caller snippets name, not the session runtime.
func TestCallerBindsTheProvidersPrincipal(t *testing.T) {
	caller := read(t, writeFixtureAPI(t, provider{}), "caller.go")
	if !strings.Contains(caller, "return orm.WithCaller(ctx, callerID(ctx))") {
		t.Errorf("caller.go does not bind the provider's principal:\n%s", caller)
	}
	if strings.Contains(caller, "session") {
		t.Errorf("caller.go still names the session runtime:\n%s", caller)
	}
}
