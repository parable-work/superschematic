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
)

// TestFilesImportEachPackageOnceWithTheUserModel: a public API over the
// user model (D50) imports every package once in middleware.go, routes.go
// and identity.go, the files the identity wiring adds imports to. The
// check runs on unformatted output (--skip-format), since go/format drops
// a duplicate import and would hide one the templates write.
func TestFilesImportEachPackageOnceWithTheUserModel(t *testing.T) {
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, userRoutesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", userRoutesAPI, err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, userModelDB))
	if err != nil {
		t.Fatalf("load %s: %v", userModelDB, err)
	}

	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     userRoutesAPI,
		ModulePath:     "example.com/schemas/api/" + userRoutesAPI,
		TypesModule:    "example.com/schemas/types/go/" + userRoutesAPI,
		IsPublic:       true,
		UpstreamSchema: userModelDB,
		UpstreamIR:     dbSchema,
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output.Auth.User == nil || !output.Auth.Identity {
		t.Fatalf("Auth = %+v, want the user model", output.Auth)
	}

	outDir := t.TempDir()
	if err := apigen.WriteAPIWithProfile(output, outDir, nil, true); err != nil {
		t.Fatalf("write api: %v", err)
	}
	for file, wants := range map[string][]string{
		"middleware.go": {"time"},
		"routes.go":     {"time", "github.com/parable-work/superschematic/runtime/http/go/identity"},
		"identity.go":   {"fmt", "github.com/parable-work/superschematic/runtime/http/go/identity"},
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(outDir, file), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		seen := map[string]int{}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			seen[path]++
		}
		for _, want := range wants {
			if seen[want] == 0 {
				t.Errorf("%s does not import %q", file, want)
			}
		}
		for path, count := range seen {
			if count > 1 {
				t.Errorf("%s imports %q %d times", file, path, count)
			}
		}
	}
}
