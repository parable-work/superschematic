package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// goldenSDK is the TypeScript SDK that a directory of testdata/golden pins
// all or some of the files of, generated as its golden test generates it.
type goldenSDK struct {
	// apiOutput, parseable and clock are what the golden test passes
	// Generate and WriteSDKWithTools; lazy sets LazyValidation (D82).
	apiOutput *apigen.APIOutput
	parseable map[string]bool
	clock     codegen.Clock
	lazy      bool
	// types is the API's types package, which the SDK imports, then the
	// packages it depends on.
	types []goldenTypes
}

// goldenTypes is a types package a golden SDK needs: the schema named name,
// whose types package depends on deps' by name.
type goldenTypes struct {
	name   string
	schema *ir.Schema
	deps   map[string]*ir.Schema
}

// apiTypes is the types package of the API schema named name, which
// depends on no other.
func apiTypes(name string, schema *ir.Schema) []goldenTypes {
	return []goldenTypes{{name: name, schema: schema}}
}

// loadFixtureSchema loads a schema of the loader's fixture corpus.
func loadFixtureSchema(t *testing.T, name string) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return schema
}

// goldenSDKs generates the SDK of each directory of testdata/golden, as the
// test that pins it does. TestEveryGoldenSDKTypeChecks fails for a
// directory with no entry, and checks each file a directory holds against
// the SDK its entry generates, so the SDK it type-checks is the one the
// golden pins.
var goldenSDKs = map[string]func(t *testing.T) goldenSDK{
	"fixture-api": func(t *testing.T) goldenSDK {
		return fixtureAPIGolden(t, false)
	},
	"fixture-api-lazy": func(t *testing.T) goldenSDK {
		return fixtureAPIGolden(t, true)
	},
	"fixture-mcp": func(t *testing.T) goldenSDK {
		apiOutput, parseable := loadMCPFixture(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: mcpClock, types: apiTypes("fixture-mcp", loadFixtureSchema(t, "fixture-mcp"))}
	},
	"fixture-nested-arrays-api": func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadNestedArraysAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, types: apiTypes("fixture-nested-arrays-api", schema)}
	},
	pagedService: func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadPagedAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, types: apiTypes(pagedService, schema)}
	},
	"fixture-refs-api": func(t *testing.T) goldenSDK {
		apiOutput, parseable := loadRefsFixture(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: mcpClock, types: apiTypes("fixture-refs-api", loadFixtureSchema(t, "fixture-refs-api"))}
	},
	"fixture-tool-invoke-api": func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadToolInvokeAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: toolParamsClock, types: apiTypes("fixture-tool-invoke-api", schema)}
	},
	"fixture-tool-params-api": func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadToolParamsAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: toolParamsClock, types: apiTypes("fixture-tool-params-api", schema)}
	},
	userRoutesAPI: func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadUserRoutesAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, types: apiTypes(userRoutesAPI, schema)}
	},
	listFiltersGolden(false): func(t *testing.T) goldenSDK {
		return listFiltersAPIGolden(t, false)
	},
	listFiltersGolden(true): func(t *testing.T) goldenSDK {
		return listFiltersAPIGolden(t, true)
	},
	sdktest.QueryListsService: func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadQueryListsAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, types: apiTypes(sdktest.QueryListsService, schema)}
	},
	workflowAPI: workflowAPIGolden,
	sdktest.UnionListsService: func(t *testing.T) goldenSDK {
		schema, apiOutput, parseable := loadUnionListsAPI(t)
		return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, types: apiTypes(sdktest.UnionListsService, schema)}
	},
}

// fixtureAPIGolden is fixture-api's SDK, whose types package depends on
// fixture-db's, with lazyValidation when lazy is set.
func fixtureAPIGolden(t *testing.T, lazy bool) goldenSDK {
	schema, apiOutput, parseable := loadFixtureAPI(t)
	upstream := loadFixtureSchema(t, "fixture-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     codegen.DefaultClock(),
		lazy:      lazy,
		types: []goldenTypes{
			{name: "fixture-api", schema: schema, deps: map[string]*ir.Schema{"fixture-db": upstream}},
			{name: "fixture-db", schema: upstream},
		},
	}
}

// listFiltersAPIGolden is list-filters-api's SDK, with lazyValidation when
// lazy is set.
func listFiltersAPIGolden(t *testing.T, lazy bool) goldenSDK {
	schema, apiOutput, parseable := loadListFiltersAPI(t)
	return goldenSDK{apiOutput: apiOutput, parseable: parseable, clock: nestedArraysClock, lazy: lazy, types: apiTypes(sdktest.ListFiltersService, schema)}
}

// TestEveryGoldenSDKTypeChecks runs tsc --noEmit on the TypeScript SDK of
// each directory of testdata/golden, generated as its golden test
// generates it, against its generated types package (D101). A golden pins
// text, and a template can write text that only the compiler refuses: a
// call of a helper the namespace does not declare type-checked nowhere,
// as another operation in the fixture a test compiled pulled the helper
// in. Each directory needs an entry in goldenSDKs, and each file it holds
// must be what the entry writes. The types packages share one install.
func TestEveryGoldenSDKTypeChecks(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
		if goldenSDKs[entry.Name()] == nil {
			t.Errorf("testdata/golden/%s has no entry in goldenSDKs, so nothing type-checks its SDK", entry.Name())
		}
	}
	for name := range goldenSDKs {
		if _, err := os.Stat(filepath.Join("testdata", "golden", name)); err != nil {
			t.Errorf("goldenSDKs names %s, which testdata/golden does not hold: %v", name, err)
		}
	}
	sort.Strings(names)

	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	// Each SDK in a directory of its own, outside the workspace's members:
	// the lazy SDKs share their package's name with the others.
	sdkDirs := map[string]string{}
	typesOf := map[string]string{}
	types := map[string]goldenTypes{}
	for _, name := range names {
		generate := goldenSDKs[name]
		if generate == nil {
			continue
		}
		golden := generate(t)
		sdkOutput, err := Generate(golden.apiOutput, golden.parseable, golden.clock)
		if err != nil {
			t.Fatalf("%s: Generate: %v", name, err)
		}
		sdkOutput.LazyValidation = golden.lazy
		sdkDir := filepath.Join(tempRoot, "golden", name)
		if err := WriteSDKWithTools(sdkOutput, golden.apiOutput, sdkDir, golden.clock); err != nil {
			t.Fatalf("%s: WriteSDKWithTools: %v", name, err)
		}
		if !*update {
			for _, file := range listFiles(t, filepath.Join("testdata", "golden", name)) {
				got, err := os.ReadFile(filepath.Join(sdkDir, file))
				if err != nil {
					t.Fatalf("%s: read generated %s: %v", name, file, err)
				}
				want, err := os.ReadFile(filepath.Join("testdata", "golden", name, file))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Errorf("testdata/golden/%s/%s is not what goldenSDKs writes: generate it as its golden test does", name, file)
				}
			}
		}
		sdkDirs[name] = sdkDir
		typesOf[name] = golden.types[0].name
		for _, pkg := range golden.types {
			types[pkg.name] = pkg
		}
	}

	// The types packages, members of one workspace, installed once.
	typesDirs := map[string]string{}
	for name, pkg := range types {
		packages := map[string]string{}
		for dep := range pkg.deps {
			packages[dep] = naming.Default().NpmTypesPackage(dep)
		}
		tsOutput, err := tsgen.Generate(pkg.schema, tsgen.Options{
			SchemaName:         name,
			Dependencies:       pkg.deps,
			DependencyPackages: packages,
			Clock:              nestedArraysClock,
			// The behaviors a table's fields compose (D103), whose
			// repositories the types package writes.
			Registry: registry.New(naming.Default()),
		})
		if err != nil {
			t.Fatalf("tsgen.Generate %s: %v", name, err)
		}
		typesDir := filepath.Join(tempRoot, "types", "typescript", name)
		if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
			t.Fatalf("set superscalar spec for %s: %v", name, err)
		}
		if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
			t.Fatalf("write types %s: %v", name, err)
		}
		typesDirs[name] = typesDir
	}
	if err := (tsgen.WorkspaceRoot{OutputRoot: tempRoot, Paths: paths}).Write(); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = tempRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the types packages (likely offline): %v\n%s", err, out))
	}

	for _, name := range names {
		sdkDir, ok := sdkDirs[name]
		if !ok {
			continue
		}
		typesDir := typesDirs[typesOf[name]]
		if err := linkTypesPackage(typesDir, sdkDir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The types package's own TypeScript, which the SDK's
			// devDependencies pin alike.
			check := exec.Command(bunPath, "x", "tsc", "--noEmit", "--pretty", "false", "-p", sdkDir)
			check.Dir = typesDir
			if out, err := check.CombinedOutput(); err != nil {
				t.Errorf("the SDK testdata/golden/%s pins does not type-check: %v\n%s", name, err, out)
			}
		})
	}
}
