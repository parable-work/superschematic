package toolsutil_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestToolSchemasGoldenUserRoutes pins the tool schemas of
// fixture-user-routes-api, whose @userSessions and @userAdministration sets
// the loader fills from its authDb (D50): password and enum input fields,
// path-only PUT and DELETE routes, and boolean results, built as any other
// operation's. Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenUserRoutes -update
func TestToolSchemasGoldenUserRoutes(t *testing.T) {
	const fixture = "fixture-user-routes-api"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: fixture,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tools := buildToolSchemas(t, output)
	if len(tools) != 19 {
		t.Fatalf("got %d tools, want the 18 operations of the two sets and greet", len(tools))
	}
	compareToolSchemasGolden(t, fixture, tools)
}
