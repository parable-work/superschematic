package apigen_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestMiddlewareImportsEachPackageOnceWithStores: with the session
// provider's stores (an upstream Session and User table), middleware.go
// imports every package once. The check runs on unformatted output
// (--skip-format), since go/format drops a duplicate import and would hide
// one the templates write.
func TestMiddlewareImportsEachPackageOnceWithStores(t *testing.T) {
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	table := func(name string, fields ...string) *ir.TypeDef {
		typeDef := &ir.TypeDef{Name: name, Role: ir.RoleDBTable}
		for _, field := range fields {
			typeDef.Fields = append(typeDef.Fields, &ir.FieldDef{Name: field})
		}
		return typeDef
	}
	dbSchema.Types["Session"] = table("Session", "id", "jti", "user", "expiresAt")
	dbSchema.Types["User"] = table("User", "id", "name")

	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       sessionauth.Provider{},
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
	if !output.Auth.HasSessionStore || !output.Auth.HasPrincipalStore {
		t.Fatalf("Auth = %+v, want both stores", output.Auth)
	}

	outDir := t.TempDir()
	if err := apigen.WriteAPIWithProfile(output, outDir, nil, true); err != nil {
		t.Fatalf("write api: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(outDir, "middleware.go"), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse middleware.go: %v", err)
	}
	seen := map[string]int{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		seen[path]++
	}
	for _, want := range []string{"time", "errors"} {
		if seen[want] == 0 {
			t.Errorf("middleware.go does not import %q", want)
		}
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("middleware.go imports %q %d times", path, count)
		}
	}
}
