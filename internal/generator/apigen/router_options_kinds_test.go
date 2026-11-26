package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestGraphOperationRoutesCompileUnderTheRouterOptions builds and vets the
// Go API module of fixture-graph-api over its database (D138), whose graph
// operations are routes, with the [go_api] table at its defaults and with
// every key set, and checks that the option run's graph routes read their
// inputs as the table says.
func TestGraphOperationRoutesCompileUnderTheRouterOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	for _, set := range []bool{false, true} {
		api, db := loadFixturePair(t, graphFixtures)
		apiDir, _ := writeFixtureModulesWith(t, graphFixtures, api, db, func(output *apigen.APIOutput) {
			if set {
				output.Naming.GoAPI = routerOptions
			}
		})
		for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = apiDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("options %t: go %s in the generated API module: %v\n%s", set, strings.Join(args, " "), err, out)
			}
		}
		routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
		if err != nil {
			t.Fatal(err)
		}
		hasOptions := strings.Contains(string(routes), "bodyargs.FirstValue()")
		if hasOptions != set {
			t.Errorf("options %t: the graph routes pass FirstValue: %t", set, hasOptions)
		}
	}
}

// withRouterOptions sets every key of the [go_api] table on the options a
// module is generated with, as routerOptions does.
func withRouterOptions(options *apigen.Options) {
	names := options.Naming.OrDefault()
	names.GoAPI = routerOptions
	options.Naming = names
}

// TestFileTransferRoutesUnderTheRouterOptions runs the file transfer
// routes' own test (testdata/files_routes_test.go: uploads, inline and
// cached downloads, D81 and D141) against the module generated with every
// [go_api] key set.
func TestFileTransferRoutesUnderTheRouterOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, filesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", filesAPI, err)
	}
	apiDir := writeGoAPIModuleOver(t, schema, filesAPI, nil, nil, withRouterOptions)
	source, err := os.ReadFile(filepath.Join("testdata", "files_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "files_routes_test.go"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	mode := "package " + filesAPIPackage + "_test\n\nfunc init() { routerOptionsSet = true }\n"
	if err := os.WriteFile(filepath.Join(apiDir, "router_options_mode_test.go"), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}

// filesAPIPackage is the test package name of fixture-files-api's module.
const filesAPIPackage = "fixturefilesapi"

// TestIdempotentRoutesUnderTheRouterOptions runs the @idempotent routes'
// own test (testdata/idempotent_routes_test.go, D140) against the module
// generated with every [go_api] key set.
func TestIdempotentRoutesUnderTheRouterOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, table, ddl := idempotentOrders(t)
	apiDir := writeGoAPIModuleOver(t, schema, idempotentAPI, nil, nil, func(options *apigen.Options) {
		options.IdempotencyTable = table
		withRouterOptions(options)
	})
	source, err := os.ReadFile(filepath.Join("testdata", "idempotent_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"idempotent_routes_test.go": source, "idempotency.sql": []byte(ddl)} {
		if err := os.WriteFile(filepath.Join(apiDir, name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGoAPIModule(t, apiDir)
}
